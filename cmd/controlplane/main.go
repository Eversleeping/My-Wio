package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/httpapi"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
	webassets "github.com/wio-platform/wio/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("control plane stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	address := env("WIO_ADDR", ":8080")
	databaseURL := strings.TrimSpace(os.Getenv("WIO_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = "wio.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	}
	devInsecure := envBool("WIO_DEV_INSECURE")
	database, err := store.Open(databaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	var vault *security.Vault
	masterKey := strings.TrimSpace(os.Getenv("WIO_MASTER_KEY"))
	if masterKey == "" && !strings.HasPrefix(databaseURL, "postgres") {
		vault = security.DevVault()
		log.Warn("using development Vault key; set WIO_MASTER_KEY before keeping real secrets")
	} else {
		vault, err = security.NewVault(masterKey)
		if err != nil {
			return err
		}
	}
	controlAgentToken, err := ensureControlPlaneAgentToken(context.Background(), database, vault, log)
	if err != nil {
		return err
	}
	hostname, hostnameErr := os.Hostname()
	if hostnameErr != nil {
		hostname = store.ControlPlaneServerName
	}
	if _, err := database.EnsureControlPlaneServer(context.Background(), hostname, controlAgentToken); err != nil {
		return fmt.Errorf("register control-plane Agent: %w", err)
	}

	hub := realtime.New()
	gateway := agentgateway.New(database, hub, vault, log)
	grpcServer := grpc.NewServer(
		grpc.ForceServerCodec(protocol.Codec()),
		grpc.MaxRecvMsgSize(8<<20),
		grpc.MaxSendMsgSize(8<<20),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
	)
	protocol.RegisterAgentServiceServer(grpcServer, gateway)
	frontend, err := fs.Sub(webassets.Dist, "dist")
	if err != nil {
		return err
	}
	httpHandler := httpapi.New(database, hub, gateway, vault, log, frontend, os.Getenv("WIO_PUBLIC_URL"), devInsecure)
	handler := h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		httpHandler.ServeHTTP(w, r)
	}), &http2.Server{})
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	listenerAddress := listener.Addr().String()

	errCh := make(chan error, 1)
	agentContext, stopAgent := context.WithCancel(context.Background())
	defer stopAgent()
	go func() {
		log.Info("Wio control plane listening", "address", listenerAddress, "version", buildinfo.Version)
		errCh <- server.Serve(listener)
	}()
	if runtime.GOOS == "linux" && envBoolDefault("WIO_CONTROL_AGENT_ENABLED", true) {
		if config, configErr := controlPlaneAgentConfig(listenerAddress, controlAgentToken); configErr != nil {
			log.Warn("control-plane Agent is disabled", "error", configErr)
		} else {
			go func() {
				if runErr := agent.NewClient(config, log).Run(agentContext); runErr != nil && !errors.Is(runErr, context.Canceled) {
					log.Warn("control-plane Agent stopped", "error", runErr)
				}
			}()
		}
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case signal := <-stop:
		log.Info("shutting down", "signal", signal.String())
		stopAgent()
	case err := <-errCh:
		stopAgent()
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	grpcServer.GracefulStop()
	return server.Shutdown(ctx)
}

func ensureControlPlaneAgentToken(ctx context.Context, database *store.Store, vault *security.Vault, log *slog.Logger) (string, error) {
	ciphertext, err := database.Setting(ctx, store.ControlPlaneAgentTokenKey, "")
	if err != nil {
		return "", fmt.Errorf("load control-plane Agent token: %w", err)
	}
	if ciphertext != "" {
		var token string
		if decryptErr := vault.Decrypt(ciphertext, &token); decryptErr == nil && strings.TrimSpace(token) != "" {
			return token, nil
		} else if decryptErr != nil {
			log.Warn("stored control-plane Agent token is invalid; rotating it", "error", decryptErr)
		}
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return "", fmt.Errorf("generate control-plane Agent token: %w", err)
	}
	protected, err := vault.Encrypt(token)
	if err != nil {
		return "", fmt.Errorf("protect control-plane Agent token: %w", err)
	}
	if err := database.SetSetting(ctx, store.ControlPlaneAgentTokenKey, protected); err != nil {
		return "", fmt.Errorf("save control-plane Agent token: %w", err)
	}
	return token, nil
}

func controlPlaneAgentConfig(address, token string) (agent.Config, error) {
	controlURL := strings.TrimRight(strings.TrimSpace(os.Getenv("WIO_CONTROL_AGENT_URL")), "/")
	if controlURL == "" {
		port := "8080"
		if _, parsedPort, err := net.SplitHostPort(address); err == nil && parsedPort != "" {
			port = parsedPort
		} else if strings.HasPrefix(address, ":") && strings.TrimPrefix(address, ":") != "" {
			port = strings.TrimPrefix(address, ":")
		} else if parsedPort, err := strconv.Atoi(strings.TrimSpace(address)); err == nil && parsedPort > 0 && parsedPort <= 65535 {
			port = strconv.Itoa(parsedPort)
		}
		controlURL = "http://127.0.0.1:" + port
	}
	config := agent.Config{
		ControlURL:         controlURL,
		ServerID:           store.ControlPlaneServerID,
		AgentToken:         token,
		ScanRoots:          controlPlaneAgentRoots(),
		CloneRoot:          env("WIO_CONTROL_AGENT_CLONE_ROOT", "/var/lib/wio-agent/projects"),
		StateDir:           env("WIO_CONTROL_AGENT_STATE_DIR", "/var/lib/wio-agent"),
		CodexPath:          env("WIO_CONTROL_AGENT_CODEX_PATH", "codex"),
		CodexAPIKeyFile:    env("WIO_CONTROL_AGENT_CODEX_KEY_FILE", "/etc/wio-agent/codex.key"),
		DockerPath:         env("WIO_CONTROL_AGENT_DOCKER_PATH", "docker"),
		PrerequisiteSocket: env("WIO_CONTROL_AGENT_PREREQUISITE_SOCKET", "/run/wio-prerequisites/helper.sock"),
		InsecureSkipVerify: envBool("WIO_CONTROL_AGENT_INSECURE_SKIP_VERIFY"),
	}
	if err := config.Validate(); err != nil {
		return agent.Config{}, err
	}
	return config, nil
}

func controlPlaneAgentRoots() []string {
	raw := strings.TrimSpace(os.Getenv("WIO_CONTROL_AGENT_SCAN_ROOTS"))
	if raw == "" {
		return []string{"/srv", "/opt", "/home"}
	}
	roots := make([]string, 0, 4)
	for _, value := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		if value = strings.TrimSpace(value); value != "" {
			roots = append(roots, value)
		}
	}
	if len(roots) == 0 {
		return []string{"/srv", "/opt", "/home"}
	}
	return roots
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envBoolDefault(name string, fallback bool) bool {
	value, exists := os.LookupEnv(name)
	if !exists || strings.TrimSpace(value) == "" {
		return fallback
	}
	return envBool(name)
}
