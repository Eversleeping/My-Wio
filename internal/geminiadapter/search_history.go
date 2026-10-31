package geminiadapter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Gemini CLI 0.66 sends [] over ACP for structured grep results, even though
// its native JSONL contains the tool's intended display result. Recover only a
// matching call from this turn's own session; missing or unfamiliar history
// remains an empty display result rather than being guessed or re-executed.
type nativeSearchHistory struct {
	root, session, project, path string
	baseline                     int64
	allowDiscovery, wait         bool
}

func newNativeSearchHistory(session, workspace string, environment []string, resuming bool) *nativeSearchHistory {
	root, err := nativeHistoryRoot(environment)
	if err != nil || session == "" {
		return nil
	}
	hash := sha256.Sum256([]byte(workspace))
	h := &nativeSearchHistory{root: root, session: session, project: hex.EncodeToString(hash[:]), allowDiscovery: !resuming, wait: true}
	if path, ok := h.findSession(); ok {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			h.path, h.baseline = path, info.Size()
			return h
		}
	}
	if resuming {
		return nil
	}
	return h
}

func (h *nativeSearchHistory) findSession() (string, bool) {
	projects, err := os.ReadDir(h.root)
	if err != nil {
		return "", false
	}
	found := ""
	for _, project := range projects {
		if !project.IsDir() || project.Type()&os.ModeSymlink != 0 {
			continue
		}
		chats := filepath.Join(h.root, project.Name(), "chats")
		if info, err := os.Lstat(chats); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		files, err := os.ReadDir(chats)
		if err != nil {
			continue
		}
		for _, file := range files {
			if !strings.HasPrefix(file.Name(), "session-") || filepath.Ext(file.Name()) != ".jsonl" || file.Type()&os.ModeSymlink != 0 || file.IsDir() {
				continue
			}
			path := filepath.Join(chats, file.Name())
			if !h.matchesHeader(path) {
				continue
			}
			if found != "" {
				return "", false // Ambiguous history is never used as a fallback.
			}
			found = path
		}
	}
	return found, found != ""
}

func (h *nativeSearchHistory) matchesHeader(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(io.LimitReader(f, 64<<10))
	if !scanner.Scan() {
		return false
	}
	var header struct {
		Session string `json:"sessionId"`
		Project string `json:"projectHash"`
	}
	return json.Unmarshal(scanner.Bytes(), &header) == nil && header.Session == h.session && header.Project == h.project
}

func (h *nativeSearchHistory) output(ctx context.Context, id string) (string, bool) {
	if h == nil || !strings.HasPrefix(id, "grep_search__") {
		return "", false
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		if h.path == "" && h.allowDiscovery {
			h.path, _ = h.findSession()
		}
		if h.path != "" {
			if output, ok := h.readResult(id); ok {
				return output, true
			}
		}
		if !h.wait || time.Now().After(deadline) {
			h.wait = false // Avoid adding repeated delays when recording is disabled.
			return "", false
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", false
		case <-timer.C:
		}
	}
}

func (h *nativeSearchHistory) readResult(id string) (string, bool) {
	// Read only the recent, complete records and never consume unbounded history.
	const maxTail = 4 << 20
	info, err := os.Lstat(h.path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < h.baseline || !h.matchesHeader(h.path) {
		return "", false
	}
	f, err := os.Open(h.path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	start := h.baseline
	if info.Size()-start > maxTail {
		start = info.Size() - maxTail
	}
	partial := false
	if start > 0 {
		var prev [1]byte
		if _, err := f.ReadAt(prev[:], start-1); err != nil {
			return "", false
		}
		partial = prev[0] != '\n'
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxTail))
	if err != nil {
		return "", false
	}
	if partial {
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			raw = raw[i+1:]
		} else {
			return "", false
		}
	}
	if i := bytes.LastIndexByte(raw, '\n'); i >= 0 {
		raw = raw[:i]
	} else {
		return "", false
	}
	type call struct {
		ID, Name      string
		ResultDisplay *struct {
			Summary string `json:"summary"`
			Matches []struct {
				FilePath   string `json:"filePath"`
				LineNumber int    `json:"lineNumber"`
				Line       string `json:"line"`
			} `json:"matches"`
		} `json:"resultDisplay"`
		Result []struct {
			FunctionResponse struct {
				ID string `json:"id"`
			} `json:"functionResponse"`
		} `json:"result"`
	}
	var found string
	ok := false
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(id)) || !bytes.Contains(line, []byte(`"toolCalls"`)) {
			continue
		}
		var record struct {
			Type  string `json:"type"`
			Calls []call `json:"toolCalls"`
			Patch struct {
				Calls []call `json:"toolCalls"`
			} `json:"$patch"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		calls := record.Patch.Calls
		if record.Type == "gemini" {
			calls = append(calls, record.Calls...)
		}
		for _, candidate := range calls {
			if candidate.ID != id || candidate.Name != "grep_search" || candidate.ResultDisplay == nil || candidate.ResultDisplay.Summary == "" {
				continue
			}
			for _, result := range candidate.Result {
				response := result.FunctionResponse
				if response.ID == id {
					var output strings.Builder
					output.WriteString(candidate.ResultDisplay.Summary)
					for _, match := range candidate.ResultDisplay.Matches {
						fmt.Fprintf(&output, "\n%s:%d: %s", match.FilePath, match.LineNumber, match.Line)
						if output.Len() > 1<<20 {
							break
						}
					}
					if output.Len() <= 1<<20 {
						found, ok = output.String(), true
					}
				}
			}
		}
	}
	return found, ok
}
