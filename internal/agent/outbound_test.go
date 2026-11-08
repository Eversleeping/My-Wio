package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wio-platform/wio/internal/protocol"
)

func outboundTestClient(size int) *Client {
	return &Client{
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		outbound: make(chan *protocol.AgentEnvelope, size),
		seen:     make(map[string]*operationExecution),
	}
}

func oversizedData() json.RawMessage {
	return json.RawMessage(`"` + strings.Repeat("a", maxAgentMessageBytes) + `"`)
}

func TestOversizedOperationResultIsReplacedByFailure(t *testing.T) {
	client := outboundTestClient(2)
	if err := client.queue("operation_result", protocol.OperationResult{OperationID: "huge", Status: "succeeded", Data: oversizedData()}, true); err != nil {
		t.Fatal(err)
	}
	envelope := receiveAgentEnvelope(t, client.outbound)
	var result protocol.OperationResult
	if err := json.Unmarshal(envelope.PayloadJSON, &result); err != nil {
		t.Fatal(err)
	}
	if envelope.Kind != "operation_result" || result.OperationID != "huge" || result.Status != "failed" || !strings.Contains(result.Message, "result too large") || len(result.Data) != 0 {
		t.Fatalf("oversized result was not replaced: %#v", result)
	}
	if encodedEnvelopeSize(envelope) > maxAgentMessageBytes {
		t.Fatal("replacement still exceeds the message limit")
	}
}

func TestOversizedEventIsDroppedInsteadOfQueued(t *testing.T) {
	client := outboundTestClient(2)
	if err := client.queue("claude_event", protocol.StreamEvent{StreamID: "thread", Kind: "claude.message", Payload: oversizedData()}, true); err != nil {
		t.Fatalf("dropping an oversized event must not fail its producer: %v", err)
	}
	if len(client.outbound) != 0 {
		t.Fatal("oversized event entered the outbound queue")
	}
}

func TestLimitOperationResultKeepsReceiptSmall(t *testing.T) {
	small := protocol.OperationResult{OperationID: "small", Status: "succeeded", Data: json.RawMessage(`{"changes":[]}`)}
	if limited := limitOperationResult(small); limited.Status != "succeeded" || string(limited.Data) != string(small.Data) {
		t.Fatalf("small result changed: %#v", limited)
	}
	limited := limitOperationResult(protocol.OperationResult{OperationID: "big", Status: "succeeded", Data: oversizedData()})
	if limited.Status != "failed" || limited.OperationID != "big" || !strings.Contains(limited.Message, "result too large") {
		t.Fatalf("oversized result was not limited: %#v", limited)
	}
}

type fakeAgentStream struct {
	grpc.ClientStream
	mu    sync.Mutex
	sent  []*protocol.AgentEnvelope
	fail  func(*protocol.AgentEnvelope) error
	calls int
}

func (s *fakeAgentStream) Send(envelope *protocol.AgentEnvelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fail != nil {
		if err := s.fail(envelope); err != nil {
			return err
		}
	}
	s.sent = append(s.sent, envelope)
	return nil
}

func (s *fakeAgentStream) Recv() (*protocol.ControlEnvelope, error) { return nil, io.EOF }
func (s *fakeAgentStream) CloseSend() error                         { return nil }

func (s *fakeAgentStream) kinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.sent))
	for _, envelope := range s.sent {
		out = append(out, envelope.Kind)
	}
	return out
}

func runSendLoopUntil(t *testing.T, client *Client, stream *fakeAgentStream, until func() bool) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.sendLoop(ctx, stream, errs)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !until() {
		select {
		case err := <-errs:
			<-done
			cancel()
			return err
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("send loop did not reach the expected state")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	return nil
}

func TestSendLoopRetriesFailedEnvelopeFirstOnNextConnection(t *testing.T) {
	client := outboundTestClient(8)
	for _, kind := range []string{"turn.started", "turn.completed"} {
		if err := client.queue(kind, map[string]string{}, true); err != nil {
			t.Fatal(err)
		}
	}
	broken := &fakeAgentStream{fail: func(*protocol.AgentEnvelope) error { return io.EOF }}
	if err := runSendLoopUntil(t, client, broken, func() bool { return false }); !errors.Is(err, io.EOF) {
		t.Fatalf("send failure was not reported: %v", err)
	}
	healthy := &fakeAgentStream{}
	_ = runSendLoopUntil(t, client, healthy, func() bool { return len(healthy.kinds()) == 2 })
	if kinds := healthy.kinds(); kinds[0] != "turn.started" || kinds[1] != "turn.completed" {
		t.Fatalf("retry reordered the stream: %v", kinds)
	}
}

func TestSendLoopNeverRetriesMessageRejectedForSize(t *testing.T) {
	client := outboundTestClient(4)
	// Bypass the queue-time check to model a size estimate that was too low.
	client.outbound <- client.newEnvelope("operation_result", json.RawMessage(`{"operation_id":"op","status":"succeeded","data":{"files":[]}}`))
	rejecting := &fakeAgentStream{fail: func(envelope *protocol.AgentEnvelope) error {
		return status.Error(codes.ResourceExhausted, "trying to send message larger than max")
	}}
	_ = runSendLoopUntil(t, client, rejecting, func() bool { return false })
	healthy := &fakeAgentStream{}
	_ = runSendLoopUntil(t, client, healthy, func() bool { return len(healthy.kinds()) == 1 })
	var result protocol.OperationResult
	if err := json.Unmarshal(healthy.sent[0].PayloadJSON, &result); err != nil {
		t.Fatal(err)
	}
	if result.OperationID != "op" || result.Status != "failed" || !strings.Contains(result.Message, "result too large") {
		t.Fatalf("rejected result was retried unchanged: %#v", result)
	}
}

func TestQueueTrackedSignalsAfterSend(t *testing.T) {
	client := outboundTestClient(4)
	if err := client.queue("heartbeat", map[string]string{}, true); err != nil {
		t.Fatal(err)
	}
	sent, err := client.queueTracked("operation_result", protocol.OperationResult{OperationID: "update", Status: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-sent:
		t.Fatal("tracked message reported sent before the stream received it")
	default:
	}
	stream := &fakeAgentStream{}
	_ = runSendLoopUntil(t, client, stream, func() bool { return len(stream.kinds()) == 2 })
	if !waitForSent(sent, time.Second) {
		t.Fatal("tracked message was not reported sent")
	}
	if waitForSent(nil, time.Millisecond) {
		t.Fatal("missing tracker reported a send")
	}
}
