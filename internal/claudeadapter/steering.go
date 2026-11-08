package claudeadapter

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// Sessions owns only live CLI input pipes. It never replays a prompt into a
// replacement process after a disconnect or Agent restart.
type Sessions struct {
	mu    sync.Mutex
	turns map[string]*liveTurn
}

type liveInput struct {
	command  protocol.ClaudeSteerCommand
	received bool
	applied  bool
	done     chan struct{}
	err      error
}

type liveTurn struct {
	questions   map[string]*liveQuestion
	steering    bool
	mu          sync.Mutex
	writeMu     sync.Mutex
	threadID    string
	operationID string
	stdin       io.WriteCloser
	cancel      context.CancelFunc
	ready       bool
	closed      bool
	inputs      map[string]*liveInput
	idle        *time.Timer
	idleRound   uint64
	idleExpired bool
	background  int
	lastResult  bool
	queued      int
	emit        func(string, map[string]any) error
}

func (s *Sessions) Run(ctx context.Context, executable, operationID string, command protocol.ClaudeTurnCommand, emit func(protocol.StreamEvent) error, environment ...string) error {
	if !command.Steer && !command.Questions {
		return Run(ctx, executable, operationID, command, emit, environment...)
	}
	live := &liveTurn{threadID: command.ThreadID, operationID: operationID, steering: command.Steer, inputs: map[string]*liveInput{}, questions: map[string]*liveQuestion{}}
	s.mu.Lock()
	if s.turns == nil {
		s.turns = map[string]*liveTurn{}
	}
	if s.turns[operationID] != nil {
		s.mu.Unlock()
		return errors.New("Claude task already has an input stream")
	}
	s.turns[operationID] = live
	s.mu.Unlock()
	defer func() {
		live.finish()
		s.mu.Lock()
		delete(s.turns, operationID)
		s.mu.Unlock()
	}()
	return run(ctx, executable, operationID, command, emit, live, environment...)
}

func (s *Sessions) Steer(ctx context.Context, command protocol.ClaudeSteerCommand) error {
	if err := protocol.ValidateClaudeSteer(command); err != nil {
		return err
	}
	s.mu.Lock()
	live := s.turns[command.OperationID]
	s.mu.Unlock()
	if live == nil {
		// After an Agent restart the CLI may already have received this ID.
		// Without its live receipt ledger, do not claim it is safe to resend.
		return errors.New(protocol.ClaudeSteerUnconfirmedMessage)
	}
	if live.threadID != command.ThreadID {
		return errors.New("Claude no longer owns this running task; instruction was not sent")
	}
	return live.steer(ctx, command)
}

func (live *liveTurn) steer(ctx context.Context, command protocol.ClaudeSteerCommand) error {
	live.writeMu.Lock()
	live.mu.Lock()
	input := live.inputs[command.RequestID]
	if input != nil {
		if input.command.Prompt != command.Prompt || !slices.Equal(input.command.Images, command.Images) {
			live.mu.Unlock()
			live.writeMu.Unlock()
			return errors.New("Claude instruction ID already has different content")
		}
	} else {
		if !live.ready || live.closed {
			live.mu.Unlock()
			live.writeMu.Unlock()
			return errors.New("Claude input has closed; instruction was not sent")
		}
		if len(live.inputs) >= 32 {
			live.mu.Unlock()
			live.writeMu.Unlock()
			return errors.New("too many Claude steering instructions")
		}
		input = &liveInput{command: command, done: make(chan struct{})}
		live.inputs[command.RequestID] = input
		live.mu.Unlock()
		// Do not hold mu while writing: a large image can fill the pipe while
		// the CLI writes output, which the reader must remain free to drain.
		watchdog := time.AfterFunc(20*time.Second, func() { live.cancel() })
		raw, err := claudeUserInput(command.Prompt, command.Images, command.RequestID, "now")
		if err == nil {
			_, err = live.stdin.Write(raw)
		}
		watchdog.Stop()
		live.mu.Lock()
		if err != nil && !input.received {
			input.err = errors.New(protocol.ClaudeSteerUnconfirmedMessage)
			close(input.done)
			input.received = true
		}
	}
	done := input.done
	live.mu.Unlock()
	live.writeMu.Unlock()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		live.mu.Lock()
		err := input.err
		live.mu.Unlock()
		return err
	case <-ctx.Done():
		return errors.New(protocol.ClaudeSteerUnconfirmedMessage)
	case <-timer.C:
		return errors.New(protocol.ClaudeSteerUnconfirmedMessage)
	}
}

func (live *liveTurn) start(stdin io.WriteCloser, cancel context.CancelFunc, emit func(string, map[string]any) error) {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.stdin = stdin
	live.cancel = cancel
	live.emit = emit
}

func (live *liveTurn) initialized() error {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.ready = true
	if !live.steering {
		return nil
	}
	return live.emit("steer.ready", map[string]any{"ready": true})
}

func (live *liveTurn) acknowledge(id string, applied bool) error {
	live.mu.Lock()
	defer live.mu.Unlock()
	input := live.inputs[id]
	if input == nil {
		return nil
	}
	if !input.received {
		input.received = true
		close(input.done)
		if err := live.emit("steer.accepted", map[string]any{"request_id": id}); err != nil {
			return err
		}
	}
	if applied && !input.applied {
		input.applied = true
		return live.emit("steer.applied", map[string]any{"request_id": id, "text": input.command.Prompt, "images": input.command.Images})
	}
	return nil
}

func (live *liveTurn) progress() {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.lastResult = false
	if live.idle != nil {
		live.idle.Stop()
		live.idle = nil
	}
}

func (live *liveTurn) result(queued int) error {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.closed {
		return nil
	}
	live.lastResult = true
	live.queued = queued
	if live.background > 0 {
		return nil
	}
	for _, question := range live.questions {
		if question.answers == nil && !question.cancelled {
			return nil
		}
	}
	pending := false
	for _, input := range live.inputs {
		if !input.applied {
			pending = true
			break
		}
	}
	if !pending && queued == 0 {
		live.ready = false
		live.closed = true
		_ = live.stdin.Close()
		return live.emit("steer.closed", map[string]any{"ready": false})
	}
	// A hook may drop an injected prompt without a result. Do not leave an
	// idle CLI open forever or silently claim the instruction was applied.
	if live.idle != nil {
		live.idle.Stop()
	}
	live.idleRound++
	round := live.idleRound
	live.idle = time.AfterFunc(steerIdleTimeout, func() { live.expireIdle(round) })
	return nil
}

// steerIdleTimeout is how long a CLI may stay open after a result while an
// injected instruction remains unconfirmed.
var steerIdleTimeout = 45 * time.Second

// expireIdle stops a CLI that stayed idle after its last result. The turn
// itself already finished; finishedByIdleTimeout lets run report that result
// instead of a cancellation.
func (live *liveTurn) expireIdle(round uint64) {
	live.mu.Lock()
	if live.idle == nil || live.idleRound != round || live.closed {
		// Progress or a newer result replaced this timer after it fired.
		live.mu.Unlock()
		return
	}
	live.idleExpired = true
	cancel := live.cancel
	live.mu.Unlock()
	cancel()
}

// finishedByIdleTimeout reports whether the idle timer stopped the CLI with
// no progress after its last result, so that result is the turn's outcome.
func (live *liveTurn) finishedByIdleTimeout() bool {
	live.mu.Lock()
	defer live.mu.Unlock()
	return live.idleExpired && live.lastResult
}

func (live *liveTurn) finish() {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.ready = false
	live.closed = true
	if live.idle != nil {
		live.idle.Stop()
	}
	if live.stdin != nil {
		_ = live.stdin.Close()
	}
	for id, input := range live.inputs {
		if !input.received {
			input.received = true
			input.err = errors.New(protocol.ClaudeSteerUnconfirmedMessage)
			close(input.done)
		}
		if !input.applied && live.emit != nil {
			_ = live.emit("steer.unconfirmed", map[string]any{"request_id": id, "message": protocol.ClaudeSteerUnconfirmedMessage})
		}
	}
}

func (live *liveTurn) backgroundTasks(count int) error {
	live.mu.Lock()
	live.background = count
	finished := count == 0 && live.lastResult
	queued := live.queued
	live.mu.Unlock()
	if finished {
		return live.result(queued)
	}
	return nil
}
