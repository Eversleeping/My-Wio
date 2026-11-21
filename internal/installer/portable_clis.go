package installer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/hostplatform"
)

type serviceCLIPlan struct {
	State    string
	Versions CLIVersions
}
type serviceCLIResult struct {
	Warnings []string `json:"warnings"`
}

func installCLIs(ctx context.Context, state string, versions CLIVersions, command func(context.Context, string, ...string) (string, error)) ([]string, error) {
	warnings := []string{}
	for _, cli := range []struct{ kind, pkg, version string }{{"codex", "@openai/codex", versions.Codex}, {"gemini", "@google/gemini-cli", versions.Gemini}, {"claude", "@anthropic-ai/claude-code", versions.Claude}} {
		if !codexcli.ValidTargetVersion(cli.version) {
			return nil, ErrInvalidTarget
		}
		prefix := filepath.Join(state, cli.kind, "versions", cli.version)
		_, err := command(ctx, "npm", "install", "--prefix", prefix, "--omit=dev", "--registry="+officialNPMRegistry, cli.pkg+"@"+cli.version)
		executable := filepath.Join(prefix, "node_modules", ".bin", cli.kind)
		if runtime.GOOS == "windows" {
			executable += ".cmd"
		}
		if err == nil {
			var version string
			version, err = command(ctx, executable, "--version")
			if err == nil && !codexcli.ReportedVersionMatches(version, cli.version) {
				err = ErrInstallation
			}
		}
		if err != nil {
			warnings = append(warnings, cli.kind+"_install_failed")
			continue
		}
		if err := atomicPortableWrite(filepath.Join(state, cli.kind, "current-version"), []byte(cli.version+"\n"), 0600); err != nil {
			return nil, err
		}
	}
	return warnings, nil
}

// RunServiceCLIs is executed by SCM under the WioAgent virtual account. The
// elevated installer only publishes a credential-free plan and controls SCM;
// npm lifecycle hooks and version probes never execute as Administrator.
func RunServiceCLIs(ctx context.Context, planFile string) error {
	layout := hostplatform.Current()
	if !hostplatform.Equal(planFile, filepath.Join(layout.State, ".installer-clis.json")) {
		return ErrInvalidTarget
	}
	file, err := os.Open(planFile)
	if err != nil {
		return err
	}
	defer file.Close()
	var plan serviceCLIPlan
	if json.NewDecoder(io.LimitReader(file, 4096)).Decode(&plan) != nil || !hostplatform.Contains(layout.State, plan.State) {
		return ErrInvalidTarget
	}
	if err := os.Setenv("PATH", filepath.Join(filepath.Dir(layout.Binary), "node")+string(os.PathListSeparator)+filepath.Join(filepath.Dir(layout.Binary), "git", "cmd")+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return err
	}
	for key, value := range map[string]string{"HOME": plan.State, "USERPROFILE": plan.State, "APPDATA": filepath.Join(plan.State, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(plan.State, "AppData", "Local")} {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	command := func(ctx context.Context, name string, args ...string) (string, error) {
		cmd := childprocess.CommandContext(childprocess.WithScope(ctx, childprocess.Installation), name, args...)
		output, err := childprocess.CombinedOutput(cmd)
		if err != nil {
			return "", errors.New("CLI installation command failed")
		}
		return strings.TrimSpace(string(output)), nil
	}
	warnings, err := installCLIs(ctx, plan.State, plan.Versions, command)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(serviceCLIResult{Warnings: warnings})
	if err != nil {
		return err
	}
	return atomicPortableWrite(filepath.Join(layout.State, ".installer-cli-result.json"), raw, 0600)
}
