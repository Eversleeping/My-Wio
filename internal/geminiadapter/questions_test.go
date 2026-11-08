package geminiadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestQuestionAnswersResumeExactlyOnceAndExpire(t *testing.T) {
	events := make(chan protocol.StreamEvent, 10)
	a := New("", func(e protocol.StreamEvent) error { events <- e; return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	questions := []protocol.UserInputQuestion{{ID: "color", Question: "Choose a color", IsOther: true}}
	go func() {
		answer, err := a.ask(ctx, "op", "thread", questions)
		if err == nil && answer["color"].Answers[0] != "Blue" {
			t.Error("answer lost")
		}
		done <- err
	}()
	event := <-events
	command := protocol.GeminiAnswerCommand{ThreadID: "other-thread", OperationID: "op", RequestID: event.EventID, Answers: map[string]protocol.UserInputAnswer{"color": {Answers: []string{"Blue"}}}}
	if a.Answer(command) == nil {
		t.Fatal("cross-thread answer accepted")
	}
	command.ThreadID = "thread"
	command.OperationID = "other-op"
	if a.Answer(command) == nil {
		t.Fatal("cross-operation answer accepted")
	}
	command.OperationID = "op"
	command.Answers = map[string]protocol.UserInputAnswer{}
	if a.Answer(command) == nil {
		t.Fatal("empty answer accepted")
	}
	command.Answers = map[string]protocol.UserInputAnswer{"color": {Answers: []string{"Blue"}}}
	if err := a.Answer(command); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if a.Answer(command) == nil {
		t.Fatal("duplicate answer accepted")
	}
	if e := <-events; e.Kind != "gemini.question.answered" {
		t.Fatal(e.Kind)
	}
	go func() { _, err := a.ask(ctx, "op", "thread", questions); done <- err }()
	event = <-events
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancellation did not unblock tool")
	}
	command.RequestID = event.EventID
	if a.Answer(command) == nil {
		t.Fatal("expired answer accepted")
	}
}

func TestQuestionBridgeRequiresBearerAndReturnsStructuredAnswers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var a *Adapter
	a = New("", func(e protocol.StreamEvent) error {
		if e.Kind == "gemini.question" {
			go func() {
				_ = a.Answer(protocol.GeminiAnswerCommand{ThreadID: "t", OperationID: "op", RequestID: e.EventID, Answers: map[string]protocol.UserInputAnswer{"q": {Answers: []string{"confirmed"}}}})
			}()
		}
		return nil
	})
	config, closeServer, err := a.questionServer(ctx, "op", "t")
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	endpoint := config["url"].(string)
	response, err := http.Post(endpoint, "application/json", strings.NewReader(`{"id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("bridge lacked authentication")
	}
	auth := config["headers"].([]any)[0].(map[string]string)["value"]
	request := httptest.NewRequest("POST", endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"wio_ask_user","arguments":{"questions":[{"id":"q","header":"Choice","question":"Continue?"}]}}}`))
	request.RequestURI = ""
	request.Header.Set("Authorization", auth)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil || result.Result.IsError || len(result.Result.Content) != 1 || !strings.Contains(result.Result.Content[0].Text, "confirmed") {
		t.Fatalf("bad tool response: %+v %v", result, err)
	}
}

// emit can block for seconds while the Agent outbound queue is full. An
// answer waiting on it must not hold the adapter lock every turn needs, and
// the question must still be answered at most once.
func TestAnswerDoesNotHoldAdapterLockWhileEmitting(t *testing.T) {
	asked := make(chan protocol.StreamEvent, 1)
	answering := make(chan struct{}, 2)
	release := make(chan struct{})
	fail := true
	a := New("", func(e protocol.StreamEvent) error {
		switch e.Kind {
		case "gemini.question":
			asked <- e
		case "gemini.question.answered":
			answering <- struct{}{}
			<-release
			if fail {
				return errors.New("agent outbound queue is full")
			}
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	answers := make(chan map[string]protocol.UserInputAnswer, 1)
	go func() {
		answer, _ := a.ask(ctx, "op", "thread", []protocol.UserInputQuestion{{ID: "q", Question: "Continue?", IsOther: true}})
		answers <- answer
	}()
	question := <-asked
	command := protocol.GeminiAnswerCommand{ThreadID: "thread", OperationID: "op", RequestID: question.EventID, Answers: map[string]protocol.UserInputAnswer{"q": {Answers: []string{"Yes"}}}}
	first := make(chan error, 1)
	go func() { first <- a.Answer(command) }()
	<-answering
	locked := make(chan struct{})
	go func() {
		_ = a.Command() // needs a.mu
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("Answer held the adapter lock while emitting")
	}
	if err := a.Answer(command); err == nil {
		t.Fatal("a second answer was accepted while the first was being delivered")
	}
	close(release)
	if err := <-first; err == nil {
		t.Fatal("failed delivery was reported as answered")
	}
	// A failed delivery leaves the question answerable.
	fail = false
	if err := a.Answer(command); err != nil {
		t.Fatal(err)
	}
	if answer := <-answers; answer["q"].Answers[0] != "Yes" {
		t.Fatalf("answer not delivered: %#v", answer)
	}
}
