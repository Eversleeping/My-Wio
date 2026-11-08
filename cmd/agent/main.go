package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/prerequisite"
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
		return errors.New("usage: wio-agent <enroll|run|prerequisite-helper> [options]")
	}
	switch args[0] {
	case "version", "--version", "-version":
		fmt.Println(buildinfo.Version)
		return nil
	case "enroll":
		flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
		controlURL := flags.String("url", "", "Wio control-plane URL")
		token := flags.String("token", "", "one-time enrollment token (visible to other local users in the process list; prefer --token-file)")
		tokenFile := flags.String("token-file", "", "read the one-time enrollment token from this file, or from standard input when set to -")
		configPath := flags.String("config", defaultConfig(), "configuration file")
		cloneRoot := flags.String("clone-root", "/var/lib/wio-agent/projects", "managed Git clone root")
		stateDir := flags.String("state-dir", "/var/lib/wio-agent", "agent state directory")
		codexPath := flags.String("codex", "codex", "Codex CLI executable")
		dockerPath := flags.String("docker", "docker", "Docker CLI executable")
		insecure := flags.Bool("insecure-skip-verify", false, "skip TLS certificate verification (development only)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *controlURL == "" || *token == "" && *tokenFile == "" {
			return errors.New("--url and one of --token or --token-file are required")
		}
		secret, err := enrollmentToken(*token, *tokenFile, os.Stdin)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		config, err := agent.Enroll(ctx, agent.EnrollmentOptions{ControlURL: *controlURL, EnrollmentToken: secret, ConfigPath: *configPath, CloneRoot: *cloneRoot, StateDir: *stateDir, CodexPath: *codexPath, DockerPath: *dockerPath, InsecureSkipVerify: *insecure})
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
		if err := agent.ActivateCurrentUpdate(config.StateDir); err != nil {
			log.Warn("could not activate current Agent update; continuing with installed binary", "error", err)
		}
		// Self-updates replace this image with syscall.Exec while keeping the
		// PID. Reap children left by the previous image before starting any.
		agent.ReapInheritedChildren(log)
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return agent.NewClient(config, log).Run(ctx)
	case "prerequisite-helper":
		if runtime.GOOS != "linux" {
			return errors.New("deployment prerequisite helper is supported only on Linux")
		}
		flags := flag.NewFlagSet("prerequisite-helper", flag.ContinueOnError)
		socket := flags.String("socket", prerequisite.DefaultSocket, "Unix socket path")
		once := flags.Bool("once", false, "install and verify deployment prerequisites, then exit")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		if *once {
			ctx, timeoutCancel := context.WithTimeout(ctx, 15*time.Minute)
			defer timeoutCancel()
			result, err := prerequisite.RunOnce(ctx)
			for _, line := range result.Logs {
				fmt.Println(line)
			}
			return err
		}
		return prerequisite.Serve(ctx, *socket)
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

// maximumEnrollmentTokenSize bounds what --token-file reads.
const maximumEnrollmentTokenSize = 64 << 10

// enrollmentToken returns the one-time enrollment token. --token puts the
// token on the command line, where every local user can read it in the
// process list, so installers pass it with --token-file instead: a file
// path, or "-" for standard input.
func enrollmentToken(flagToken, tokenFile string, stdin io.Reader) (string, error) {
	if flagToken != "" && tokenFile != "" {
		return "", errors.New("use only one of --token and --token-file")
	}
	if tokenFile == "" {
		if flagToken == "" {
			return "", errors.New("enrollment token is empty")
		}
		return flagToken, nil
	}
	source := stdin
	name := "standard input"
	if tokenFile != "-" {
		file, err := os.Open(tokenFile)
		if err != nil {
			return "", fmt.Errorf("read enrollment token: %w", err)
		}
		defer file.Close()
		source, name = file, tokenFile
	}
	data, err := io.ReadAll(io.LimitReader(source, maximumEnrollmentTokenSize+1))
	if err != nil {
		return "", fmt.Errorf("read enrollment token from %s: %w", name, err)
	}
	if len(data) > maximumEnrollmentTokenSize {
		return "", fmt.Errorf("enrollment token in %s is too large", name)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("enrollment token in %s is empty", name)
	}
	return token, nil
}

var _ = os.Interrupt
