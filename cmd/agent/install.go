package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/wio-platform/wio/internal/enrollment"
	"github.com/wio-platform/wio/internal/installer"
)

func installAgent(args []string) error {
	if runtime.GOOS != "linux" {
		return errors.New("Agent installation is supported only on Linux")
	}
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	controlURL := flags.String("url", "", "Wio control-plane URL")
	id := flags.String("registration", "", "temporary registration ID")
	requestStdin := flags.Bool("request-stdin", false, "read the installation plan from stdin (SSH bootstrap)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*requestStdin && (*controlURL == "" || !enrollment.ValidID(*id)) {
		return errors.New("--url and --registration are required")
	}
	if os.Geteuid() != 0 {
		return errors.New("run the installer as root (sudo)")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("a running systemd is required")
	}
	release, err := lockAgentInstaller()
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, time.Hour)
	defer timeout()
	if !*requestStdin {
		return enrollment.Run(ctx, *controlURL, *id, "/var/lib/wio-installer", func() (string, error) { return readInstallationCode(ctx) }, os.Stdout)
	}
	var request installer.InstallRequest
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 2<<20)).Decode(&request); err != nil {
		return errors.New("invalid installation plan")
	}
	encoder := json.NewEncoder(os.Stdout)
	request.Progress = func(p installer.InstallProgress) {
		_ = encoder.Encode(installer.Event{Type: "progress", Step: p.Step, Current: p.Current, Total: p.Total})
	}
	result, err := installer.InstallLocal(ctx, request)
	if err != nil {
		code := "installation_failed"
		if errors.Is(err, installer.ErrHeartbeatPending) {
			code = "agent_heartbeat_pending"
		}
		_ = encoder.Encode(installer.Event{Type: "error", Code: code, Error: "target installation failed", Result: &result})
		return errors.New("target installation failed")
	}
	return encoder.Encode(installer.Event{Type: "complete", Result: &result})
}
