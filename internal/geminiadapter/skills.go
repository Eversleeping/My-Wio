package geminiadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

const maxGeminiSkillsOutput = 512 << 10

var geminiSkillHeading = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]{0,127}) \[(Enabled|Disabled)\]( \[Built-in\])?$`)

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
	return parseGeminiSkills(output.String())
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
	}
	if len(result.Skills) == 0 && !noSkills {
		return protocol.GeminiSkillsResult{}, errors.New("Gemini returned an unrecognized skill list")
	}
	return result, nil
}
