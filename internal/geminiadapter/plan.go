package geminiadapter

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxGeminiPlanBytes = 128 << 10

// ACP 0.66 provides only a plan path in its approval request. Read the plan
// from this workspace or this native session's private plans directory before
// presenting an approval, and never follow a path outside those roots.
func readGeminiPlan(title, workspace, sessionID string, environment []string) (string, error) {
	path, ok := strings.CutPrefix(title, "Requesting plan approval for: ")
	if !ok || !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".md") {
		return "", errors.New("invalid Gemini plan path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("could not open Gemini plan")
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", errors.New("could not open Gemini workspace")
	}
	allowed := pathWithin(root, resolved)
	if !allowed && sessionID != "" && filepath.Base(sessionID) == sessionID {
		home, _ := os.UserHomeDir()
		for _, entry := range environment {
			key, value, found := strings.Cut(entry, "=")
			if found && strings.EqualFold(key, "GEMINI_CLI_HOME") && value != "" {
				home = value
			}
		}
		tempRoot, err := filepath.EvalSymlinks(filepath.Join(home, ".gemini", "tmp"))
		if err == nil && pathWithin(tempRoot, resolved) {
			rel, err := filepath.Rel(tempRoot, resolved)
			parts := strings.Split(rel, string(filepath.Separator))
			allowed = err == nil && len(parts) == 4 && parts[1] == sessionID && parts[2] == "plans"
		}
	}
	if !allowed {
		return "", errors.New("Gemini plan is outside this workspace and session")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", errors.New("could not open Gemini plan")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxGeminiPlanBytes {
		return "", errors.New("Gemini plan is not a readable, bounded file")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxGeminiPlanBytes+1))
	if err != nil || len(content) > maxGeminiPlanBytes || !utf8.Valid(content) || len(strings.TrimSpace(string(content))) == 0 {
		return "", errors.New("Gemini plan could not be displayed")
	}
	return string(content), nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
