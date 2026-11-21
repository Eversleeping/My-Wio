package main

import (
	"context"
	"errors"
	"flag"
	"github.com/wio-platform/wio/internal/installer"

	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/childprocess"
	"golang.org/x/sys/windows/svc"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

type agentService struct {
	log    *slog.Logger
	config string
}

func runPlatformService(log *slog.Logger, config string, worker bool) (bool, error) {
	if worker {
		return false, nil
	}
	service, err := svc.IsWindowsService()
	if err != nil {
		return true, err
	}
	if !service {
		return false, nil
	}
	return true, svc.Run("WioAgent", agentService{log, config})
}
func (s agentService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	config, err := agent.LoadConfig(s.config)
	if err != nil {
		s.log.Error("service configuration failed")
		return false, 1
	}
	base, err := os.Executable()
	if err != nil {
		return false, 1
	}
	logfile, err := os.OpenFile(filepath.Join(config.StateDir, "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return false, 1
	}
	defer logfile.Close()
	s.log = slog.New(slog.NewJSONHandler(logfile, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		for ctx.Err() == nil {
			binary, err := agent.ServiceUpdateBinary(config.StateDir, base)
			if err != nil {
				s.log.Warn("update rolled back", "error", err)
			}
			cmd := childprocess.CommandContext(ctx, binary, "run", "--config", s.config, "--worker")
			cmd.Env = childprocess.Environment(ctx, "PATH="+filepath.Join(filepath.Dir(base), "node")+";"+filepath.Join(filepath.Dir(base), "git", "cmd")+";"+filepath.Join(filepath.Dir(filepath.Dir(base)), "Git", "cmd")+";"+os.Getenv("PATH"), "CODEX_HOME="+config.StateDir+`\.Codex`, "HOME="+config.StateDir, "USERPROFILE="+config.StateDir, "APPDATA="+config.StateDir+`\AppData\Roaming`, "LOCALAPPDATA="+config.StateDir+`\AppData\Local`)
			cmd.Dir = config.StateDir
			cmd.Stdout, cmd.Stderr = logfile, logfile
			err = childprocess.Run(cmd)
			if ctx.Err() != nil {
				done <- nil
				return
			}
			if err == nil {
				done <- nil
				return
			}
			s.log.Warn("Agent worker exited; restarting", "error", err)
			select {
			case <-ctx.Done():
				done <- nil
				return
			case <-time.After(3 * time.Second):
			}
		}
		done <- nil
	}()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case <-done:
			return false, 0
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
					return false, 0
				case <-time.After(25 * time.Second):
					return false, 1
				}
			}
		}
	}
}

type cliInstallService struct{ plan string }

func runCLIService(args []string) error {
	flags := flag.NewFlagSet("service-install", flag.ContinueOnError)
	plan := flags.String("plan", "", "private CLI installation plan")
	if err := flags.Parse(args); err != nil {
		return err
	}
	service, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !service {
		return errors.New("CLI installation must run under the Wio service account")
	}
	return svc.Run("WioAgent", cliInstallService{*plan})
}
func (s cliInstallService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- installer.RunServiceCLIs(ctx, s.plan) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil {
				return false, 1
			}
			return false, 0
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				cancel()
				status <- svc.Status{State: svc.StopPending}
			}
		}
	}
}
