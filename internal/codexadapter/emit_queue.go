package codexadapter

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

const (
	// Bound the backlog one app-server can build up while the Agent's outbound
	// queue is stalled (for example while the control plane is unreachable).
	// At the bound, new events that a later event supersedes are dropped;
	// events that must never be lost are always queued.
	maxQueuedEvents     = 4096
	maxQueuedEventBytes = 32 << 20
	maxEmitRetryDelay   = 2 * time.Second
)

// After Close, failing deliveries are retried only this long, so the delivery
// goroutine always terminates.
var closeDrainTimeout = 10 * time.Second

// emitQueue decouples event delivery from the app-server reader. The Agent's
// emit can block for seconds while its outbound queue is full; doing that on
// readLoop would stall every RPC response behind it, including turn/interrupt
// and approvals. Events are delivered strictly in push order by at most one
// goroutine, which exits whenever the queue is empty.
type emitQueue struct {
	mu       sync.Mutex
	pending  []protocol.StreamEvent
	bytes    int
	running  bool
	dropped  int
	deadline time.Time // set by Close: stop retrying after it
}

// mustDeliver lists events whose loss leaves the control plane wrong until a
// human intervenes: a turn that never starts or ends, or an approval or
// question nobody can answer. They are queued regardless of the backlog bound
// and retried until delivered.
func mustDeliver(kind string) bool {
	switch kind {
	case "turn.accepted", "approval.requested", "codex.serverRequest.resolved", "codex.turn.completed", "codex.turn.failed", "codex.turn.cancelled":
		return true
	default:
		return false
	}
}

// publish queues an event for in-order delivery. It never blocks and never
// takes a.mu, so callers may hold a.mu: publishing in the critical section
// that changes turn state keeps the event order identical to the order of the
// state transitions (turn.accepted before the turn's terminal event).
func (a *Adapter) publish(event protocol.StreamEvent) {
	q := &a.events
	q.mu.Lock()
	defer q.mu.Unlock()
	size := len(event.Payload)
	if !mustDeliver(event.Kind) && (len(q.pending) >= maxQueuedEvents || q.bytes+size > maxQueuedEventBytes) {
		q.dropped++
		return
	}
	q.pending = append(q.pending, event)
	q.bytes += size
	if !q.running {
		q.running = true
		go a.deliverEvents()
	}
}

func (a *Adapter) deliverEvents() {
	q := &a.events
	failures := 0
	dropped := 0
	var reported time.Time
	for {
		q.mu.Lock()
		dropped += q.dropped
		q.dropped = 0
		if len(q.pending) == 0 {
			q.running = false
			q.pending = nil
			q.bytes = 0
			q.mu.Unlock()
			a.reportDropped(dropped)
			return
		}
		event := q.pending[0]
		deadline := q.deadline
		q.mu.Unlock()
		if dropped > 0 && time.Since(reported) >= 10*time.Second {
			a.reportDropped(dropped)
			dropped, reported = 0, time.Now()
		}
		err := a.deliver(event)
		// Retry the head until it is delivered so later events stay behind it.
		// The Agent's emit only fails while its outbound queue is full, which a
		// reconnect resolves; an event that can never be encoded is not retried.
		if err != nil && (deadline.IsZero() || time.Now().Before(deadline)) && (len(event.Payload) == 0 || json.Valid(event.Payload)) {
			failures++
			if failures == 1 || failures%10 == 0 {
				a.logger().Warn("could not deliver Codex event; retrying", "thread_id", event.StreamID, "kind", event.Kind, "attempts", failures, "error", err)
			}
			time.Sleep(emitRetryDelay(failures))
			continue
		}
		if err != nil {
			a.logger().Error("dropping undeliverable Codex event", "thread_id", event.StreamID, "kind", event.Kind, "attempts", failures+1, "error", err)
		} else if failures > 0 {
			a.logger().Info("delivered Codex event after retrying", "thread_id", event.StreamID, "kind", event.Kind, "attempts", failures+1)
		}
		failures = 0
		q.mu.Lock()
		q.pending[0] = protocol.StreamEvent{}
		q.pending = q.pending[1:]
		q.bytes -= len(event.Payload)
		q.mu.Unlock()
	}
}

func (a *Adapter) deliver(event protocol.StreamEvent) error {
	if a.emit == nil {
		return nil
	}
	return a.emit(event)
}

func (a *Adapter) reportDropped(count int) {
	if count > 0 {
		a.logger().Warn("dropped Codex progress events while the Agent outbound queue was backed up", "count", count, "limit_events", maxQueuedEvents, "limit_bytes", maxQueuedEventBytes)
	}
}

func emitRetryDelay(failures int) time.Duration {
	delay := 50 * time.Millisecond
	for i := 1; i < failures && delay < maxEmitRetryDelay; i++ {
		delay *= 2
	}
	return min(delay, maxEmitRetryDelay)
}

// close bounds how long the remaining events are retried once the adapter is
// closed. Already queued events are still delivered when possible.
func (q *emitQueue) close() {
	q.mu.Lock()
	q.deadline = time.Now().Add(closeDrainTimeout)
	q.mu.Unlock()
}

// reopen restores unbounded retries when a closed adapter is started again.
func (q *emitQueue) reopen() {
	q.mu.Lock()
	q.deadline = time.Time{}
	q.mu.Unlock()
}

func (a *Adapter) logger() *slog.Logger {
	if a.log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return a.log
}
