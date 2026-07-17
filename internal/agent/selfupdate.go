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

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
)

const maxAgentUpdateSize = 256 << 20

type agentUpdateManifest struct {
	Version string `json:"version"`
	Binary  string `json:"binary"`
	SHA256  string `json:"sha256"`
	State   string `json:"state"`
}

func (c *Client) stageAgentUpdate(ctx context.Context, command protocol.AgentUpdateCommand) (string, error) {
	if strings.TrimSpace(command.Version) == "" {
		return "", errors.New("Agent update version is required")
	}
	if !buildinfo.UpdateAvailable(buildinfo.Version, command.Version) {
		return "", fmt.Errorf("Agent update version %s is not newer than %s", command.Version, buildinfo.Version)
	}
	pkg, ok := command.Packages[runtime.GOARCH]
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
	targetPath := filepath.Join(targetDir, "wio-agent")
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
	manifest := agentUpdateManifest{
		Version: command.Version,
		Binary:  filepath.ToSlash(filepath.Join(strings.ToLower(pkg.SHA256), "wio-agent")),
		SHA256:  strings.ToLower(pkg.SHA256),
		State:   "pending",
	}
	if err := writeAgentUpdateManifest(updatesRoot, manifest); err != nil {
		return "", err
	}
	return targetPath, nil
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
		_ = os.Remove(filepath.Join(updatesRoot, "current.json"))
		return errors.New("current Agent update checksum is invalid")
	}
	if matchesCurrentExecutable(binary) {
		return nil
	}
	if !buildinfo.UpdateAvailable(buildinfo.Version, manifest.Version) {
		_ = os.Remove(filepath.Join(updatesRoot, "current.json"))
		return nil
	}
	if manifest.State == "attempting" {
		_ = os.Remove(filepath.Join(updatesRoot, "current.json"))
		return fmt.Errorf("Agent update %s did not start successfully and was rolled back", manifest.Version)
	}
	manifest.State = "attempting"
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

func activateStagedUpdate(stateDir, expectedPath string) error {
	updatesRoot := filepath.Join(stateDir, "updates")
	manifest, binary, err := readAgentUpdateManifest(updatesRoot)
	if err != nil {
		return err
	}
	if !samePath(binary, expectedPath) || !matchesFileHash(binary, manifest.SHA256) {
		return errors.New("staged Agent update changed before activation")
	}
	manifest.State = "attempting"
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
	if !matchesCurrentExecutable(binary) || manifest.State == "healthy" {
		return false, nil
	}
	manifest.State = "healthy"
	if err := writeAgentUpdateManifest(updatesRoot, manifest); err != nil {
		return false, err
	}
	return true, nil
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

func replaceFile(source, target string) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
}
