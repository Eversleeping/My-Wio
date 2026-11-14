package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

// Exercise run itself: a helper that only calls grpc.Stop would conceal the
// ServeHTTP transport panic in the production signal path.
func TestControlPlaneSignalShutdown(t *testing.T) {
	if os.Getenv("WIO_TEST_SIGNAL_SHUTDOWN") == "1" {
		log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
		if err := run(log); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX signals")
	}
	for _, signal := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			root := t.TempDir()
			databaseURL := filepath.Join(root, "wio.db") + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
			database, err := store.Open(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			token, err := ensureControlPlaneAgentToken(ctx, database, security.DevVault(), log)
			if err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, binary, "-test.run=^TestControlPlaneSignalShutdown$")
			command.Env = replaceEnvironment(os.Environ(), map[string]string{
				"WIO_TEST_SIGNAL_SHUTDOWN": "1", "WIO_ADDR": "127.0.0.1:0",
				"WIO_DATABASE_URL": databaseURL, "WIO_MASTER_KEY": "", "WIO_DEV_INSECURE": "true",
				"WIO_CONTROL_AGENT_ENABLED": "false", "WIO_SETUP_TOKEN": acceptanceSetupToken,
			})
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			address := make(chan string, 1)
			scanned := make(chan struct{})
			go func() {
				defer close(scanned)
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					var entry struct{ Address string }
					if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Address != "" {
						select {
						case address <- entry.Address:
						default:
						}
					}
				}
			}()
			done := make(chan error, 1)
			exited := make(chan struct{})
			go func() {
				defer close(exited)
				done <- command.Wait()
			}()
			defer func() {
				_ = command.Process.Kill()
				<-exited
				<-scanned
			}()
			var endpoint string
			select {
			case endpoint = <-address:
			case err := <-done:
				t.Fatalf("server exited before listening: %v %s", err, stderr.String())
			case <-ctx.Done():
				t.Fatal("server did not listen")
			}
			connection, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.ForceCodec(protocol.Codec())))
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			stream, err := protocol.NewAgentServiceClient(connection).Connect(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Recv(); err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&protocol.AgentEnvelope{Kind: "heartbeat", PayloadJSON: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			jar, _ := cookiejar.New(nil)
			api := &acceptanceAPI{baseURL: "http://" + endpoint, client: &http.Client{Jar: jar, Timeout: 3 * time.Second}}
			api.setupAndLogin(t)
			headers := http.Header{}
			request, _ := http.NewRequest(http.MethodGet, api.baseURL, nil)
			for _, cookie := range jar.Cookies(request.URL) {
				headers.Add("Cookie", cookie.String())
			}
			ws, _, err := websocket.DefaultDialer.DialContext(ctx, "ws://"+endpoint+"/api/ws", headers)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			if err := command.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				<-scanned
				if err != nil {
					t.Fatalf("signal shutdown failed: %v\n%s", err, stderr.String())
				}
			case <-ctx.Done():
				t.Fatal("shutdown did not finish")
			case <-time.After(5 * time.Second):
				_ = command.Process.Signal(syscall.SIGQUIT)
				<-done
				t.Fatalf("shutdown stalled:\n%s", stderr.String())
			}
			var state string
			if err := database.DB.GetContext(ctx, &state, database.Q("SELECT status FROM servers WHERE id=?"), store.ControlPlaneServerID); err != nil || state != "offline" {
				t.Fatalf("Agent cleanup was not persisted before exit: %q %v", state, err)
			}
			_ = ws.SetReadDeadline(time.Now().Add(time.Second))
			if _, _, err := ws.ReadMessage(); err == nil || strings.Contains(err.Error(), "timeout") {
				t.Fatalf("WebSocket stayed open after shutdown: %v", err)
			}
		})
	}
}
