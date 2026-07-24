package store

import (
	"context"
	"testing"
)

func TestDefaultControlPlaneCredentialProfilesUsesExistingProfilesOnce(t *testing.T) {
	database := testStore(t)
	ctx := context.Background()
	if _, err := database.EnsureControlPlaneServer(ctx, "control-host", "control-token"); err != nil {
		t.Fatal(err)
	}
	codex, err := database.SaveCredentialProfile(ctx, CredentialProfile{Kind: "codex", Name: "Default Codex", Endpoint: "https://api.example.com/v1", Model: "gpt-5.6-sol"}, "v1:codex")
	if err != nil {
		t.Fatal(err)
	}
	git, err := database.SaveCredentialProfile(ctx, CredentialProfile{Kind: "git", Name: "Default Git", Endpoint: "https://gitee.com", Username: "wio", CommitName: "Wio", CommitEmail: "wio@example.com"}, "v1:git")
	if err != nil {
		t.Fatal(err)
	}
	regular := enrollProjectImportTestServer(t, database, "regular-default")
	if err := database.SetServerCredentialProfiles(ctx, regular.ID, codex.ID, git.ID); err != nil {
		t.Fatal(err)
	}
	selectedCodex, selectedGit, initialize, err := database.DefaultControlPlaneCredentialProfiles(ctx)
	if err != nil || !initialize || selectedCodex.ID != codex.ID || selectedGit == nil || selectedGit.ID != git.ID {
		t.Fatalf("unexpected default profiles: codex=%#v git=%#v initialize=%v err=%v", selectedCodex, selectedGit, initialize, err)
	}
	if err := database.SetServerCredentialProfiles(ctx, ControlPlaneServerID, codex.ID, ""); err != nil {
		t.Fatal(err)
	}
	_, _, initialize, err = database.DefaultControlPlaneCredentialProfiles(ctx)
	if err != nil || initialize {
		t.Fatalf("explicit control-plane binding should not be replaced: initialize=%v err=%v", initialize, err)
	}
}

func TestDefaultControlPlaneCredentialProfilesRequiresCodex(t *testing.T) {
	database := testStore(t)
	ctx := context.Background()
	if _, err := database.EnsureControlPlaneServer(ctx, "control-host", "control-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SaveCredentialProfile(ctx, CredentialProfile{Kind: "git", Name: "Git only", Endpoint: "https://gitee.com", Username: "wio", CommitName: "Wio", CommitEmail: "wio@example.com"}, "v1:git"); err != nil {
		t.Fatal(err)
	}
	_, _, initialize, err := database.DefaultControlPlaneCredentialProfiles(ctx)
	if err != nil || initialize {
		t.Fatalf("expected no default without a Codex profile: initialize=%v err=%v", initialize, err)
	}
}
