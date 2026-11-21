package installer

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/windows"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/hostplatform"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"os"
	"time"
)

func preparePortableService(ctx context.Context, l hostplatform.Layout) error {
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService("WioAgent")
	if err == nil {
		defer service.Close()
		config, err := service.Config()
		if err != nil {
			return err
		}
		arguments, parseErr := windows.DecomposeCommandLine(config.BinaryPathName)
		if parseErr != nil || len(arguments) == 0 || !hostplatform.Equal(arguments[0], l.Binary) || !strings.EqualFold(config.ServiceStartName, `NT SERVICE\WioAgent`) {
			return errors.New("existing service does not belong to Wio")
		}
		status, err := service.Query()
		if err != nil {
			return err
		}
		if status.State != svc.Stopped {
			if _, err = service.Control(svc.Stop); err != nil {
				return err
			}
			for status.State != svc.Stopped {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Second):
				}
				status, err = service.Query()
				if err != nil {
					return err
				}
			}
		}
		config.BinaryPathName = windows.ComposeCommandLine([]string{l.Binary, "run", "--config", l.Config})
		config.StartType = mgr.StartAutomatic
		return service.UpdateConfig(config)
	}
	service, err = manager.CreateService("WioAgent", l.Binary, mgr.Config{DisplayName: "Wio Agent", StartType: mgr.StartAutomatic, ServiceStartName: `NT SERVICE\WioAgent`}, "run", "--config", l.Config)
	if err != nil {
		return err
	}
	defer service.Close()
	// The service runs under its own unprivileged virtual account.
	_, err = portableCommand(ctx, "sc.exe", "sidtype", "WioAgent", "unrestricted")
	return err
}
func protectPortableState(ctx context.Context, l hostplatform.Layout, state string) error {
	for _, dir := range []string{l.State, state} {
		if err := hostplatform.ProtectInstaller(dir); err != nil {
			return err
		}
		if _, err := portableCommand(ctx, "icacls.exe", dir, "/grant", `NT SERVICE\WioAgent:(OI)(CI)F`, "/T", "/L", "/Q"); err != nil {
			return err
		}
	}
	// Binary files remain administrator-owned; workers only receive read/execute.
	directory := filepath.Dir(l.Binary)
	// Protect the directory first, then reset children to inherit it. Applying
	// /inheritance:r recursively with directory-only grants empties file DACLs.
	if _, err := portableCommand(ctx, "icacls.exe", directory, "/inheritance:r", "/grant:r", `*S-1-5-18:(OI)(CI)F`, `*S-1-5-32-544:(OI)(CI)F`, `NT SERVICE\WioAgent:(OI)(CI)RX`, "/Q"); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := portableCommand(ctx, "icacls.exe", filepath.Join(directory, entry.Name()), "/reset", "/T", "/L", "/Q"); err != nil {
			return err
		}
	}
	return nil
}
func startPortableService(ctx context.Context, l hostplatform.Layout) error {
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService("WioAgent")
	if err != nil {
		return err
	}
	defer service.Close()
	return service.Start()
}
func ensurePortableNode(ctx context.Context) error {
	nodeDir := filepath.Join(filepath.Dir(hostplatform.Current().Binary), "node")
	if err := os.Setenv("PATH", nodeDir+";"+os.Getenv("PATH")); err != nil {
		return err
	}
	if _, err := portableCommand(ctx, filepath.Join(nodeDir, "node.exe"), "-e", "if(Number(process.versions.node.split('.')[0])<20)process.exit(1)"); err == nil {
		return nil
	}
	_, err := portableCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", strings.ReplaceAll(windowsNodeScript, "__WIO_NODE_DIR__", powershellQuote(nodeDir)))
	return err
}
func uninstallPortableLocal(ctx context.Context, l hostplatform.Layout) error {
	manager, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService("WioAgent")
	if err != nil {
		return err
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State != svc.Stopped {
		_, err = service.Control(svc.Stop)
		if err != nil {
			return err
		}
	}
	for status.State != svc.Stopped {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		status, err = service.Query()
		if err != nil {
			return err
		}
	}
	if err := service.Delete(); err != nil {
		return err
	}
	if err := os.RemoveAll(l.State); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Dir(l.Binary)); err != nil {
		return err
	}
	return nil
}

func ensurePortableGit(ctx context.Context) error {
	gitDir := filepath.Join(filepath.Dir(hostplatform.Current().Binary), "git", "cmd")
	if err := os.Setenv("PATH", gitDir+";"+os.Getenv("PATH")); err != nil {
		return err
	}
	if _, err := portableCommand(ctx, "git", "--version"); err == nil {
		return nil
	}
	_, err := portableCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", strings.ReplaceAll(windowsGitScript, "__WIO_GIT_DIR__", powershellQuote(filepath.Dir(gitDir))))
	return err
}

func installPortableCLIs(ctx context.Context, state string, versions CLIVersions) (warnings []string, resultErr error) {
	layout := hostplatform.Current()
	planFile := filepath.Join(layout.State, ".installer-clis.json")
	resultFile := filepath.Join(layout.State, ".installer-cli-result.json")
	raw, err := json.Marshal(serviceCLIPlan{State: state, Versions: versions})
	if err != nil {
		return nil, err
	}
	if err := atomicPortableWrite(planFile, raw, 0600); err != nil {
		return nil, err
	}
	defer os.Remove(planFile)
	if err := os.Remove(resultFile); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	defer os.Remove(resultFile)
	manager, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService("WioAgent")
	if err != nil {
		return nil, err
	}
	defer service.Close()
	original, err := service.Config()
	if err != nil {
		return nil, err
	}
	temporary := original
	temporary.BinaryPathName = windows.ComposeCommandLine([]string{layout.Binary, "service-install", "--plan", planFile})
	if err := service.UpdateConfig(temporary); err != nil {
		return nil, err
	}
	defer func() {
		_, _ = service.Control(svc.Stop)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			status, err := service.Query()
			if err != nil || status.State == svc.Stopped {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err := service.UpdateConfig(original); err != nil {
			resultErr = err
		}
	}()
	if err := service.Start(); err != nil {
		return nil, err
	}
	for {
		status, err := service.Query()
		if err != nil {
			return nil, err
		}
		if status.State == svc.Stopped {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	raw, err = os.ReadFile(resultFile)
	if err != nil {
		return nil, errors.New("CLI service did not finish successfully")
	}
	var result serviceCLIResult
	if json.Unmarshal(raw, &result) != nil || !ValidWarnings(result.Warnings) {
		return nil, ErrInstallation
	}
	return result.Warnings, nil
}
