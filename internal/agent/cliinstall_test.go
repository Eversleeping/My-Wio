package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wio-platform/wio/internal/geminiadapter"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestGeminiInstallVerifiesBeforeActivationAndPersists(t *testing.T) {
	originalRun, originalFind, originalRead := runCodexUpdateCommand, findCodexUpdateCommand, readCodexUpdateVersion
	t.Cleanup(func() {
		runCodexUpdateCommand, findCodexUpdateCommand, readCodexUpdateVersion = originalRun, originalFind, originalRead
	})
	dir := t.TempDir()
	c := &Client{config: Config{StateDir: dir}, gemini: geminiadapter.New("gemini", nil)}
	findCodexUpdateCommand = func(string) (string, error) { return "npm", nil }
	var args []string
	runCodexUpdateCommand = func(_ context.Context, name string, values ...string) error {
		if name != "npm" {
			t.Fatal(name)
		}
		args = values
		return nil
	}
	readCodexUpdateVersion = func(context.Context, string) string { return "0.64.0" }
	command := protocol.CLIInstallCommand{Kind: "gemini", Version: "0.65.0"}
	if err := c.installCLI(context.Background(), command); err == nil {
		t.Fatal("version mismatch accepted")
	}
	if c.gemini.Command() != "gemini" || effectiveGeminiPath(dir) != "gemini" {
		t.Fatal("failed verification changed active CLI")
	}
	readCodexUpdateVersion = func(context.Context, string) string { return "0.65.0" }
	if err := c.installCLI(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	exe := managedGeminiExecutable(dir, "0.65.0")
	want := []string{"install", "--prefix", filepath.Join(dir, "gemini", "versions", "0.65.0"), "--omit=dev", "--registry=https://registry.npmjs.org", "@google/gemini-cli@0.65.0"}
	if !reflect.DeepEqual(args, want) || c.gemini.Command() != exe {
		t.Fatalf("activation %v %s", args, c.gemini.Command())
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("fake CLI"), 0700); err != nil {
		t.Fatal(err)
	}
	if effectiveGeminiPath(dir) != exe {
		t.Fatal("managed CLI was not restored")
	}
	runCodexUpdateCommand = func(context.Context, string, ...string) error { return errors.New("network error") }
	if err := c.installCLI(context.Background(), protocol.CLIInstallCommand{Kind: "gemini", Version: "0.64.0"}); err == nil {
		t.Fatal("failed install accepted")
	}
	if effectiveGeminiPath(dir) != exe || c.gemini.Command() != exe {
		t.Fatal("failed install changed active CLI")
	}
	if err := c.installCLI(context.Background(), protocol.CLIInstallCommand{Kind: "evil", Version: "0.65.0"}); err == nil {
		t.Fatal("unknown package accepted")
	}
}
