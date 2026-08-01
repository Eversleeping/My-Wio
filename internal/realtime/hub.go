package realtime

import (
	"sync"

	"github.com/wio-platform/wio/internal/protocol"
)

type Hub struct {
	mu      sync.RWMutex
	next    int
	clients map[int]chan protocol.StreamEvent
}

func New() *Hub { return &Hub{clients: make(map[int]chan protocol.StreamEvent)} }

func (h *Hub) Subscribe() (int, <-chan protocol.StreamEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	ch := make(chan protocol.StreamEvent, 256)
	h.clients[id] = ch
	return id, ch
}

func (h *Hub) Unsubscribe(id int) {
	h.mu.Lock()
	if ch, ok := h.clients[id]; ok {
		delete(h.clients, id)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *Hub) Publish(event protocol.StreamEvent) {
	h.mu.RLock()
	stalled := make([]int, 0)
	for id, ch := range h.clients {
		select {
		case ch <- event:
		default:
			stalled = append(stalled, id)
		}
	}
	h.mu.RUnlock()

	if len(stalled) == 0 {
		return
	}
	// A WebSocket event is an invalidation hint. Silently dropping the final
	// hint can leave a browser stale forever, so disconnect slow subscribers.
	// The browser reconnect path performs a full refresh and safely catches up.
	h.mu.Lock()
	for _, id := range stalled {
		if ch, ok := h.clients[id]; ok {
			delete(h.clients, id)
			close(ch)
		}
	}
	h.mu.Unlock()
}
