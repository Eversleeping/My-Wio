package geminiadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

// Fork copies Gemini's recording verbatim, including native patch records. The
// official loader reconstructs it on resume; no model call or tools are run.
func (a *Adapter) Fork(ctx context.Context, opID string, command protocol.GeminiForkCommand) (protocol.GeminiForkResult, error) {
	a.mu.Lock()
	if a.updating {
		a.mu.Unlock()
		return protocol.GeminiForkResult{}, errors.New("Gemini configuration is changing")
	}
	environment := mergedEnvironment(a.environment)
	forkCtx, cancel := context.WithCancel(ctx)
	a.active[opID] = cancel
	a.mu.Unlock()
	defer func() { cancel(); a.mu.Lock(); delete(a.active, opID); a.mu.Unlock() }()
	path, chats, err := prepareNativeHistory(protocol.GeminiTurnCommand{SessionID: command.SessionID, Workspace: command.SourceWorkspace, Rewrite: command.Before}, environment)
	if err != nil {
		return protocol.GeminiForkResult{}, err
	}
	defer os.Remove(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return protocol.GeminiForkResult{}, err
	}
	header, body, _ := bytes.Cut(raw, []byte("\n"))
	var metadata map[string]any
	if err = json.Unmarshal(header, &metadata); err != nil {
		return protocol.GeminiForkResult{}, err
	}
	id := uuid.NewString()
	hash := sha256.Sum256([]byte(command.Workspace))
	metadata["sessionId"] = id
	metadata["projectHash"] = hex.EncodeToString(hash[:])
	now := time.Now().UTC().Format(time.RFC3339Nano)
	metadata["startTime"] = now
	metadata["lastUpdated"] = now
	header, err = json.Marshal(metadata)
	if err != nil {
		return protocol.GeminiForkResult{}, err
	}
	if command.Workspace != command.SourceWorkspace {
		// Fresh worktrees have no Gemini registry entry. The official CLI migrates
		// its legacy hash directory into the registered slug on first resume.
		chats = filepath.Join(filepath.Dir(filepath.Dir(chats)), hex.EncodeToString(hash[:]), "chats")
	}
	// Refuse symlink ancestors before writing private conversation data.
	for p := chats; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return protocol.GeminiForkResult{}, errors.New("unsafe Gemini history directory")
		}
		if e != nil && !os.IsNotExist(e) {
			return protocol.GeminiForkResult{}, e
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if err = os.MkdirAll(chats, 0700); err != nil {
		return protocol.GeminiForkResult{}, err
	}
	dest := filepath.Join(chats, "session-"+time.Now().UTC().Format("2006-01-02T15-04-05.000")+"-"+id[:8]+".jsonl")
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return protocol.GeminiForkResult{}, err
	}
	defer f.Close()
	data := append(append(header, '\n'), body...)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = forkCtx.Err()
	}
	if err != nil {
		_ = os.Remove(dest)
		return protocol.GeminiForkResult{}, err
	}
	return protocol.GeminiForkResult{SessionID: id}, nil
}
