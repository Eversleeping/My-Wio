package main

import (
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
