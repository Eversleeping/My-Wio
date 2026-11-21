package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/hostplatform"
	"github.com/wio-platform/wio/internal/protocol"
)

const maxAgentUpdateSize = 256 << 20

type agentUpdateManifest struct {
	Version         string `json:"version"`
	Binary          string `json:"binary"`
	SHA256          string `json:"sha256"`
	State           string `json:"state"`
	PreviousBinary  string `json:"previous_binary,omitempty"`
	PreviousVersion string `json:"previous_version,omitempty"`
	PreviousSHA256  string `json:"previous_sha256,omitempty"`
	AllowRollback   bool   `json:"allow_rollback,omitempty"`
}

func (c *Client) stageAgentUpdate(ctx context.Context, command protocol.AgentUpdateCommand) (restartPath string, updateErr error) {
	c.seenMu.Lock()
	if c.updatePending {
		c.seenMu.Unlock()
		return "", errors.New("an Agent update is already in progress")
	}
	if err := c.agentUpdateBlocker(); err != nil {
		c.seenMu.Unlock()
		return "", err
	}
	// handleOperation refuses non-restart-safe work once this is set, so the
	// scan above stays valid until exec.
	c.updatePending = true
	c.seenMu.Unlock()
	defer func() {
		if updateErr != nil || restartPath == "" {
			c.releaseAgentUpdate()
		}
	}()
	if command.Rollback {
		return stageAgentRollback(c.config.StateDir)
	}
	if strings.TrimSpace(command.Version) == "" {
		return "", errors.New("Agent update version is required")
	}
	if !buildinfo.UpdateAvailable(buildinfo.Version, command.Version) {
		return "", fmt.Errorf("Agent update version %s is not newer than %s", command.Version, buildinfo.Version)
	}
	pkg, ok := command.Packages[runtime.GOOS+"-"+runtime.GOARCH]
	if !ok && runtime.GOOS == "linux" {
		pkg, ok = command.Packages[runtime.GOARCH]
	}
	if !ok {
		return "", fmt.Errorf("Agent update does not support architecture %s", runtime.GOARCH)
	}
	if pkg.Size <= 0 || pkg.Size > maxAgentUpdateSize {
		return "", errors.New("Agent update package size is invalid")
	}
	expectedHash, err := hex.DecodeString(pkg.SHA256)
	if err != nil || len(expectedHash) != sha256.Size {
		return "", errors.New("Agent update checksum is invalid")
	}
	downloadURL, err := agentUpdateURL(c.config.ControlURL, pkg.URL)
	if err != nil {
		return "", err
	}
	updatesRoot := filepath.Join(c.config.StateDir, "updates")
	targetDir := filepath.Join(updatesRoot, strings.ToLower(pkg.SHA256))
	targetPath := filepath.Join(targetDir, agentExecutableName())
	if matchesFileHash(targetPath, pkg.SHA256) {
		if matchesCurrentExecutable(targetPath) {
			return "", nil
		}
	} else {
		if err := os.MkdirAll(targetDir, 0o750); err != nil {
			return "", err
		}
		if err := c.downloadAgentUpdate(ctx, downloadURL, targetPath, pkg.Size, pkg.SHA256); err != nil {
			return "", err
		}
	}
	previous, previousVersion, previousSHA := "", "", ""
	if old, _, err := readAgentUpdateManifest(updatesRoot); err == nil {
		previous, previousVersion, previousSHA = old.Binary, old.Version, old.SHA256
		if previous == filepath.ToSlash(filepath.Join(strings.ToLower(pkg.SHA256), agentExecutableName())) {
			previous, previousVersion, previousSHA = old.PreviousBinary, old.PreviousVersion, old.PreviousSHA256
		}
	}
	manifest := agentUpdateManifest{
		Version:         command.Version,
		Binary:          filepath.ToSlash(filepath.Join(strings.ToLower(pkg.SHA256), agentExecutableName())),
		SHA256:          strings.ToLower(pkg.SHA256),
		State:           "pending",
		PreviousBinary:  previous,
		PreviousVersion: previousVersion,
		PreviousSHA256:  previousSHA,
	}
	if err := writeAgentUpdateManifest(updatesRoot, manifest); err != nil {
		return "", err
	}
	return targetPath, nil
}

// Called under seenMu. syscall.Exec wipes every goroutine without sending its
// result, so any active execution that is not restart-safe blocks the update.
// The running agent.update itself is exempt; updatePending serializes those.
func (c *Client) agentUpdateBlocker() error {
	for _, execution := range c.seen {
		if execution.kind == protocol.OpAgentUpdate || execution.kind == protocol.OpAgentAccessUpdate || execution.kind == protocol.OpAgentRestart || protocol.SafeDuringAgentRestart(execution.kind) {
			continue
		}
		select {
		case <-execution.done:
			continue
		default:
		}
		switch execution.kind {
		case protocol.OpGeminiTurnStart, protocol.OpGeminiTurnRewrite, protocol.OpGeminiThreadFork, protocol.OpClaudeTurnStart, protocol.OpClaudeThreadFork, protocol.OpGitWorktreeCreate, protocol.OpGeminiSkillsManage:
			return errors.New("finish or interrupt active Gemini or Claude tasks before updating the Agent")
		}
		return fmt.Errorf("finish active operations before updating the Agent (%s is still running)", execution.kind)
	}
	return nil
}

const (
	agentUpdateFlushTimeout = 10 * time.Second
	// Stream.Send returns once gRPC has queued the frame; give its writer a
	// moment to put the bytes on the wire before the process image is replaced.
	agentUpdateFlushGrace = 250 * time.Millisecond
)

func waitForSent(sent <-chan struct{}, timeout time.Duration) bool {
	if sent == nil {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-sent:
		time.Sleep(agentUpdateFlushGrace)
		return true
	case <-timer.C:
		return false
	}
}

func (c *Client) agentUpdatePending() bool {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	return c.updatePending
}

func (c *Client) releaseAgentUpdate() {
	c.seenMu.Lock()
	c.updatePending = false
	c.seenMu.Unlock()
}

func agentUpdateURL(controlURL, packageURL string) (string, error) {
	base, err := url.Parse(controlURL)
	if err != nil || base.Host == "" || base.Path != "" && base.Path != "/" {
		return "", errors.New("Agent control URL is invalid")
	}
	relative, err := url.Parse(packageURL)
	if err != nil || relative.IsAbs() || relative.Host != "" || !strings.HasPrefix(relative.Path, "/api/agent/update-package/") {
		return "", errors.New("Agent update package URL is invalid")
	}
	return base.ResolveReference(relative).String(), nil
}

func (c *Client) downloadAgentUpdate(ctx context.Context, source, target string, expectedSize int64, expectedSHA string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.config.AgentToken)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.config.InsecureSkipVerify}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Agent update download failed (%d): %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".wio-agent-update-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, expectedSize+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != expectedSize {
		return fmt.Errorf("Agent update size mismatch: received %d bytes, expected %d", written, expectedSize)
	}
	actualSHA := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actualSHA, expectedSHA) {
		return errors.New("Agent update checksum mismatch")
	}
	if err := os.Chmod(temporaryPath, 0o750); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, target); err != nil {
		return err
	}
	return nil
}

func ActivateCurrentUpdate(stateDir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	updatesRoot := filepath.Join(stateDir, "updates")
	manifest, binary, err := readAgentUpdateManifest(updatesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		_ = os.Remove(filepath.Join(updatesRoot, "current.json"))
		return err
	}
	if !matchesFileHash(binary, manifest.SHA256) {
		previous, restoreErr := restorePreviousAgentUpdate(updatesRoot, manifest)
		if restoreErr != nil {
			return restoreErr
		}
		if previous != "" {
			return execAgentBinary(previous)
		}
		return errors.New("current Agent update checksum is invalid")
	}
	if matchesCurrentExecutable(binary) {
		return nil
	}
	if !manifest.AllowRollback && !buildinfo.UpdateAvailable(buildinfo.Version, manifest.Version) {
		_ = os.Remove(filepath.Join(updatesRoot, "current.json"))
		return nil
	}
	if manifest.State == "attempting" {
		previous, err := restorePreviousAgentUpdate(updatesRoot, manifest)
		if err != nil {
			return err
		}
		if previous != "" {
			return execAgentBinary(previous)
		}
		return fmt.Errorf("Agent update %s did not start successfully and was rolled back", manifest.Version)
	}
	// A confirmed binary remains healthy across boots, including control-plane
	// outages. Only its first, unconfirmed activation participates in rollback.
	previousState := manifest.State
	if manifest.State != "healthy" {
		manifest.State = "attempting"
		if err := writeAgentUpdateManifest(updatesRoot, manifest); err != nil {
			return err
		}
	}
	if err := execAgentBinary(binary); err != nil {
		manifest.State = previousState
		_ = writeAgentUpdateManifest(updatesRoot, manifest)
		return err
	}
	return nil
}

func activateStagedUpdate(stateDir, expectedPath string) error {
	if expectedPath == "rollback-base" {
		base := hostplatform.Current().Binary
		if _, err := os.Stat(base); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(stateDir, "updates", "current.json")); err != nil {
			return err
		}
		return execAgentBinary(base)
	}
	updatesRoot := filepath.Join(stateDir, "updates")
	manifest, binary, err := readAgentUpdateManifest(updatesRoot)
	if err != nil {
		return err
	}
	if !samePath(binary, expectedPath) || !matchesFileHash(binary, manifest.SHA256) {
		return errors.New("staged Agent update changed before activation")
	}
	manifest.State = "attempting"
	if runtime.GOOS == "windows" {
		manifest.State = "pending"
	}
	if err := writeAgentUpdateManifest(updatesRoot, manifest); err != nil {
		return err
	}
	if err := execAgentBinary(binary); err != nil {
		manifest.State = "pending"
		_ = writeAgentUpdateManifest(updatesRoot, manifest)
		return err
	}
	return nil
}

func confirmCurrentUpdate(stateDir string) (bool, error) {
	updatesRoot := filepath.Join(stateDir, "updates")
	manifest, binary, err := readAgentUpdateManifest(updatesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !matchesCurrentExecutable(binary) {
		return false, nil
	}
	if manifest.State == "healthy" {
		return false, pruneAgentUpdates(updatesRoot, manifest)
	}
	manifest.State = "healthy"
	if err := writeAgentUpdateManifest(updatesRoot, manifest); err != nil {
		return false, err
	}
	return true, pruneAgentUpdates(updatesRoot, manifest)
}

func readAgentUpdateManifest(updatesRoot string) (agentUpdateManifest, string, error) {
	raw, err := os.ReadFile(filepath.Join(updatesRoot, "current.json"))
	if err != nil {
		return agentUpdateManifest{}, "", err
	}
	var manifest agentUpdateManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return agentUpdateManifest{}, "", err
	}
	relative := filepath.Clean(filepath.FromSlash(manifest.Binary))
	if manifest.Version == "" || manifest.SHA256 == "" || filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return agentUpdateManifest{}, "", errors.New("current Agent update manifest is invalid")
	}
	binary := filepath.Join(updatesRoot, relative)
	return manifest, binary, nil
}

func writeAgentUpdateManifest(updatesRoot string, manifest agentUpdateManifest) error {
	if err := os.MkdirAll(updatesRoot, 0o750); err != nil {
		return err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(updatesRoot, ".current-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceFile(temporaryPath, filepath.Join(updatesRoot, "current.json"))
}

func matchesFileHash(path, expected string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected)
}

func matchesCurrentExecutable(path string) bool {
	executable, err := os.Executable()
	return err == nil && samePath(executable, path)
}

func samePath(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo)
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func replaceFile(source, target string) error { return hostplatform.ReplaceFile(source, target) }

// Keep both the confirmed binary and the previous version for recovery. Only
// package directories named by a SHA256 digest belong to this cleanup policy.
func pruneAgentUpdates(root string, manifest agentUpdateManifest) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, binary := range []string{manifest.Binary, manifest.PreviousBinary} {
		clean := filepath.Clean(filepath.FromSlash(binary))
		parts := strings.Split(clean, string(filepath.Separator))
		if len(parts) == 2 && parts[1] == agentExecutableName() {
			keep[parts[0]] = true
		}
	}
	// Older manifests did not record a previous binary. Retain the newest
	// other package on that first cleanup so migration keeps a recovery copy.
	if manifest.PreviousBinary == "" {
		previous := ""
		var newest time.Time
		for _, entry := range entries {
			digest, err := hex.DecodeString(entry.Name())
			if !entry.IsDir() || err != nil || len(digest) != sha256.Size || keep[entry.Name()] {
				continue
			}
			info, err := os.Stat(filepath.Join(root, entry.Name(), agentExecutableName()))
			if err == nil && info.Mode().IsRegular() && (previous == "" || info.ModTime().After(newest)) {
				previous, newest = entry.Name(), info.ModTime()
			}
		}
		if previous != "" {
			keep[previous] = true
		}
	}
	var cleanupErr error
	for _, entry := range entries {
		digest, err := hex.DecodeString(entry.Name())
		if !entry.IsDir() || err != nil || len(digest) != sha256.Size || keep[entry.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func agentExecutableName() string {
	if runtime.GOOS == "windows" {
		return "wio-agent.exe"
	}
	return "wio-agent"
}

// ServiceUpdateBinary is called by the Windows service supervisor before each
// worker. An unconfirmed crashed update falls back to the immutable base.
func ServiceUpdateBinary(stateDir, base string) (string, error) {
	root := filepath.Join(stateDir, "updates")
	m, binary, err := readAgentUpdateManifest(root)
	if errors.Is(err, os.ErrNotExist) {
		return base, nil
	}
	if err != nil {
		return base, err
	}
	if !m.AllowRollback && !buildinfo.UpdateAvailable(buildinfo.Version, m.Version) {
		if err := os.Remove(filepath.Join(root, "current.json")); err != nil {
			return base, err
		}
		return base, nil
	}
	if !matchesFileHash(binary, m.SHA256) || m.State == "attempting" {
		previous, err := restorePreviousAgentUpdate(root, m)
		if err != nil {
			return base, err
		}
		if previous != "" {
			return previous, nil
		}
		return base, errors.New("unconfirmed or invalid service update was rolled back")
	}
	if m.State != "healthy" {
		m.State = "attempting"
		if err := writeAgentUpdateManifest(root, m); err != nil {
			return base, err
		}
	}
	return binary, nil
}

func previousAgentManifest(m agentUpdateManifest) (agentUpdateManifest, error) {
	if m.PreviousBinary == "" || m.PreviousVersion == "" || m.PreviousSHA256 == "" {
		return agentUpdateManifest{}, errors.New("no previous Agent package")
	}
	previous := agentUpdateManifest{Version: m.PreviousVersion, Binary: m.PreviousBinary, SHA256: m.PreviousSHA256, State: "pending", AllowRollback: true}
	return previous, nil
}
func validatedPreviousAgent(root string, m agentUpdateManifest) (agentUpdateManifest, string, error) {
	previous, err := previousAgentManifest(m)
	if err != nil {
		return previous, "", err
	}
	relative := filepath.Clean(filepath.FromSlash(previous.Binary))
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return previous, "", errors.New("invalid previous Agent path")
	}
	binary := filepath.Join(root, relative)
	if !matchesFileHash(binary, previous.SHA256) {
		return previous, "", errors.New("previous Agent checksum mismatch")
	}
	return previous, binary, nil
}
func restorePreviousAgentUpdate(root string, m agentUpdateManifest) (string, error) {
	previous, binary, err := validatedPreviousAgent(root, m)
	if err != nil {
		if removeErr := os.Remove(filepath.Join(root, "current.json")); removeErr != nil && !os.IsNotExist(removeErr) {
			return "", removeErr
		}
		return "", nil
	}
	previous.State = "healthy"
	if err := writeAgentUpdateManifest(root, previous); err != nil {
		return "", err
	}
	return binary, nil
}
func stageAgentRollback(state string) (string, error) {
	root := filepath.Join(state, "updates")
	manifest, _, err := readAgentUpdateManifest(root)
	if err != nil {
		return "", errors.New("no Agent update to roll back")
	}
	if manifest.PreviousBinary == "" || manifest.PreviousVersion == "" || manifest.PreviousSHA256 == "" {
		if _, err := os.Stat(hostplatform.Current().Binary); err != nil {
			return "", err
		}
		return "rollback-base", nil
	}
	previous, binary, err := validatedPreviousAgent(root, manifest)
	if err != nil {
		return "", err
	}
	if err := writeAgentUpdateManifest(root, previous); err != nil {
		return "", err
	}
	return binary, nil
}
