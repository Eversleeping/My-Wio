package claudeadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// PrepareRewrite keeps the source transcript untouched. The native loader forks
// the immutable snapshot at the assistant message preceding the edited input.
func PrepareRewrite(ctx context.Context, stateDir, operationID string, c *protocol.ClaudeTurnCommand) error {
	if c.Rewrite == nil {
		return nil
	}
	if c.SessionID == "" {
		return errors.New("Claude message has no native session to edit")
	}
	_, err := Snapshot(ctx, stateDir, protocol.ClaudeForkCommand{SessionID: c.SessionID, TargetThreadID: operationID})
	if err != nil {
		return err
	}
	path, err := SnapshotPath(stateDir, operationID)
	if err != nil {
		return err
	}
	parent, err := rewriteBoundary(ctx, path, *c.Rewrite)
	if err != nil {
		return err
	}
	c.ForkSessionID, c.SessionID = c.SessionID, ""
	c.ForkSnapshotID, c.ResumePath, c.ResumeAt = "", "", ""
	if parent != "" {
		c.ResumePath, c.ResumeAt = path, parent
	}
	return nil
}

type nativeRewriteMessage struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid"`
	Parent    string `json:"parentUuid"`
	Timestamp string `json:"timestamp"`
	Sidechain bool   `json:"isSidechain"`
	Message   struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
	} `json:"message"`
}

func (m nativeRewriteMessage) userPrompt() (string, bool) {
	if m.Type != "user" || m.Sidechain || m.Message.Role != "user" {
		return "", false
	}
	var text string
	if json.Unmarshal(m.Message.Content, &text) == nil {
		return text, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Message.Content, &blocks) != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
		default:
			return "", false
		}
	}
	return strings.Join(parts, "\n"), true
}

func rewriteBoundary(ctx context.Context, path string, target protocol.ClaudeRewriteTarget) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 65536), 8<<20)
	byID := map[string]nativeRewriteMessage{}
	var candidates []nativeRewriteMessage
	start, startErr := time.Parse(time.RFC3339Nano, target.StartedAt)
	before, beforeErr := time.Parse(time.RFC3339Nano, target.Before)
	for scan.Scan() {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var m nativeRewriteMessage
		if json.Unmarshal(scan.Bytes(), &m) != nil {
			return "", errors.New("invalid Claude transcript; original history was preserved")
		}
		if m.UUID == "" || m.Sidechain {
			continue
		}
		byID[m.UUID] = m
		text, isPrompt := m.userPrompt()
		at, timeErr := time.Parse(time.RFC3339Nano, m.Timestamp)
		if isPrompt && text == target.Prompt && startErr == nil && timeErr == nil && !at.Before(start) && (target.Before == "" || beforeErr == nil && at.Before(before)) {
			candidates = append(candidates, m)
		}
	}
	if err := scan.Err(); err != nil {
		return "", err
	}
	m, ok := byID[target.MessageID]
	if !ok {
		// Older agents generated random native UUIDs. Only an unambiguous exact
		// prompt in the saved turn interval may be used as a migration fallback.
		if len(candidates) != 1 {
			return "", errors.New("could not uniquely locate the Claude message; original history was preserved")
		}
		m = candidates[0]
	}
	if _, ok := m.userPrompt(); !ok {
		return "", errors.New("Claude edit target is not a user prompt")
	}
	if m.Parent == "" {
		return "", nil
	}
	parent, ok := byID[m.Parent]
	if !ok || parent.Type != "assistant" || parent.Sidechain || parent.Message.StopReason == "tool_use" {
		return "", errors.New("Claude message has no safe assistant resume boundary; original history was preserved")
	}
	// Reject a broken/cyclic prefix rather than resuming at an unrelated UUID.
	seen := map[string]bool{m.UUID: true}
	for p := parent; p.UUID != ""; {
		if seen[p.UUID] {
			return "", errors.New("invalid Claude parent chain")
		}
		seen[p.UUID] = true
		if p.Parent == "" {
			break
		}
		var found bool
		p, found = byID[p.Parent]
		if !found {
			return "", errors.New("Claude prefix is incomplete; original history was preserved")
		}
	}
	return parent.UUID, nil
}
