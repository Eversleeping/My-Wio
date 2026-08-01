package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestWebsocketDeliversEventsAndClosesAfterHubDisconnectsSlowSubscriber(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	user := store.User{ID: "websocket-user", Username: "websocket-user", AuthMode: store.AuthModePassword}
	if err := database.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	const sessionToken = "websocket-session-token"
	if err := database.CreateSession(ctx, user.ID, store.HashToken(sessionToken), "csrf", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	hub := realtime.New()
	vault := security.DevVault()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(database, hub, agentgateway.New(database, hub, vault, log), vault, log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true)
	gate := newWebsocketWriteGate(t)
	server := httptest.NewUnstartedServer(handler)
	if err := server.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	server.Listener = gate.listener
	server.Start()
	defer func() {
		gate.ReleaseWrites()
		server.Close()
	}()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/ws"
	headers := http.Header{}
	headers.Set("Cookie", (&http.Cookie{Name: sessionCookie, Value: sessionToken}).String())
	connection, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	// The WebSocket handshake completes before the handler registers its Hub
	// subscription, so give that short post-upgrade setup a chance to finish.
	time.Sleep(20 * time.Millisecond)
	healthy := protocol.StreamEvent{EventID: "healthy-event", StreamID: "thread-1", Kind: "thread.updated", Payload: json.RawMessage(`{"status":"healthy"}`)}
	hub.Publish(healthy)
	connection.SetReadDeadline(time.Now().Add(time.Second))
	var received protocol.StreamEvent
	if err := connection.ReadJSON(&received); err != nil {
		t.Fatalf("normal realtime event did not reach websocket client: %v", err)
	}
	if received.EventID != healthy.EventID || received.Kind != healthy.Kind {
		t.Fatalf("unexpected normal realtime event: %#v", received)
	}

	gate.BlockWrites()
	stalled := protocol.StreamEvent{EventID: "stalled-event", StreamID: "thread-1", Kind: "thread.updated", Payload: json.RawMessage(`{"status":"stalled"}`)}
	hub.Publish(stalled)
	select {
	case <-gate.blocked:
	case <-time.After(time.Second):
		t.Fatal("websocket writer did not block while simulating a slow client")
	}
	// The first event is held in WriteJSON. These 256 fill the Hub's subscription
	// buffer, and the next publish forces the Hub to close that subscription.
	for index := 0; index <= 256; index++ {
		hub.Publish(stalled)
	}
	gate.ReleaseWrites()

	connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	frames := 0
	for {
		_, _, err := connection.ReadMessage()
		if err != nil {
			var networkErr net.Error
			if errors.As(err, &networkErr) && networkErr.Timeout() {
				t.Fatalf("websocket stayed open after its Hub subscription was closed; received %d frames", frames)
			}
			break
		}
		frames++
	}
	if frames != 257 {
		t.Fatalf("websocket drained %d stalled frames before close, want 257", frames)
	}
}

type websocketWriteGate struct {
	listener net.Listener

	mu      sync.RWMutex
	blocked chan struct{}
	release chan struct{}
	block   bool
	once    sync.Once
}

func newWebsocketWriteGate(t *testing.T) *websocketWriteGate {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := &websocketWriteGate{blocked: make(chan struct{}, 1), release: make(chan struct{})}
	gate.listener = websocketGateListener{Listener: listener, gate: gate}
	return gate
}

func (g *websocketWriteGate) BlockWrites() {
	g.mu.Lock()
	g.block = true
	g.mu.Unlock()
}

func (g *websocketWriteGate) ReleaseWrites() {
	g.once.Do(func() { close(g.release) })
}

func (g *websocketWriteGate) shouldBlock() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.block
}

type websocketGateListener struct {
	net.Listener
	gate *websocketWriteGate
}

func (l websocketGateListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return websocketGateConn{Conn: connection, gate: l.gate}, nil
}

type websocketGateConn struct {
	net.Conn
	gate *websocketWriteGate
}

func (c websocketGateConn) Write(payload []byte) (int, error) {
	if c.gate.shouldBlock() {
		select {
		case c.gate.blocked <- struct{}{}:
		default:
		}
		<-c.gate.release
	}
	return c.Conn.Write(payload)
}
