package claudeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type liveQuestion struct {
	nativeID  string
	input     map[string]json.RawMessage
	detail    json.RawMessage
	questions []protocol.UserInputQuestion
	answers   map[string]protocol.UserInputAnswer
	cancelled bool
}

// Only AskUserQuestion is allowed by this bridge. Other permission requests
// retain the previous headless denial behavior; no permission rules are saved.
func (live *liveTurn) questionRequest(raw []byte) error {
	var frame struct {
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string                     `json:"subtype"`
			Tool    string                     `json:"tool_name"`
			Input   map[string]json.RawMessage `json:"input"`
		} `json:"request"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.RequestID == "" || len(frame.RequestID) > 256 {
		return errors.New("invalid Claude control request")
	}
	if frame.Request.Subtype != "can_use_tool" || frame.Request.Tool != "AskUserQuestion" {
		return live.controlResponse(frame.RequestID, map[string]any{"behavior": "deny", "message": "This tool requires approval that is not available in Wio. Use the configured permission mode or another allowed tool."})
	}
	var native []struct {
		Question    string `json:"question"`
		Header      string `json:"header"`
		MultiSelect bool   `json:"multiSelect"`
		Options     []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	}
	if json.Unmarshal(frame.Request.Input["questions"], &native) != nil || len(native) < 1 || len(native) > 4 {
		return live.controlResponse(frame.RequestID, map[string]any{"behavior": "deny", "message": "Invalid questions"})
	}
	questions := make([]protocol.UserInputQuestion, len(native))
	texts := map[string]bool{}
	for i, q := range native {
		if strings.TrimSpace(q.Question) == "" || len(q.Question) > 8192 || len(q.Header) > 200 || len(q.Options) > 12 || texts[q.Question] {
			return live.controlResponse(frame.RequestID, map[string]any{"behavior": "deny", "message": "Invalid questions"})
		}
		texts[q.Question] = true
		labels := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" || len(o.Label) > 1000 || len(o.Description) > 4000 || labels[o.Label] {
				return live.controlResponse(frame.RequestID, map[string]any{"behavior": "deny", "message": "Invalid options"})
			}
			labels[o.Label] = true
		}
		questions[i] = protocol.UserInputQuestion{ID: strconv.Itoa(i), Header: q.Header, Question: q.Question, MultiSelect: q.MultiSelect, IsOther: true, Options: q.Options}
	}
	id := live.operationID + ":" + frame.RequestID
	// Omit isSecret: credential redaction must not turn a boolean into text.
	wire := make([]map[string]any, len(questions))
	for i, q := range questions {
		wire[i] = map[string]any{"id": q.ID, "header": q.Header, "question": q.Question, "options": q.Options, "isOther": true, "multiSelect": q.MultiSelect}
	}
	detail, _ := json.Marshal(map[string]any{"questions": wire})
	live.mu.Lock()
	if live.questions == nil {
		live.questions = map[string]*liveQuestion{}
	}
	if live.questions[id] != nil {
		live.mu.Unlock()
		return nil
	}
	live.questions[id] = &liveQuestion{nativeID: frame.RequestID, input: frame.Request.Input, detail: detail, questions: questions}
	live.lastResult = false
	if live.idle != nil {
		live.idle.Stop()
		live.idle = nil
	}
	live.mu.Unlock()
	return live.emit("question", map[string]any{"request_id": id, "questions": wire})
}

func (live *liveTurn) controlResponse(id string, response map[string]any) error {
	live.writeMu.Lock()
	defer live.writeMu.Unlock()
	return live.writeControlResponse(id, response)
}

func (live *liveTurn) writeControlResponse(id string, response map[string]any) error {
	raw, _ := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": response}})
	watchdog := time.AfterFunc(20*time.Second, live.cancel)
	defer watchdog.Stop()
	_, err := live.stdin.Write(append(raw, '\n'))
	if err != nil {
		return errors.New("could not deliver Claude answer; the task may have ended")
	}
	return nil
}

func (s *Sessions) Answer(ctx context.Context, command protocol.ClaudeAnswerCommand) error {
	s.mu.Lock()
	live := s.turns[command.OperationID]
	s.mu.Unlock()
	if live == nil || live.threadID != command.ThreadID {
		return errors.New("Claude question has expired")
	}
	live.writeMu.Lock()
	defer live.writeMu.Unlock()
	live.mu.Lock()
	q := live.questions[command.RequestID]
	if ctx.Err() != nil || live.closed || q == nil || q.cancelled {
		live.mu.Unlock()
		return errors.New("Claude question has expired")
	}
	if q.answers != nil {
		same := reflect.DeepEqual(q.answers, command.Answers)
		live.mu.Unlock()
		if same {
			return nil
		}
		return errors.New("Claude question already has a different answer")
	}
	if err := protocol.ValidateUserInputAnswers(q.detail, command.Answers); err != nil {
		live.mu.Unlock()
		return err
	}
	q.answers = command.Answers
	live.mu.Unlock()
	input := map[string]any{}
	for key, value := range q.input {
		input[key] = value
	}
	answers := map[string]any{}
	for _, question := range q.questions {
		a := command.Answers[question.ID].Answers
		if question.MultiSelect {
			answers[question.Question] = a
		} else {
			answers[question.Question] = a[0]
		}
	}
	input["answers"] = answers
	// Persist the answer before releasing the CLI so resumed output and task
	// completion cannot overtake the receipt in the Agent outbox.
	if err := live.emit("question.answered", map[string]any{"request_id": command.RequestID, "answers": command.Answers}); err != nil {
		live.cancel()
		return err
	}
	if err := live.writeControlResponse(q.nativeID, map[string]any{"behavior": "allow", "updatedInput": input}); err != nil {
		_ = live.cancelQuestion(q.nativeID)
		live.cancel()
		return err
	}
	return nil
}

func (live *liveTurn) cancelQuestion(nativeID string) error {
	id := live.operationID + ":" + nativeID
	live.mu.Lock()
	q := live.questions[id]
	if q != nil {
		q.cancelled = true
	}
	live.mu.Unlock()
	if q == nil {
		return nil
	}
	return live.emit("question.cancelled", map[string]any{"request_id": id})
}
