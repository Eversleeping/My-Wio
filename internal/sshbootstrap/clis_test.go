package sshbootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/codexcli"
)

func TestBootstrapResolvesLatestStableVersionsForBothCLIs(t *testing.T) {
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/codex":
			w.Write([]byte(`[{"tag_name":"rust-v0.165.0"},{"tag_name":"rust-v0.166.0-alpha.1","prerelease":true},{"tag_name":"rust-v0.164.0"}]`))
		case "/gemini":
			w.Write([]byte(`[{"tag_name":"v0.66.0"},{"tag_name":"v0.68.0","draft":true},{"tag_name":"v0.67.0-preview.1","prerelease":true},{"tag_name":"v0.65.0"}]`))
		default:
			t.Errorf("unexpected release source: %s", r.URL.Path)
		}
	}))
	defer releases.Close()
	service := New(t.TempDir())
	service.codexReleases = codexcli.NewReleaseChecker(releases.Client(), releases.URL+"/codex")
	service.geminiReleases = codexcli.NewTaggedReleaseChecker(releases.Client(), releases.URL+"/gemini", "v")
	versions, err := service.latestCLIVersions(context.Background())
	if err != nil || versions.codex != "0.165.0" || versions.gemini != "0.66.0" {
		t.Fatalf("bootstrap did not select both newest formal releases: %+v %v", versions, err)
	}
}

func TestBootstrapCannotSilentlyUsePinnedVersionWhenDiscoveryFails(t *testing.T) {
	for _, failing := range []string{"codex", "gemini"} {
		t.Run(failing, func(t *testing.T) {
			releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/"+failing {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Write([]byte(`[{"tag_name":"rust-v0.165.0"}]`))
			}))
			defer releases.Close()
			service := New(t.TempDir())
			service.codexReleases = codexcli.NewReleaseChecker(releases.Client(), releases.URL+"/codex")
			service.geminiReleases = codexcli.NewTaggedReleaseChecker(releases.Client(), releases.URL+"/gemini", "v")
			versions, err := service.latestCLIVersions(context.Background())
			if !errors.Is(err, ErrCLIReleasesUnavailable) || versions != (cliVersions{}) {
				t.Fatalf("discovery failure fell back to an old version: %+v %v", versions, err)
			}
		})
	}
}

func TestBootstrapInstallsAndActivatesVerifiedCLIsAsServiceAccount(t *testing.T) {
	for _, test := range []struct{ kind, version, packageName, reported string }{
		{"codex", "0.165.0", "@openai/codex", "codex-cli 0.165.0\n"},
		{"gemini", "0.66.0", "@google/gemini-cli", "0.66.0\n"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			for _, registry := range []string{officialNPMRegistry, mainlandNPMRegistry} {
				var commands []string
				path, err := installBootstrapCLI(test.kind, test.version, registry, func(command string) (string, error) {
					commands = append(commands, command)
					if !strings.HasPrefix(command, "su -s /bin/sh -c ") || !strings.HasSuffix(command, " wio-agent") {
						t.Fatalf("CLI setup did not use the service account: %s", command)
					}
					if strings.Contains(command, " --version") {
						return test.reported, nil
					}
					return "", nil
				})
				prefix := "/var/lib/wio-agent/" + test.kind + "/versions/" + test.version
				if err != nil || path != prefix+"/node_modules/.bin/"+test.kind || len(commands) != 3 {
					t.Fatalf("CLI installation/verification/activation failed: %q %v %v", path, commands, err)
				}
				for _, expected := range []string{test.packageName + "@" + test.version, prefix, registry} {
					if !strings.Contains(commands[0], expected) {
						t.Fatalf("installation used the wrong package, version or registry: %s", commands[0])
					}
				}
				for _, expected := range []string{"mktemp", "chmod 0600", "mv -f", "/var/lib/wio-agent/" + test.kind + "/current-version", test.version} {
					if !strings.Contains(commands[2], expected) {
						t.Fatalf("verified version was not published atomically: %s", commands[2])
					}
				}
			}
		})
	}
}

func TestBootstrapKeepsPreviousSelectionWhenCLIInstallationOrVerificationFails(t *testing.T) {
	for _, kind := range []string{"codex", "gemini"} {
		for _, failure := range []string{"install", "verification", "wrong_version", "activation"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				calls := 0
				path, err := installBootstrapCLI(kind, "0.165.0", officialNPMRegistry, func(command string) (string, error) {
					calls++
					if calls == 1 && failure == "install" || calls == 2 && failure == "verification" || calls == 3 && failure == "activation" {
						return "", errors.New("command failed")
					}
					if calls == 2 {
						if failure == "wrong_version" {
							return "0.144.4", nil
						}
						return "0.165.0", nil
					}
					return "", nil
				})
				if err == nil || path != "" {
					t.Fatalf("unverified executable was selected: %q %v", path, err)
				}
				if failure != "activation" && calls > 2 {
					t.Fatal("an unsuccessful install overwrote the previous CLI selection")
				}
			})
		}
	}
}

func TestBootstrapRejectsInvalidCLIPackagesAndVersionsBeforeExecuting(t *testing.T) {
	for _, test := range []struct{ kind, version string }{{"other", "1.2.3"}, {"codex", "latest"}, {"gemini", "1.2.3;echo invalid"}} {
		_, err := installBootstrapCLI(test.kind, test.version, officialNPMRegistry, func(string) (string, error) {
			t.Fatal("invalid CLI target reached the SSH executor")
			return "", nil
		})
		if !errors.Is(err, ErrInvalidTarget) {
			t.Fatalf("invalid CLI target accepted: %+v %v", test, err)
		}
	}
}

func TestBootstrapPublishesCLISelectionWithRealShell(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	for _, kind := range []string{"codex", "gemini"} {
		for _, reported := range []string{"0.165.0", "0.144.4"} {
			t.Run(kind+"/"+reported, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "CLI tools")
				bin := filepath.Join(root, kind, "versions", "0.165.0", "node_modules", ".bin")
				if err := os.MkdirAll(bin, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, kind), []byte("#!/bin/sh\nprintf '%s\\n' '"+reported+"'\n"), 0o750); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(root, kind, "current-version")
				if err := os.WriteFile(marker, []byte("0.144.4\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				calls := 0
				_, installErr := installBootstrapCLI(kind, "0.165.0", officialNPMRegistry, func(command string) (string, error) {
					calls++
					command = strings.ReplaceAll(command, "/var/lib/wio-agent", filepath.ToSlash(root))
					if calls == 1 {
						// Check install syntax without downloading packages or using su.
						output, err := exec.Command(shell, "-n", "-c", command).CombinedOutput()
						return string(output), err
					}
					// Execute verification and activation as the current test user.
					output, err := exec.Command(shell, "-c", "su() { shift 3; sh -c \"$1\"; }\n"+command).CombinedOutput()
					return string(output), err
				})
				if (installErr == nil) != (reported == "0.165.0") {
					t.Fatalf("unexpected CLI verification result: %v", installErr)
				}
				selected, err := os.ReadFile(marker)
				if err != nil || string(selected) != reported+"\n" {
					t.Fatalf("wrong version selection after verification: %q %v", selected, err)
				}
				leftovers, err := filepath.Glob(marker + ".*")
				if err != nil || len(leftovers) != 0 {
					t.Fatalf("activation left temporary files: %v %v", leftovers, err)
				}
			})
		}
	}
}
