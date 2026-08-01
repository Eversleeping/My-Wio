package realtime

import (
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestPublishDeliversToHealthySubscriber(t *testing.T) {
	hub := New()
	id, events := hub.Subscribe()
	defer hub.Unsubscribe(id)

	want := protocol.StreamEvent{EventID: "event-1", StreamID: "thread-1", Kind: "thread.updated"}
	hub.Publish(want)

	got, ok := <-events
	if !ok || got.EventID != want.EventID {
		t.Fatalf("unexpected event: %#v open=%v", got, ok)
	}
}

func TestPublishDisconnectsStalledSubscriber(t *testing.T) {
	hub := New()
	id, events := hub.Subscribe()
	for index := 0; index < cap(events); index++ {
		hub.Publish(protocol.StreamEvent{EventID: "buffered"})
	}

	hub.Publish(protocol.StreamEvent{EventID: "overflow"})

	for index := 0; index < cap(events); index++ {
		if _, ok := <-events; !ok {
			t.Fatalf("subscription closed before buffered event %d was drained", index)
		}
	}
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("stalled subscription remained open after overflow")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stalled subscription to close")
	}
	// The overflow path already removed the client; an explicit cleanup must be
	// safe and must not close the channel a second time.
	hub.Unsubscribe(id)
}
