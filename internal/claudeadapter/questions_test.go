package claudeadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

type questionWriter struct {
	bytes.Buffer
	closed bool
}

func (w *questionWriter) Close() error { w.closed = true; return nil }

func TestClaudeQuestionNativeResponseAndIsolation(t *testing.T) {
	w := &questionWriter{}
	var events []string
	live := &liveTurn{threadID: "thread", operationID: "op", stdin: w, cancel: func() {}, inputs: map[string]*liveInput{}, emit: func(kind string, _ map[string]any) error { events = append(events, kind); return nil }}
	sessions := Sessions{turns: map[string]*liveTurn{"op": live}}
	raw := []byte(`{"request_id":"req","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"metadata":{"source":"native"},"questions":[{"header":"Choice","question":"Pick items","multiSelect":true,"options":[{"label":"A","description":"First"},{"label":"B","description":"Second"}]}]}}}`)
	if err := live.questionRequest(raw); err != nil {
		t.Fatal(err)
	}
	if err := live.questionRequest(raw); err != nil || len(events) != 1 {
		t.Fatal("duplicate question", err, events)
	}
	if err := live.result(0); err != nil || w.closed {
		t.Fatal("closed input while waiting", err)
	}
	c := protocol.ClaudeAnswerCommand{ThreadID: "other", OperationID: "op", RequestID: "op:req", Answers: map[string]protocol.UserInputAnswer{"0": {Answers: []string{"A", "B", "custom"}}}}
	if err := sessions.Answer(context.Background(), c); err == nil {
		t.Fatal("accepted cross-thread answer")
	}
	c.ThreadID = "thread"
	if err := sessions.Answer(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	size := w.Len()
	if err := sessions.Answer(context.Background(), c); err != nil || w.Len() != size {
		t.Fatal("duplicate write", err)
	}
	var response struct {
		Type     string
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior     string
				UpdatedInput struct {
					Metadata map[string]string
					Answers  map[string][]string
				} `json:"updatedInput"`
			}
		}
	}
	if err := json.Unmarshal(w.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "control_response" || response.Response.RequestID != "req" || response.Response.Response.Behavior != "allow" || len(response.Response.Response.UpdatedInput.Answers["Pick items"]) != 3 || response.Response.Response.UpdatedInput.Metadata["source"] != "native" {
		t.Fatal(w.String())
	}
	c.Answers = map[string]protocol.UserInputAnswer{"0": {Answers: []string{"B"}}}
	if err := sessions.Answer(context.Background(), c); err == nil {
		t.Fatal("replaced an existing answer")
	}
	if len(events) != 2 || events[1] != "question.answered" {
		t.Fatal(events)
	}
}

func TestClaudeQuestionCancellationAndPermissionDenial(t *testing.T) {
	w := &questionWriter{}
	live := &liveTurn{threadID: "thread", operationID: "op", stdin: w, cancel: func() {}, emit: func(string, map[string]any) error { return nil }}
	if err := live.questionRequest([]byte(`{"request_id":"permission","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"dangerous"}}}`)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(w.Bytes(), []byte(`"behavior":"deny"`)) || bytes.Contains(w.Bytes(), []byte(`"updatedPermissions"`)) {
		t.Fatal(w.String())
	}
	w.Reset()
	if err := live.questionRequest([]byte(`{"request_id":"req","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Continue?","header":"Choice"}]}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := live.cancelQuestion("req"); err != nil {
		t.Fatal(err)
	}
	sessions := Sessions{turns: map[string]*liveTurn{"op": live}}
	if err := sessions.Answer(context.Background(), protocol.ClaudeAnswerCommand{ThreadID: "thread", OperationID: "op", RequestID: "op:req", Answers: map[string]protocol.UserInputAnswer{"0": {Answers: []string{"yes"}}}}); err == nil {
		t.Fatal("accepted cancelled question")
	}
	if w.Len() != 0 {
		t.Fatal("wrote cancelled answer")
	}
}
