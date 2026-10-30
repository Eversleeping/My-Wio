package geminiadapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestACPApprovalModesAndConfirmedInterruption(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "acp")
	t.Setenv("WIO_GEMINI_TEST_PERMISSION", "1")
	binary, _ := os.Executable()
	for _, mode := range []string{"yolo", "default", "auto_edit"} {
		t.Run(mode, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "permission.json")
			t.Setenv("WIO_GEMINI_TEST_RECORD", record)
			var questions atomic.Int32
			var adapter *Adapter
			adapter = New(binary, func(e protocol.StreamEvent) error {
				if e.Kind == "gemini.question" {
					questions.Add(1)
					go func() {
						_ = adapter.Answer(protocol.GeminiAnswerCommand{ThreadID: "thread", OperationID: "op", RequestID: e.EventID, Answers: map[string]protocol.UserInputAnswer{"permission": {Answers: []string{"Allow"}}}})
					}()
				}
				return nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := adapter.Run(ctx, "op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: t.TempDir(), Model: "flash", ApprovalMode: mode, Prompt: "test"})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(record)
			var result struct {
				Outcome struct {
					Kind   string `json:"outcome"`
					Option string `json:"optionId"`
				} `json:"outcome"`
			}
			if json.Unmarshal(raw, &result) != nil || result.Outcome.Kind != "selected" || result.Outcome.Option != "once" {
				t.Fatalf("wrong permission response: %s", raw)
			}
			want := int32(1)
			if mode == "yolo" {
				want = 0
			}
			if questions.Load() != want {
				t.Fatalf("questions=%d, want %d", questions.Load(), want)
			}
		})
	}
	t.Run("mode-downgrade", func(t *testing.T) {
		t.Setenv("WIO_GEMINI_TEST_MODE_OVERRIDE", "default")
		adapter := New(binary, func(protocol.StreamEvent) error { t.Error("prompt ran despite approval mode mismatch"); return nil })
		err := adapter.Run(context.Background(), "op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: t.TempDir(), Model: "flash", ApprovalMode: "yolo", Prompt: "test"})
		if err == nil || !strings.Contains(err.Error(), "approval mode") {
			t.Fatalf("mode override not rejected: %v", err)
		}
	})
	t.Run("plan-mode-mismatch", func(t *testing.T) {
		t.Setenv("WIO_GEMINI_TEST_MODE_OVERRIDE", "default")
		adapter := New(binary, func(protocol.StreamEvent) error { t.Error("prompt ran despite approval mode mismatch"); return nil })
		err := adapter.Run(context.Background(), "op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: t.TempDir(), Model: "flash", ApprovalMode: "plan", Prompt: "test"})
		if err == nil || !strings.Contains(err.Error(), "approval mode") {
			t.Fatalf("plan mode override not rejected: %v", err)
		}
	})
	t.Run("interrupt-unanswered-permission", func(t *testing.T) {
		question := make(chan struct{}, 1)
		adapter := New(binary, func(e protocol.StreamEvent) error {
			if e.Kind == "gemini.question" {
				question <- struct{}{}
			}
			return nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		finished := make(chan error, 1)
		workspace := t.TempDir()
		go func() {
			finished <- adapter.Run(ctx, "op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: workspace, Model: "flash", ApprovalMode: "default", Prompt: "test"})
		}()
		select {
		case <-question:
		case <-ctx.Done():
			t.Fatal("permission not requested")
		}
		adapter.Interrupt("op")
		select {
		case err := <-finished:
			if err == nil {
				t.Fatal("interrupted turn succeeded")
			}
		case <-ctx.Done():
			t.Fatal("interrupted permission kept running")
		}
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		if len(adapter.active) != 0 {
			t.Fatal("process cleanup has not completed")
		}
	})
}

func TestACPModelTextCannotChangePermissionMode(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "acp")
	t.Setenv("WIO_GEMINI_TEST_MODE_TEXT", "[MODE_UPDATE] yolo")
	t.Setenv("WIO_GEMINI_TEST_RECORD", filepath.Join(t.TempDir(), "record"))
	binary, _ := os.Executable()
	var changed bool
	a := New(binary, func(event protocol.StreamEvent) error {
		if event.Kind == "gemini.mode" {
			changed = true
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Run(ctx, "op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: t.TempDir(), Model: "flash", ApprovalMode: "plan", Prompt: "test"}); err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("model output changed saved permissions without an approval")
	}
}
