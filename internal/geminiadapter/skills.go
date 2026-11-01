package geminiadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

const maxGeminiSkillsOutput = 512 << 10

var geminiSkillHeading = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]{0,127}) \[(Enabled|Disabled)\]( \[Built-in\])?$`)
var geminiInstalledSkill = regexp.MustCompile(`(?m)^Successfully installed skill: ([A-Za-z0-9][A-Za-z0-9_-]{0,127}) \(scope: (workspace|user), location: `)

// ListSkills asks the same CLI and workspace used for turns to discover skills.
// The command is a read-only operation; it does not activate a skill.
func (a *Adapter) ListSkills(parent context.Context, operationID, workspace string) (protocol.GeminiSkillsResult, error) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	a.mu.Lock()
	if a.updating || a.active[operationID] != nil {
		a.mu.Unlock()
		return protocol.GeminiSkillsResult{}, errors.New("Gemini configuration is changing; retry skill discovery")
	}
	executable := a.command
	environment := append([]string(nil), a.environment...)
	environment = append(environment, "GEMINI_CLI_TRUST_WORKSPACE=true", "NO_COLOR=1", "FORCE_COLOR=0")
	a.active[operationID] = cancel
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.active, operationID); a.mu.Unlock() }()

	cmd := exec.CommandContext(ctx, executable, "skills", "list", "--all")
	cmd.Dir = workspace
	cmd.Env = mergedEnvironment(environment)
	configureProcess(cmd)
	var output boundedSkillBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return protocol.GeminiSkillsResult{}, ctx.Err()
		}
		return protocol.GeminiSkillsResult{}, errors.New("could not list Gemini skills; check the CLI installation and workspace configuration")
	}
	return skillSnapshot(output.String(), workspace, cmd.Env)
}

func skillSnapshot(output, workspace string, environment []string) (protocol.GeminiSkillsResult, error) {
	result, err := parseGeminiSkills(output)
	if err != nil {
		return result, err
	}
	home, _ := os.UserHomeDir()
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "HOME="); ok {
			home = value
		}
	}
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "GEMINI_CLI_HOME="); ok && value != "" {
			home = value
		}
	}
	for i := range result.Skills {
		skill := &result.Skills[i]
		skill.Scope = "external"
		if skill.Builtin {
			skill.Scope = "builtin"
			continue
		}
		for _, candidate := range []struct{ scope, root string }{{"workspace", filepath.Join(workspace, ".gemini", "skills")}, {"user", filepath.Join(home, ".gemini", "skills")}} {
			rel, err := filepath.Rel(candidate.root, skill.Location)
			if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				skill.Scope = candidate.scope
				break
			}
		}
	}
	return result, nil
}

// Configuration changes reserve the entire Gemini adapter, including other
// workspaces on this account. No running CLI loses its skill configuration.
func (a *Adapter) ManageSkill(parent context.Context, command protocol.GeminiSkillManageCommand) (protocol.GeminiSkillsResult, error) {
	if err := protocol.ValidateGeminiSkillManagement(command); err != nil {
		return protocol.GeminiSkillsResult{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	a.mu.Lock()
	if a.updating || len(a.active) != 0 {
		a.mu.Unlock()
		return protocol.GeminiSkillsResult{}, errors.New("finish active Gemini tasks before managing skills")
	}
	a.updating = true
	executable, environment := a.command, mergedEnvironment(append([]string(nil), a.environment...))
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.updating = false; a.mu.Unlock() }()
	environment = append(environment, "GEMINI_CLI_TRUST_WORKSPACE=true", "NO_COLOR=1", "FORCE_COLOR=0")
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Dir, cmd.Env = command.Workspace, environment
		configureProcess(cmd)
		var output boundedSkillBuffer
		// The official CLI writes mutation confirmations to stderr, even on success.
		// Keep both streams bounded and use them only for local verification.
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			return "", errors.New("Gemini skill command failed; check the source, CLI and account permissions")
		}
		return output.String(), nil
	}
	list := func() (protocol.GeminiSkillsResult, error) {
		output, err := run("skills", "list", "--all")
		if err != nil {
			return protocol.GeminiSkillsResult{}, err
		}
		return skillSnapshot(output, command.Workspace, environment)
	}
	before, err := list()
	if err != nil {
		return before, err
	}
	if command.Action != "install" {
		found := false
		for _, skill := range before.Skills {
			if skill.Name == command.Name {
				found = true
				if command.Action == "uninstall" && (skill.Builtin || skill.Scope != command.Scope) {
					return before, errors.New("this skill is not installed in the selected scope")
				}
			}
		}
		if !found {
			return before, errors.New("skill no longer exists; refresh the list")
		}
	}
	args := []string{"skills", command.Action}
	if command.Action == "install" {
		source := command.Source
		if !strings.HasPrefix(source, "https://") {
			if command.Path != "" {
				return before, errors.New("repository subdirectory is only valid for HTTPS sources")
			}
			if !filepath.IsAbs(source) {
				source = filepath.Join(command.Workspace, source)
			}
			source, err = filepath.EvalSymlinks(source)
			root, rootErr := filepath.EvalSymlinks(command.Workspace)
			rel, relErr := filepath.Rel(root, source)
			if err != nil || rootErr != nil || relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return before, errors.New("local skill source must be inside this workspace")
			}
		}
		args = append(args, source, "--scope", command.Scope, "--consent")
		if command.Path != "" {
			args = append(args, "--path", command.Path)
		}
	} else {
		args = append(args, command.Name)
		if command.Action != "enable" {
			args = append(args, "--scope", command.Scope)
		}
	}
	changeOutput, err := run(args...)
	if err != nil {
		return before, err
	}
	after, err := list()
	if err != nil {
		return after, errors.New("skill command finished but its result could not be verified; refresh the skill list")
	}
	found := false
	for _, skill := range after.Skills {
		if skill.Name != command.Name {
			continue
		}
		found = true
		if (command.Action == "enable" && !skill.Enabled) || (command.Action == "disable" && skill.Enabled) || (command.Action == "uninstall" && skill.Scope == command.Scope) {
			return after, errors.New("Gemini did not apply the requested skill change; check overriding settings")
		}
	}
	if (command.Action == "enable" || command.Action == "disable") && !found {
		return after, errors.New("skill disappeared while updating; refresh the skill list")
	}
	if command.Action == "install" {
		installed := geminiInstalledSkill.FindAllStringSubmatch(changeOutput, -1)
		if len(installed) == 0 {
			return after, errors.New("CLI did not confirm any installed skills; refresh the list and check the source")
		}
		for _, match := range installed {
			verified := false
			for _, skill := range after.Skills {
				if skill.Name == match[1] && skill.Scope == command.Scope && match[2] == command.Scope && !skill.Builtin {
					verified = true
					break
				}
			}
			if !verified {
				return after, errors.New("an installed skill could not be discovered in the selected scope; check overriding skills and settings")
			}
		}
	}
	return after, nil
}

type boundedSkillBuffer struct{ bytes.Buffer }

func (b *boundedSkillBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxGeminiSkillsOutput {
		return 0, errors.New("Gemini skill list is too large")
	}
	return b.Buffer.Write(p)
}

func parseGeminiSkills(output string) (protocol.GeminiSkillsResult, error) {
	result := protocol.GeminiSkillsResult{Skills: []protocol.GeminiSkill{}}
	var current *protocol.GeminiSkill
	noSkills := false
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "No skills discovered." {
			noSkills = true
			continue
		}
		if match := geminiSkillHeading.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			if len(result.Skills) >= 500 {
				return protocol.GeminiSkillsResult{}, errors.New("too many Gemini skills")
			}
			result.Skills = append(result.Skills, protocol.GeminiSkill{Name: match[1], Enabled: match[2] == "Enabled", Builtin: match[3] != ""})
			current = &result.Skills[len(result.Skills)-1]
			continue
		}
		if current != nil && strings.HasPrefix(strings.TrimSpace(line), "Description:") {
			description := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Description:"))
			if len([]rune(description)) > 512 {
				description = string([]rune(description)[:512])
			}
			current.Description = description
		}
		if current != nil && strings.HasPrefix(strings.TrimSpace(line), "Location:") {
			current.Location = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Location:"))
		}
	}
	if len(result.Skills) == 0 && !noSkills {
		return protocol.GeminiSkillsResult{}, errors.New("Gemini returned an unrecognized skill list")
	}
	return result, nil
}
