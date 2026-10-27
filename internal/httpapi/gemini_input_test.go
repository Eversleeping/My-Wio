package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestGeminiImagesAndAnswersStayInTheirActiveTurn(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "image-agent")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.72"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/images", Name: "images"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(db)
	image := protocol.TurnImage{DataURL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="}
	send := func(images []protocol.TurnImage) *httptest.ResponseRecorder {
		return threadResourceRequest(t, "POST", "/turns", thread.ID, map[string]any{"images": images}, api.startGeminiTurn)
	}
	if w := send([]protocol.TurnImage{image}); w.Code != 409 {
		t.Fatalf("old Agent accepted image: %d %s", w.Code, w.Body.String())
	}
	if err = db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.73"}); err != nil {
		t.Fatal(err)
	}
	for _, images := range [][]protocol.TurnImage{nil, {{DataURL: "data:image/png;base64,bad"}}, {image, image, image, image, image}} {
		if w := send(images); w.Code != 400 {
			t.Fatalf("invalid images accepted: %d", w.Code)
		}
	}
	if w := send([]protocol.TurnImage{image}); w.Code != 202 {
		t.Fatalf("image-only send failed: %d %s", w.Code, w.Body.String())
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	op, _ := db.Operation(ctx, thread.OperationID)
	var command protocol.GeminiTurnCommand
	_ = json.Unmarshal([]byte(op.Payload), &command)
	if !thread.InputSupported || !command.Interactive || len(command.Images) != 1 || command.Images[0] != image {
		t.Fatalf("image lost: %+v", command)
	}
	events, _ := db.Events(ctx, thread.ID, 0, 50)
	if len(events) != 1 {
		t.Fatal(events)
	}
	var message struct {
		Images []protocol.TurnImage `json:"images"`
	}
	_ = json.Unmarshal(events[0].Payload, &message)
	if len(message.Images) != 1 || message.Images[0] != image {
		t.Fatal("image not saved in history")
	}
	_, err = db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET status='running' WHERE id=?"), thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := json.Marshal(map[string]any{"operation_id": op.ID, "questions": []protocol.UserInputQuestion{{ID: "color", Question: "Which color?"}}})
	question, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "gemini.question", Payload: detail})
	if err != nil {
		t.Fatal(err)
	}
	answer := func(answers map[string]protocol.UserInputAnswer) *httptest.ResponseRecorder {
		route := chi.NewRouteContext()
		route.URLParams.Add("threadID", thread.ID)
		route.URLParams.Add("requestID", question.EventID)
		return directJSONRequest(t, "POST", "/answer", map[string]any{"answers": answers}, nil, func(w http.ResponseWriter, r *http.Request) {
			api.answerGeminiQuestion(w, r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route)))
		})
	}
	if w := answer(nil); w.Code != 409 {
		t.Fatal("empty answer accepted")
	}
	answers := map[string]protocol.UserInputAnswer{"color": {Answers: []string{"Blue"}}}
	w := answer(answers)
	if w.Code != 202 {
		t.Fatalf("answer rejected: %d %s", w.Code, w.Body.String())
	}
	var queued struct {
		OperationID string `json:"operation_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &queued)
	if again := answer(answers); again.Body.String() != w.Body.String() {
		t.Fatalf("answer retry not idempotent: %s", again.Body.String())
	}
	answerOp, _ := db.Operation(ctx, queued.OperationID)
	if answerOp.Kind != "gemini.question.answer" || answerOp.WorkspaceWrite != 0 {
		t.Fatalf("answer blocked waiting turn: %+v", answerOp)
	}
	current, _ := db.GeminiThread(ctx, thread.ID)
	if current.OperationID != op.ID || current.Status != "running" {
		t.Fatal("answer changed active turn")
	}
	if w := answer(map[string]protocol.UserInputAnswer{"color": {Answers: []string{"Green"}}}); w.Code != 409 {
		t.Fatal("different retry accepted")
	}
	_, _ = db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET status='idle',operation_id='' WHERE id=?"), thread.ID)
	stale, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "gemini.question", Payload: detail})
	if err != nil {
		t.Fatal(err)
	}
	question = stale
	if w := answer(answers); w.Code != 409 {
		t.Fatal("stale question accepted")
	}
}
