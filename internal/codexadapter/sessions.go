package codexadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

const sessionIdleTimeout = 5 * time.Minute

// Sessions isolates each Wio conversation in its own app-server process. The
// shared adapter is used only for server/workspace capability queries.
type Sessions struct {
	lifecycle sync.RWMutex
	mu        sync.Mutex
	command   string
	env       []string
	log       *slog.Logger
	emit      EmitFunc
	shared    *Adapter
	sessions  map[string]*session
	closed    bool
	stop      chan struct{}
	done      chan struct{}
}

type session struct {
	adapter  *Adapter
	users    int
	lastUsed time.Time
}

func NewSessions(command string, environment []string, log *slog.Logger, emit EmitFunc) *Sessions {
	p := &Sessions{
		command: command, env: append([]string(nil), environment...), log: log, emit: emit,
		shared:   NewWithEnvironment(command, environment, log, emit),
		sessions: make(map[string]*session), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go p.reapLoop()
	return p
}

// acquire returns a session lease without holding the pool mutex during any
// RPC. A slow initialization in one conversation cannot block another.
func (p *Sessions) acquire(threadID string) (*Adapter, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, errors.New("Codex sessions are closed")
	}
	if threadID == "" {
		return p.shared, func() {}, nil
	}
	s := p.sessions[threadID]
	if s == nil {
		s = &session{lastUsed: time.Now()}
		s.adapter = NewWithEnvironment(p.command, p.env, p.log, func(event protocol.StreamEvent) error {
			p.mu.Lock()
			s.lastUsed = time.Now()
			p.mu.Unlock()
			return p.emit(event)
		})
		p.sessions[threadID] = s
	}
	s.users++
	return s.adapter, func() {
		p.mu.Lock()
		s.users--
		s.lastUsed = time.Now()
		p.mu.Unlock()
	}, nil
}

func (p *Sessions) withSession(threadID string, action func(*Adapter) error) error {
	p.lifecycle.RLock()
	defer p.lifecycle.RUnlock()
	a, release, err := p.acquire(threadID)
	if err != nil {
		return err
	}
	defer release()
	return action(a)
}

func (p *Sessions) Warm(ctx context.Context) error {
	return p.withSession("", func(a *Adapter) error { return a.Warm(ctx) })
}

func (p *Sessions) StartTurn(ctx context.Context, command protocol.StartTurnCommand) error {
	if strings.TrimSpace(command.ThreadID) == "" {
		return errors.New("Wio thread ID is required")
	}
	return p.withSession(command.ThreadID, func(a *Adapter) error { return a.StartTurn(ctx, command) })
}

func (p *Sessions) RewriteTurn(ctx context.Context, command protocol.RewriteTurnCommand) error {
	if strings.TrimSpace(command.Start.ThreadID) == "" {
		return errors.New("Wio thread ID is required")
	}
	return p.withSession(command.Start.ThreadID, func(a *Adapter) error { return a.RewriteTurn(ctx, command) })
}

func (p *Sessions) Interrupt(ctx context.Context, command protocol.InterruptTurnCommand) error {
	if strings.TrimSpace(command.ThreadID) == "" {
		return errors.New("Wio thread ID is required")
	}
	return p.withSession(command.ThreadID, func(a *Adapter) error { return a.Interrupt(ctx, command) })
}

func (p *Sessions) SteerTurn(ctx context.Context, command protocol.SteerTurnCommand) error {
	if strings.TrimSpace(command.ThreadID) == "" {
		return errors.New("Wio thread ID is required")
	}
	return p.withSession(command.ThreadID, func(a *Adapter) error { return a.SteerTurn(ctx, command) })
}

func (p *Sessions) Decide(command protocol.ApprovalDecisionCommand) error {
	if strings.TrimSpace(command.ThreadID) == "" {
		return errors.New("Wio thread ID is required")
	}
	return p.withSession(command.ThreadID, func(a *Adapter) error { return a.Decide(command) })
}

func (p *Sessions) ForkThread(ctx context.Context, command protocol.ForkThreadCommand) (result protocol.ForkThreadResult, err error) {
	if strings.TrimSpace(command.TargetThreadID) == "" {
		return result, errors.New("target Wio thread ID is required")
	}
	err = p.withSession(command.TargetThreadID, func(a *Adapter) error {
		result, err = a.ForkThread(ctx, command)
		return err
	})
	return result, err
}

func (p *Sessions) CodexOperation(ctx context.Context, kind string, payload json.RawMessage) (result protocol.CodexCapabilityResult, err error) {
	var base protocol.CodexSnapshotCommand
	if err = json.Unmarshal(payload, &base); err != nil {
		return result, err
	}
	threadID := base.ThreadID
	if kind == "codex.models.list" || kind == "codex.skills.list" || kind == "codex.status.snapshot" {
		threadID = ""
	}
	err = p.withSession(threadID, func(a *Adapter) error {
		result, err = a.CodexOperation(ctx, kind, payload)
		return err
	})
	return result, err
}

func (a *Adapter) hasActiveTurn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, state := range a.turns {
		if state.Active {
			return true
		}
	}
	return false
}

func (p *Sessions) ReconfigureEnvironment(environment []string, update func() error) error {
	return p.reconfigure(func() error { return p.shared.ReconfigureEnvironment(environment, update) }, func() {
		p.env = append([]string(nil), environment...)
	})
}

func (p *Sessions) ReconfigureCommand(command string, update func() error) error {
	return p.reconfigure(func() error { return p.shared.ReconfigureCommand(command, update) }, func() {
		p.command = command
	})
}

func (p *Sessions) reconfigure(update func() error, apply func()) error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("Codex sessions are closed")
	}
	var adapters []*Adapter
	for _, s := range p.sessions {
		if s.adapter.hasActiveTurn() {
			p.mu.Unlock()
			return errors.New("Codex has an active turn; retry the update after all sessions complete")
		}
		adapters = append(adapters, s.adapter)
	}
	p.mu.Unlock()
	if err := update(); err != nil {
		return err
	}
	p.mu.Lock()
	apply()
	p.sessions = make(map[string]*session)
	p.mu.Unlock()
	for _, a := range adapters {
		_ = a.Close()
	}
	return nil
}

func (p *Sessions) reapLoop() {
	defer close(p.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			p.reapIdle(now)
		case <-p.stop:
			return
		}
	}
}

func (p *Sessions) reapIdle(now time.Time) {
	p.lifecycle.RLock()
	defer p.lifecycle.RUnlock()
	p.mu.Lock()
	var idle []*Adapter
	for id, s := range p.sessions {
		if s.users == 0 && !s.lastUsed.IsZero() && now.Sub(s.lastUsed) >= sessionIdleTimeout && !s.adapter.hasActiveTurn() {
			delete(p.sessions, id)
			idle = append(idle, s.adapter)
		}
	}
	p.mu.Unlock()
	for _, a := range idle {
		_ = a.Close()
	}
}

func (p *Sessions) Close() error {
	p.lifecycle.Lock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.lifecycle.Unlock()
		return nil
	}
	p.closed = true
	close(p.stop)
	var adapters []*Adapter
	adapters = append(adapters, p.shared)
	for _, s := range p.sessions {
		adapters = append(adapters, s.adapter)
	}
	p.sessions = make(map[string]*session)
	p.mu.Unlock()
	for _, a := range adapters {
		_ = a.Close()
	}
	p.lifecycle.Unlock()
	<-p.done
	return nil
}
