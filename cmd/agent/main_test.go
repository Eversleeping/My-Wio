package main

import (
	"fmt"
	"github.com/wio-platform/wio/internal/agent"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrollmentTokenSources(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("  file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		flagToken string
		tokenFile string
		stdin     string
		want      string
	}{
		{name: "command line flag stays compatible", flagToken: "flag-token", want: "flag-token"},
		{name: "standard input", tokenFile: "-", stdin: "stdin-token\n", want: "stdin-token"},
		{name: "file", tokenFile: file, want: "file-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := enrollmentToken(test.flagToken, test.tokenFile, strings.NewReader(test.stdin))
			if err != nil || got != test.want {
				t.Fatalf("enrollmentToken() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestEnrollmentTokenRejectsInvalidInput(t *testing.T) {
	directory := t.TempDir()
	empty := filepath.Join(directory, "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		flagToken string
		tokenFile string
		stdin     string
	}{
		{name: "both sources", flagToken: "flag-token", tokenFile: "-", stdin: "stdin-token"},
		{name: "no source", flagToken: ""},
		{name: "empty standard input", tokenFile: "-", stdin: "\n"},
		{name: "empty file", tokenFile: empty},
		{name: "missing file", tokenFile: filepath.Join(directory, "missing")},
		{name: "oversized input", tokenFile: "-", stdin: strings.Repeat("x", maximumEnrollmentTokenSize+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if token, err := enrollmentToken(test.flagToken, test.tokenFile, strings.NewReader(test.stdin)); err == nil {
				t.Fatalf("invalid enrollment token input accepted: %q", token)
			}
		})
	}
}

func TestEnrollRequiresATokenSource(t *testing.T) {
	err := run(nil, []string{"enroll", "--url", "https://wio.example.com"})
	if err == nil || !strings.Contains(err.Error(), "--token-file") {
		t.Fatalf("missing token was not rejected with guidance: %v", err)
	}
}

func TestRepairEnrollmentPreservesAccessConfiguration(t *testing.T) {
	for _, allowSudo := range []bool{false, true} {
		t.Run(fmt.Sprint(allowSudo), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"server_id":"server-1","agent_token":"new-synthetic-token","scan_roots":["/srv"]}`))
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "config.json")
			previous := agent.Config{ControlURL: server.URL, ServerID: "server-1", AgentToken: "old-synthetic-token", AllowSudo: allowSudo}
			if err := agent.SaveConfig(path, previous); err != nil {
				t.Fatal(err)
			}
			// Conflicting enrollment defaults must not override the installed policy.
			args := []string{"enroll", "--url", server.URL, "--token", "synthetic-enrollment", "--config", path, "--preserve-access-settings", "--allow-sudo=" + fmt.Sprint(!allowSudo)}
			if err := run(nil, args); err != nil {
				t.Fatal(err)
			}
			next, err := agent.LoadConfig(path)
			if err != nil || next.AllowSudo != allowSudo {
				t.Fatalf("installed access policy lost during repair: %v", err)
			}
			if next.AgentToken != "new-synthetic-token" {
				t.Fatal("repair did not renew the enrollment")
			}
		})
	}
}
