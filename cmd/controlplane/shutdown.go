package main

import (
	"context"
	"net/http"
	"sync"

	"google.golang.org/grpc"

	"github.com/wio-platform/wio/internal/agentgateway"
)

// HTTP Shutdown does not join hijacked h2c or WebSocket handlers. Track them
// explicitly, together with the background writers that share the database.
type controlPlaneLifecycle struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	stopping bool
	handlers sync.WaitGroup
	workers  sync.WaitGroup
}

func newControlPlaneLifecycle() *controlPlaneLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &controlPlaneLifecycle{ctx: ctx, cancel: cancel}
}

func (l *controlPlaneLifecycle) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		if l.stopping {
			l.mu.Unlock()
			http.Error(w, "control plane is shutting down", http.StatusServiceUnavailable)
			return
		}
		l.handlers.Add(1)
		l.mu.Unlock()
		defer l.handlers.Done()
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(l.ctx, cancel)
		defer stop()
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Go starts a background worker during startup, before Shutdown is called.
func (l *controlPlaneLifecycle) Go(work func(context.Context)) {
	l.workers.Add(1)
	go func() {
		defer l.workers.Done()
		work(l.ctx)
	}()
}

// Shutdown reports whether all database users have stopped. On a deadline the
// caller must leave the database open for the remaining cleanup and exit the
// process, rather than racing Store.Close against those writers.
func (l *controlPlaneLifecycle) Shutdown(ctx context.Context, server *http.Server, rpc *grpc.Server, gateway *agentgateway.Gateway) (bool, error) {
	l.mu.Lock()
	l.stopping = true
	l.mu.Unlock()
	l.cancel()
	done := make(chan error, 1)
	go func() {
		// grpc-go's ServeHTTP transport cannot Drain; GracefulStop panics.
		// Stop cancels the streams. The waits below also join work spawned by
		// RPC handlers, which neither Stop nor http.Server.Shutdown joins.
		rpc.Stop()
		err := server.Shutdown(ctx)
		if err != nil {
			_ = server.Close()
		}
		l.handlers.Wait()
		gateway.Wait()
		l.workers.Wait()
		done <- err
	}()
	select {
	case err := <-done:
		return true, err
	case <-ctx.Done():
		_ = server.Close()
		return false, ctx.Err()
	}
}
