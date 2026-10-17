package codexadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type inputTestWriter struct{ bytes.Buffer }

func (*inputTestWriter) Close() error { return nil }

func TestUserInputRoundTripAndResolution(t *testing.T) {
	var events []protocol.StreamEvent
	a := New("", slog.New(slog.NewTextHandler(io.Discard, nil)), func(e protocol.StreamEvent) error { events = append(events, e); return nil })
	writer := &inputTestWriter{}
	a.stdin = writer
	a.threads["native"] = "wio"
	params := json.RawMessage(`{"threadId":"native","turnId":"turn","questions":[{"id":"choice","question":"Choose","options":[{"label":"A"},{"label":"B"}]},{"id":"text","question":"Describe","isOther":true}]}`)
	a.handleServerRequest(rpcMessage{ID: json.RawMessage(`7`), Method: protocol.UserInputRequestMethod, Params: params})
	if len(events) != 1 || events[0].StreamID != "wio" || writer.Len() != 0 {
		t.Fatalf("request not retained: %+v %s", events, writer.String())
	}
	command := protocol.ApprovalDecisionCommand{ThreadID: "wio", RequestID: "7", Decision: "answered", Answers: map[string]protocol.UserInputAnswer{"choice": {Answers: []string{"A"}}, "text": {Answers: []string{"custom"}}}}
	if err := a.Decide(command); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		ID     int `json:"id"`
		Result struct {
			Answers map[string]protocol.UserInputAnswer `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal(writer.Bytes(), &reply); err != nil || reply.ID != 7 || reply.Result.Answers["text"].Answers[0] != "custom" {
		t.Fatalf("wrong response: %s %v", writer.String(), err)
	}
	if err := a.Decide(command); err == nil {
		t.Fatal("duplicate answer accepted")
	}
	a.handleServerRequest(rpcMessage{ID: json.RawMessage(`8`), Method: protocol.UserInputRequestMethod, Params: params})
	a.handleNotification(rpcMessage{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"native","requestId":8}`)})
	command.RequestID = "8"
	if err := a.Decide(command); err == nil {
		t.Fatal("resolved request accepted")
	}
}

func TestSessionsAskAndAnswerInDefaultMode(t *testing.T) {
	for _, codexThread := range []string{"", "existing-thread"} {
		name := "new"
		if codexThread != "" {
			name = "resumed"
		}
		t.Run(name, func(t *testing.T) {
			pool, events := sessionTestPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := pool.StartTurn(ctx, protocol.StartTurnCommand{ThreadID: "question-thread", CodexThread: codexThread, Workspace: t.TempDir(), Prompt: "ask-user"}); err != nil {
				t.Fatal(err)
			}
			question := waitSessionEvent(t, events, "question-thread", "approval.requested")
			var request struct {
				Kind   string `json:"kind"`
				Detail struct {
					Questions []protocol.UserInputQuestion `json:"questions"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(question.Payload, &request); err != nil || request.Kind != protocol.UserInputRequestMethod || len(request.Detail.Questions) != 2 {
				t.Fatalf("interactive question missing: %s (%v)", question.Payload, err)
			}
			if err := pool.Decide(protocol.ApprovalDecisionCommand{
				ThreadID: "question-thread", RequestID: "43", Decision: "answered",
				Answers: map[string]protocol.UserInputAnswer{"choice": {Answers: []string{"B"}}, "details": {Answers: []string{"custom answer"}}},
			}); err != nil {
				t.Fatal(err)
			}
			answer := waitSessionEvent(t, events, "question-thread", "codex.item.completed")
			var reply struct {
				Item struct {
					Text string `json:"text"`
				} `json:"item"`
			}
			if err := json.Unmarshal(answer.Payload, &reply); err != nil || reply.Item.Text != "B: custom answer" {
				t.Fatalf("answers did not reach Codex: %s", answer.Payload)
			}
			waitSessionEvent(t, events, "question-thread", "codex.turn.completed")
		})
	}
}
