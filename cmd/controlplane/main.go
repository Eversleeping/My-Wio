package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

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

	errCh := make(chan error, 1)
	go func() {
		log.Info("Wio control plane listening", "address", address, "version", buildinfo.Version)
		errCh <- server.ListenAndServe()
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case signal := <-stop:
		log.Info("shutting down", "signal", signal.String())
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	grpcServer.GracefulStop()
	return server.Shutdown(ctx)
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
