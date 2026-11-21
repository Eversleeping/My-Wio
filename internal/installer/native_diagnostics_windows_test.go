//go:build windows && platformintegration

package installer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/hostplatform"
)

func nativeServiceDiagnostics(t *testing.T, l hostplatform.Layout) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, name := range []string{filepath.Dir(l.Binary), l.Binary, l.State, l.Config} {
		output, err := portableCommand(ctx, "icacls.exe", name)
		t.Logf("Native ACL %s: %s (%v)", name, output, err)
	}
	output, err := portableCommand(ctx, "sc.exe", "qc", "WioAgent")
	t.Logf("Service definition: %s (%v)", output, err)
	output, err = portableCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `Get-WinEvent -FilterHashtable @{LogName='System';ProviderName='Service Control Manager';StartTime=(Get-Date).AddMinutes(-10)} -ErrorAction SilentlyContinue | Where-Object {$_.Message -like '*Wio*'} | Select-Object -First 10 -ExpandProperty Message`)
	t.Logf("Service events: %s (%v)", output, err)
}

func nativeDependencyProbe(t *testing.T, l hostplatform.Layout, binary []byte) (string, bool) {
	directory := filepath.Join(filepath.Dir(l.Binary), "native-dependency")
	if err := managedDirectory(directory); err != nil {
		t.Fatal(err)
	}
	// Simulate an archive extracted in an administrator-only temp directory and
	// moved onto the same volume. The service must inherit RX before probing it.
	if err := hostplatform.ProtectInstaller(directory); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "dependency.exe")
	if err := atomicPortableWrite(file, binary, 0755); err != nil {
		t.Fatal(err)
	}
	return file, true
}
