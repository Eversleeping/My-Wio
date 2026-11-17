package sshbootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccessPatchPreservesIdentityAndAdvancedConfiguration(t *testing.T) {
	raw := []byte(`{"server_id":"server-1","agent_token":"synthetic-secret","unknown":{"keep":true},"session_isolation":{"enabled":false,"uid":1234,"runtime_paths":["/custom"]}}`)
	current, _, err := decodeAccess(raw, "server-1")
	if err != nil {
		t.Fatal(err)
	}
	current.SessionIsolation = true
	next, err := patchAccess(raw, "server-1", current)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(next, &fields); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fields["agent_token"], []byte(`"synthetic-secret"`)) || !strings.Contains(string(fields["session_isolation"]), "1234") || !strings.Contains(string(fields["session_isolation"]), "/custom") || fields["unknown"] == nil {
		t.Fatalf("configuration not preserved")
	}
	safe, _, _ := decodeAccess(next, "server-1")
	encoded, _ := json.Marshal(safe)
	if bytes.Contains(encoded, []byte("synthetic-secret")) || !safe.SessionIsolation {
		t.Fatal("unsafe or incorrect response")
	}
	if _, _, err = decodeAccess(raw, "server-2"); !errors.Is(err, ErrServerMismatch) {
		t.Fatal("wrong identity accepted")
	}
	current.Revision = "stale"
	if _, err = patchAccess(raw, "server-1", current); !errors.Is(err, ErrAccessChanged) {
		t.Fatal("stale update accepted")
	}
	current.AllowSudo = true
	if _, err = patchAccess(raw, "server-1", current); !errors.Is(err, ErrInvalidTarget) {
		t.Fatal("conflicting options accepted")
	}
}

func TestAccessScriptAppliesAndRollsBack(t *testing.T) {
	for _, allowSudo := range []bool{true, false} {
		for _, scenario := range []string{"apply", "restart-fails", "stale"} {
			t.Run(fmt.Sprintf("%s/sudo=%t", scenario, allowSudo), func(t *testing.T) {
				root := t.TempDir()
				etc := filepath.Join(root, "etc")
				backup := filepath.Join(etc, "wio-agent", "access.test")
				bin := filepath.Join(root, "bin")
				for _, dir := range []string{backup, bin, filepath.Join(etc, "sudoers.d"), filepath.Join(etc, "systemd/system/wio-agent.service.d"), filepath.Join(root, "lock")} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				original := []byte(fmt.Sprintf(`{"server_id":"server-1","allow_sudo":%t}`, !allowSudo))
				current, _, _ := decodeAccess(original, "server-1")
				current.AllowSudo = allowSudo
				next, err := patchAccess(original, "server-1", current)
				if err != nil {
					t.Fatal(err)
				}
				config := filepath.Join(etc, "wio-agent/config.json")
				dropin := filepath.Join(etc, "systemd/system/wio-agent.service.d/90-wio-access.conf")
				for name, data := range map[string][]byte{config: original, filepath.Join(backup, "next.json"): next, filepath.Join(etc, "systemd/system/wio-agent.service"): []byte("[Service]\n"), dropin: []byte("previous override")} {
					if err = os.WriteFile(name, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if !allowSudo {
					if err = os.WriteFile(filepath.Join(etc, "sudoers.d/wio-agent"), []byte("previous sudo policy"), 0440); err != nil {
						t.Fatal(err)
					}
				}
				fakeSystemctl := `#!/bin/sh
if [ "$1" = restart ] && [ "$WIO_ACCESS_SCENARIO" = restart-fails ] && [ ! -f "$WIO_ACCESS_MARKER" ]; then touch "$WIO_ACCESS_MARKER"; exit 1; fi
exit 0
`
				for name, data := range map[string]string{"systemctl": fakeSystemctl, "sudo": "#!/bin/sh\nexit 0\n", "visudo": "#!/bin/sh\nexit 0\n"} {
					if err = os.WriteFile(filepath.Join(bin, name), []byte(data), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "stale" {
					current.Revision = strings.Repeat("0", 64)
				}
				script := accessApplyScript("/etc/wio-agent/access.test", current)
				script = strings.ReplaceAll(script, "/etc/", etc+"/")
				script = strings.ReplaceAll(script, "/run/lock", filepath.Join(root, "lock"))
				command := exec.Command("sh", "-c", script)
				command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "WIO_ACCESS_SCENARIO="+scenario, "WIO_ACCESS_MARKER="+filepath.Join(root, "failed"))
				output, err := command.CombinedOutput()
				if (scenario == "apply") != (err == nil) {
					t.Fatalf("unexpected apply result: %v %s", err, output)
				}
				got, _ := os.ReadFile(config)
				override, _ := os.ReadFile(dropin)
				if scenario == "apply" {
					if !bytes.Equal(got, next) || !bytes.Contains(override, []byte("NoNewPrivileges="+fmt.Sprint(!allowSudo))) {
						t.Fatalf("settings not installed: %s", output)
					}

				} else {
					if !bytes.Equal(got, original) || string(override) != "previous override" {
						t.Fatalf("previous configuration not restored: %s", output)
					}

				}
				wantSudo := allowSudo
				if scenario != "apply" {
					wantSudo = !allowSudo
				}
				policy, statErr := os.ReadFile(filepath.Join(etc, "sudoers.d/wio-agent"))
				if wantSudo && statErr != nil {
					t.Fatalf("sudo policy missing: %v", statErr)
				}
				if !wantSudo && !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("sudo policy unexpectedly remains")
				}
				if wantSudo && scenario != "apply" && string(policy) != "previous sudo policy" {
					t.Fatal("previous sudo policy not restored")
				}
			})
		}
	}
}
