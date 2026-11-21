package installer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPortableAtomicWriteRejectsLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires developer mode")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "credential")
	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if err := atomicPortableWrite(target, []byte("secret"), 0600); err == nil {
		t.Fatal("credential link accepted")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "unchanged" {
		t.Fatal("credential destination overwritten")
	}
}
func TestPortableScriptsAndPowerShellQuoting(t *testing.T) {
	for _, part := range []string{"_wio-agent", "UniqueID", "NFSHomeDirectory", "UserShell"} {
		if !strings.Contains(macAccountScript, part) {
			t.Fatal(part)
		}
	}
	for _, part := range []string{"UserName", "KeepAlive", "RunAtLoad", "CODEX_HOME", "Umask"} {
		if !strings.Contains(macServicePlist, part) {
			t.Fatal(part)
		}
	}
	if !strings.Contains(macNodeScript, "shasum -a 256 -c") || !strings.Contains(windowsNodeScript, "Get-FileHash") {
		t.Fatal("dependency integrity check missing")
	}
	if PowerShellQuote("a'$env:SECRET") != "'a''$env:SECRET'" {
		t.Fatal("PowerShell expanded a literal")
	}
	other := "windows"
	if runtime.GOOS == "windows" {
		other = "darwin"
	}
	if _, err := InstallLocal(context.Background(), InstallRequest{OS: other}); err != ErrUnsupportedPlatform {
		t.Fatal("foreign-platform plan accepted")
	}
}

func TestManagedCLISelectionSurvivesFailedRepair(t *testing.T) {
	state := t.TempDir()
	for _, kind := range []string{"codex", "gemini", "claude"} {
		if err := os.MkdirAll(filepath.Join(state, kind), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, kind, "current-version"), []byte("1.0.0"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	warnings, err := installCLIs(context.Background(), state, CLIVersions{Codex: "2.0.0", Gemini: "2.0.0", Claude: "2.0.0"}, func(_ context.Context, name string, args ...string) (string, error) {
		if name == "npm" {
			return "", nil
		}
		return "wrong-version", nil
	})
	if err != nil || len(warnings) != 3 {
		t.Fatalf("verification warnings=%v %v", warnings, err)
	}
	for _, kind := range []string{"codex", "gemini", "claude"} {
		raw, err := os.ReadFile(filepath.Join(state, kind, "current-version"))
		if err != nil || string(raw) != "1.0.0" {
			t.Fatal("failed CLI replaced selection")
		}
	}
}

type windowsInspectionHost struct{}

func (windowsInspectionHost) execute(_ string, _ io.Reader, out io.Writer) error {
	_, err := io.WriteString(out, `{"architecture":"x64","admin":true}`)
	return err
}
func TestWindowsInspectionNormalizesDotNetArchitecture(t *testing.T) {
	probe, err := inspectWindows(windowsInspectionHost{}, "windows-host")
	if err != nil || probe.Architecture != "amd64" || probe.OS != "windows" || !probe.SudoReady {
		t.Fatalf("Windows OpenSSH platform: %+v %v", probe, err)
	}
	asset, err := platformAgentAsset(probe.OS, probe.Architecture)
	if err != nil || asset != "wio-agent-windows-amd64.exe" {
		t.Fatalf("Windows OpenSSH asset: %q %v", asset, err)
	}
}
