package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/wio-platform/wio/internal/agent"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log, os.Args[1:]); err != nil {
		log.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: wio-agent <enroll|run> [options]")
	}
	switch args[0] {
	case "enroll":
		flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
		controlURL := flags.String("url", "", "Wio control-plane URL")
		token := flags.String("token", "", "one-time enrollment token")
		configPath := flags.String("config", defaultConfig(), "configuration file")
		cloneRoot := flags.String("clone-root", "/var/lib/wio-agent/projects", "managed Git clone root")
		stateDir := flags.String("state-dir", "/var/lib/wio-agent", "agent state directory")
		codexPath := flags.String("codex", "codex", "Codex CLI executable")
		dockerPath := flags.String("docker", "docker", "Docker CLI executable")
		insecure := flags.Bool("insecure-skip-verify", false, "skip TLS certificate verification (development only)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *controlURL == "" || *token == "" {
			return errors.New("--url and --token are required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		config, err := agent.Enroll(ctx, agent.EnrollmentOptions{ControlURL: *controlURL, EnrollmentToken: *token, ConfigPath: *configPath, CloneRoot: *cloneRoot, StateDir: *stateDir, CodexPath: *codexPath, DockerPath: *dockerPath, InsecureSkipVerify: *insecure})
		if err != nil {
			return err
		}
		fmt.Printf("Enrolled server %s; configuration written to %s\n", config.ServerID, *configPath)
		return nil
	case "run":
		if runtime.GOOS != "linux" {
			return errors.New("wio-agent run is supported only on Linux")
		}
		flags := flag.NewFlagSet("run", flag.ContinueOnError)
		configPath := flags.String("config", defaultConfig(), "configuration file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		config, err := agent.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return agent.NewClient(config, log).Run(ctx)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func defaultConfig() string {
	if runtime.GOOS == "linux" {
		return "/etc/wio-agent/config.json"
	}
	return "wio-agent.json"
}

var _ = os.Interrupt
