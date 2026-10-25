package geminiadapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

const maxRewriteSessionBytes = 64 * 1024 * 1024

// prepareRewrite copies the original file's prefix, without modifying it. Let
// the official --session-file loader interpret JSONL patches, rewinds and chat
// compression, rather than reimplementing Gemini's evolving history loader.
func prepareRewrite(command protocol.GeminiTurnCommand, environment []string) (string, error) {
	if command.Rewrite == nil || command.SessionID == "" {
		return "", errors.New("Gemini rewrite requires saved native history")
	}
	started, err := time.Parse(time.RFC3339Nano, command.Rewrite.StartedAt)
	if err != nil {
		return "", errors.New("invalid Gemini rewrite boundary")
	}
	var before time.Time
	if command.Rewrite.Before != "" {
		before, err = time.Parse(time.RFC3339Nano, command.Rewrite.Before)
		if err != nil || !before.After(started) {
			return "", errors.New("invalid Gemini rewrite end boundary")
		}
	}
	home, _ := os.UserHomeDir()
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "HOME="); ok {
			home = value
		}
	}
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "GEMINI_CLI_HOME="); ok && value != "" {
			home = value
		}
	}
	if home == "" {
		return "", errors.New("Gemini home directory unavailable")
	}
	runtime := filepath.Join(home, ".gemini")
	for _, entry := range environment {
		if entry == "SANDBOX=sandbox-exec" {
			runtime = filepath.Join(home, ".cache", ".gemini")
		}
	}
	root := filepath.Join(runtime, "tmp")
	projects, err := os.ReadDir(root)
	if err != nil {
		return "", errors.New("Gemini native session history unavailable")
	}
	hash := sha256.Sum256([]byte(command.Workspace))
	projectHash := hex.EncodeToString(hash[:])
	var prefix []byte
	found := false
	for _, project := range projects {
		if !project.IsDir() || project.Type()&os.ModeSymlink != 0 {
			continue
		}
		chats := filepath.Join(root, project.Name(), "chats")
		if info, err := os.Lstat(chats); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		files, err := os.ReadDir(chats)
		if err != nil {
			continue
		}
		for _, file := range files {
			if !strings.HasPrefix(file.Name(), "session-") || (filepath.Ext(file.Name()) != ".jsonl" && filepath.Ext(file.Name()) != ".json") || file.Type()&os.ModeSymlink != 0 || file.IsDir() {
				continue
			}
			path := filepath.Join(chats, file.Name())
			info, err := file.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > maxRewriteSessionBytes {
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			raw, err := io.ReadAll(io.LimitReader(f, maxRewriteSessionBytes+1))
			f.Close()
			if err != nil || len(raw) > maxRewriteSessionBytes {
				continue
			}
			data, matched, err := rewritePrefix(raw, command.SessionID, projectHash, started, before)
			if !matched {
				continue
			}
			if err != nil {
				return "", err
			}
			if found {
				return "", errors.New("ambiguous Gemini native session history")
			}
			prefix = data
			found = true
		}
	}
	if !found {
		return "", errors.New("saved Gemini session or message not found; original conversation was kept")
	}
	file, err := os.CreateTemp("", "wio-gemini-rewrite-*.jsonl")
	if err != nil {
		return "", errors.New("could not prepare Gemini conversation prefix")
	}
	name := file.Name()
	if _, err = file.Write(prefix); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", errors.New("could not save Gemini conversation prefix")
	}
	return name, nil
}

type nativeMessage struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
}

func matchesRewriteBoundary(raw json.RawMessage, started, before time.Time) (bool, error) {
	var record nativeMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		return false, err
	}
	if record.Type != "user" {
		return false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return false, errors.New("invalid timestamp in Gemini native history")
	}
	return !at.Before(started) && (before.IsZero() || at.Before(before)), nil
}

func rewritePrefix(raw []byte, sessionID, projectHash string, started, before time.Time) ([]byte, bool, error) {
	var document struct {
		SessionID   string            `json:"sessionId"`
		ProjectHash string            `json:"projectHash"`
		Messages    []json.RawMessage `json:"messages"`
		StartTime   string            `json:"startTime"`
		LastUpdated string            `json:"lastUpdated"`
	}
	trimmed := bytes.TrimSpace(raw)
	// Legacy JSON sessions are converted to the official JSONL import format.
	if json.Unmarshal(trimmed, &document) == nil && document.Messages != nil {
		if document.SessionID != sessionID || document.ProjectHash != projectHash {
			return nil, false, nil
		}
		metadata, _ := json.Marshal(map[string]string{"sessionId": sessionID, "projectHash": projectHash, "startTime": document.StartTime, "lastUpdated": document.LastUpdated})
		out := append(metadata, '\n')
		for _, message := range document.Messages {
			match, err := matchesRewriteBoundary(message, started, before)
			if err != nil {
				return nil, true, err
			}
			if match {
				return out, true, nil
			}
			out = append(out, message...)
			out = append(out, '\n')
		}
		return nil, true, errors.New("Gemini message is absent from native history; original conversation was kept")
	}
	lines := bytes.SplitAfter(raw, []byte("\n"))
	if len(lines) == 0 || json.Unmarshal(bytes.TrimSpace(lines[0]), &document) != nil || document.SessionID != sessionID || document.ProjectHash != projectHash {
		return nil, false, nil
	}
	offset := 0
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			offset += len(line)
			continue
		}
		match, err := matchesRewriteBoundary(bytes.TrimSpace(line), started, before)
		if err != nil {
			return nil, true, errors.New("could not read Gemini native history; original conversation was kept")
		}
		if match {
			return append([]byte(nil), raw[:offset]...), true, nil
		}
		offset += len(line)
	}
	return nil, true, errors.New("Gemini message is absent from native history; original conversation was kept")
}
