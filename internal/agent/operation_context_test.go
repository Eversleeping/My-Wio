package agent

import (
	"context"
	"testing"
	"time"
)

func TestGeminiTurnsFollowParentLifetimeWithoutMaintenanceDeadline(t *testing.T) {
	for _, kind := range []string{"gemini.turn.start", "gemini.turn.rewrite"} {
		t.Run(kind, func(t *testing.T) {
			parent, stop := context.WithCancel(context.Background())
			defer stop()
			ctx, cancel := operationContext(parent, kind)
			defer cancel()
			if deadline, ok := ctx.Deadline(); ok {
				t.Fatalf("long turn still has a fixed deadline: %v", deadline)
			}
			stop()
			if ctx.Err() != context.Canceled {
				t.Fatal("turn did not stop with Agent shutdown")
			}

			deadline := time.Now().Add(time.Minute)
			bounded, release := context.WithDeadline(context.Background(), deadline)
			defer release()
			child, end := operationContext(bounded, kind)
			defer end()
			if got, ok := child.Deadline(); !ok || !got.Equal(deadline) {
				t.Fatal("turn ignored caller's explicit deadline")
			}
		})
	}
}

func TestMaintenanceOperationsKeepTheirDeadlines(t *testing.T) {
	for kind, limit := range map[string]time.Duration{
		"workspace.files": 10 * time.Minute, "gemini.question.answer": 10 * time.Minute,
		"gemini.turn.interrupt": 10 * time.Minute, "gemini.thread.fork": 10 * time.Minute,
		"codex.models.list": 10 * time.Minute, "deploy.container": time.Hour,
		"agent.update": 30 * time.Minute, "codex.update": 30 * time.Minute,
	} {
		t.Run(kind, func(t *testing.T) {
			before := time.Now()
			ctx, cancel := operationContext(context.Background(), kind)
			defer cancel()
			deadline, ok := ctx.Deadline()
			if !ok || deadline.Before(before.Add(limit)) || deadline.After(time.Now().Add(limit)) {
				t.Fatalf("unexpected maintenance deadline: %v", deadline)
			}
		})
	}
}
