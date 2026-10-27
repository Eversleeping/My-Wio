package geminiadapter

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

type pendingQuestion struct {
	ctx                   context.Context
	threadID, operationID string
	detail                json.RawMessage
	answer                chan map[string]protocol.UserInputAnswer
	claimed               bool
}

func (a *Adapter) ask(ctx context.Context, op, thread string, questions []protocol.UserInputQuestion) (map[string]protocol.UserInputAnswer, error) {
	if len(questions) == 0 || len(questions) > 4 {
		return nil, errors.New("ask 1 to 4 questions")
	}
	ids := map[string]bool{}
	for _, q := range questions {
		if strings.TrimSpace(q.ID) == "" || len(q.ID) > 100 || ids[q.ID] || strings.TrimSpace(q.Question) == "" || len(q.Question) > 8192 || len(q.Header) > 200 || len(q.Options) > 12 || q.IsSecret {
			return nil, errors.New("invalid question; do not request credentials or secrets")
		}
		ids[q.ID] = true
		labels := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" || len(o.Label) > 1000 || len(o.Description) > 4000 || labels[o.Label] {
				return nil, errors.New("invalid question option")
			}
			labels[o.Label] = true
		}
	}
	id := uuid.NewString()
	// Avoid a false isSecret flag being redacted into a truthy string by the
	// gateway's credential filter. Secret questions are rejected above.
	wireQuestions := make([]map[string]any, len(questions))
	for i, q := range questions {
		wireQuestions[i] = map[string]any{"id": q.ID, "header": q.Header, "question": q.Question, "isOther": q.IsOther, "options": q.Options}
	}
	detail, _ := json.Marshal(map[string]any{"operation_id": op, "questions": wireQuestions})
	p := &pendingQuestion{ctx: ctx, threadID: thread, operationID: op, detail: detail, answer: make(chan map[string]protocol.UserInputAnswer, 1)}
	a.mu.Lock()
	if a.questions == nil {
		a.questions = map[string]*pendingQuestion{}
	}
	a.questions[id] = p
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.questions, id); a.mu.Unlock() }()
	if err := a.emit(protocol.StreamEvent{EventID: id, StreamID: thread, Kind: "gemini.question", Payload: detail}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case answer := <-p.answer:
		return answer, nil
	}
}

// Answer resolves only a live question from the same task. The answer event is
// queued before releasing the model so a resumed response cannot overtake it.
func (a *Adapter) Answer(command protocol.GeminiAnswerCommand) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.questions[command.RequestID]
	if p == nil || p.ctx.Err() != nil || p.claimed || p.threadID != command.ThreadID || p.operationID != command.OperationID {
		return errors.New("Gemini question has expired or was already answered")
	}
	if err := protocol.ValidateUserInputAnswers(p.detail, command.Answers); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"operation_id": command.OperationID, "request_id": command.RequestID, "answers": command.Answers})
	if err := a.emit(protocol.StreamEvent{EventID: command.RequestID + ":answered", StreamID: command.ThreadID, Kind: "gemini.question.answered", Payload: raw}); err != nil {
		return err
	}
	p.claimed = true
	p.answer <- command.Answers
	return nil
}

// The MCP bridge lives only for this turn and binds only to loopback. A random
// bearer credential keeps unrelated local web pages from invoking its tools.
func (a *Adapter) questionServer(ctx context.Context, op, thread string) (map[string]any, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	token := uuid.NewString() + uuid.NewString()
	name := "wio_questions_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&request) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": name, "version": "1"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			// This tool has no filesystem or command capability; it only asks questions.
			schema := json.RawMessage(`{"type":"object","properties":{"questions":{"type":"array","minItems":1,"maxItems":4,"items":{"type":"object","properties":{"id":{"type":"string"},"header":{"type":"string"},"question":{"type":"string"},"isOther":{"type":"boolean"},"options":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"},"description":{"type":"string"}},"required":["label","description"]}}},"required":["id","header","question"]}}},"required":["questions"]}`)
			// Native ask_user is excluded in ACP, including MCP tools with that
			// exact name. Keep a distinct name so the CLI exposes this bridge.
			result = map[string]any{"tools": []any{map[string]any{"name": "wio_ask_user", "description": "Ask the user interactive questions in the web UI and wait for their answers before continuing. Use this tool whenever clarification or a choice is needed. Provide 1-4 questions with stable IDs; options are single choice, isOther allows custom text. Omit options for a text answer. Never ask for passwords or credentials.", "inputSchema": schema, "annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}}}}
		case "tools/call":
			var params struct {
				Name      string `json:"name"`
				Arguments struct {
					Questions []protocol.UserInputQuestion `json:"questions"`
				} `json:"arguments"`
			}
			var answers map[string]protocol.UserInputAnswer
			err := json.Unmarshal(request.Params, &params)
			if err == nil && params.Name != "wio_ask_user" {
				err = errors.New("unknown tool")
			}
			if err == nil {
				answers, err = a.ask(ctx, op, thread, params.Arguments.Questions)
			}
			value := ""
			if err != nil {
				value = err.Error()
			} else {
				raw, _ := json.Marshal(map[string]any{"answers": answers})
				value = string(raw)
			}
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": value}}, "isError": err != nil}
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = server.Serve(listener) }()
	return map[string]any{"name": name, "type": "http", "url": "http://" + listener.Addr().String() + "/mcp", "headers": []any{map[string]string{"name": "Authorization", "value": "Bearer " + token}}}, func() { _ = server.Close() }, nil
}
