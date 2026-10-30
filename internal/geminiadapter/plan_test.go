package geminiadapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadGeminiPlanIsBoundedAndScoped(t *testing.T) {
	workspace, home, outside := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	local := filepath.Join(workspace, "plans", "plan.md")
	private := filepath.Join(home, ".gemini", "tmp", "project", "session-1", "plans", "plan.md")
	foreign := filepath.Join(home, ".gemini", "tmp", "project", "session-2", "plans", "plan.md")
	secret := filepath.Join(outside, "secret.md")
	for _, path := range []string{local, private, foreign, secret} {
		write(path, "# Reviewable plan")
	}
	env := []string{"GEMINI_CLI_HOME=" + home}
	for _, path := range []string{local, private} {
		if content, err := readGeminiPlan("Requesting plan approval for: "+path, workspace, "session-1", env); err != nil || content != "# Reviewable plan" {
			t.Fatalf("valid plan %s: %q %v", path, content, err)
		}
	}
	for _, path := range []string{foreign, secret, "relative.md"} {
		if _, err := readGeminiPlan("Requesting plan approval for: "+path, workspace, "session-1", env); err == nil {
			t.Fatalf("accepted outside plan %s", path)
		}
	}
	write(local, strings.Repeat("x", maxGeminiPlanBytes+1))
	if _, err := readGeminiPlan("Requesting plan approval for: "+local, workspace, "session-1", env); err == nil {
		t.Fatal("accepted an oversized plan")
	}
}
