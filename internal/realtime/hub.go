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
	defer h.mu.RUnlock()
	for _, ch := range h.clients {
		select {
		case ch <- event:
		default:
		}
	}
}
