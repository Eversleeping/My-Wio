package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"

	"syscall"
	"time"

	"github.com/wio-platform/wio/internal/enrollment"
	"github.com/wio-platform/wio/internal/hostplatform"
	"github.com/wio-platform/wio/internal/installer"
)

func installAgent(args []string) error {
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
	if !hostplatform.Administrator() {
		return errors.New("run the installer as administrator/root")
	}
	if err := installationPreflight(); err != nil {
		return err
	}
	if err := os.MkdirAll(hostplatform.Current().Installer, 0700); err != nil {
		return err
	}
	if err := hostplatform.ProtectInstaller(hostplatform.Current().Installer); err != nil {
		return err
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
		return enrollment.Run(ctx, *controlURL, *id, hostplatform.Current().Installer, func() (string, error) { return readInstallationCode(ctx) }, os.Stdout)
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
