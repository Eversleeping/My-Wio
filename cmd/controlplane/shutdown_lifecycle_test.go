package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestShutdownWaitsForDatabaseUsersAndRejectsNewRequests(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gateway := agentgateway.New(database, realtime.New(), security.DevVault(), log)
	lifecycle := newControlPlaneLifecycle()
	defer lifecycle.cancel()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	workerCancelled, requestCancelled, requestStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	writes := make(chan error, 2)
	lifecycle.Go(func(ctx context.Context) {
		<-ctx.Done()
		close(workerCancelled)
		<-release
		writes <- database.SetSetting(context.Background(), "shutdown-worker", "finished")
	})
	handler := lifecycle.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestCancelled)
		<-release
		writes <- database.SetSetting(context.Background(), "shutdown-request", "finished")
	}))
	server := httptest.NewServer(handler)
	defer func() { unblock(); server.Close() }()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := server.Client().Get(server.URL)
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-requestStarted:
	case <-ctx.Done():
		t.Fatal("request did not start")
	}
	done := make(chan error, 1)
	go func() {
		joined, err := lifecycle.Shutdown(ctx, server.Config, grpc.NewServer(), gateway)
		if !joined && err == nil {
			err = errors.New("shutdown returned without joining database users")
		}
		done <- err
	}()
	for _, cancelled := range []<-chan struct{}{workerCancelled, requestCancelled} {
		select {
		case <-cancelled:
		case <-ctx.Done():
			t.Fatal("database user was not cancelled")
		}
	}
	select {
	case err := <-done:
		t.Fatalf("shutdown returned while database writes were pending: %v", err)
	default:
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("new request admitted during shutdown: %d", w.Code)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("shutdown did not finish after writes were released")
	}
	for range 2 {
		if err := <-writes; err != nil {
			t.Fatal(err)
		}
	}
	<-requestDone
}

func TestShutdownDeadlineDoesNotClaimWorkersHaveStopped(t *testing.T) {
	lifecycle := newControlPlaneLifecycle()
	release, workerDone := make(chan struct{}), make(chan struct{})
	lifecycle.Go(func(context.Context) {
		defer close(workerDone)
		<-release // A worker that temporarily cannot honour cancellation.
	})
	defer func() { close(release); <-workerDone }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	joined, err := lifecycle.Shutdown(ctx, &http.Server{}, grpc.NewServer(), &agentgateway.Gateway{})
	if joined || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline reported database safe to close: joined=%v error=%v", joined, err)
	}
}
