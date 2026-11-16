package childprocess

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestEnvironmentFiltersInheritedWioAndKeepsExplicitSettings(t *testing.T) {
	parent := []string{"PATH=/bin", "HOME=/home/test", "WIO_MASTER_KEY=parent-secret", "wio_database_url=parent-database", "WIO_CODEX_API_KEY=parent-key", "ANTHROPIC_API_KEY=parent-key", "http_proxy=parent-proxy"}
	overrides := []string{"WIO_CODEX_API_KEY=selected-key", "ANTHROPIC_API_KEY=", "HTTP_PROXY=selected-proxy"}
	got := mergeEnvironment(parent, overrides, Claude, nil)
	want := []string{"PATH=/bin", "HOME=/home/test", "WIO_CODEX_API_KEY=selected-key", "ANTHROPIC_API_KEY=", "HTTP_PROXY=selected-proxy"}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %v, want %v", got, want)
	}
	if parent[2] != "WIO_MASTER_KEY=parent-secret" || overrides[0] != "WIO_CODEX_API_KEY=selected-key" {
		t.Fatal("environment construction mutated its inputs")
	}
}

func TestCommandContextDoesNotPassControlPlaneEnvironment(t *testing.T) {
	for _, name := range []string{"WIO_MASTER_KEY", "WIO_DATABASE_URL", "WIO_SETUP_TOKEN", "wio_private_setting"} {
		t.Setenv(name, "parent-only")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := CommandContext(context.Background(), binary, "-test.run=^TestChildEnvironmentProcess$")
	cmd.Env = append(cmd.Env, "CHILDPROCESS_TEST_CHILD=1")
	output, err := Output(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal(output, &names); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasPrefix(strings.ToUpper(name), "WIO_") {
			t.Fatalf("inherited Wio setting reached helper: %s", name)
		}
	}
	if os.Getenv("WIO_MASTER_KEY") != "parent-only" {
		t.Fatal("child construction changed the parent environment")
	}
}

func TestChildEnvironmentProcess(t *testing.T) {
	if os.Getenv("CHILDPROCESS_TEST_CHILD") != "1" {
		return
	}
	var names []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	_ = json.NewEncoder(os.Stdout).Encode(names)
	os.Exit(0)
}

func TestCommandEnvironmentScopes(t *testing.T) {
	variables := []string{"WIO_MASTER_KEY", "UNAPPROVED_TOKEN", "PROJECT_TOOLCHAIN_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "AWS_ACCESS_KEY_ID", "GOOGLE_CLOUD_PROJECT", "DOCKER_AUTH_CONFIG", "npm_config_registry", "GIT_AUTHOR_NAME", "SSH_AUTH_SOCK", "http_proxy", "NODE_EXTRA_CA_CERTS", "LC_MESSAGES"}
	for _, name := range variables {
		t.Setenv(name, "synthetic-only")
	}
	cases := []struct {
		name  string
		scope Scope
		own   []string
	}{
		{"runtime", Runtime, nil},
		{"git", Git, []string{"GIT_AUTHOR_NAME", "SSH_AUTH_SOCK"}},
		{"codex", Codex, []string{"OPENAI_API_KEY", "GIT_AUTHOR_NAME", "SSH_AUTH_SOCK"}},
		{"claude", Claude, []string{"ANTHROPIC_API_KEY", "AWS_ACCESS_KEY_ID", "GOOGLE_CLOUD_PROJECT", "GIT_AUTHOR_NAME", "SSH_AUTH_SOCK"}},
		{"gemini", Gemini, []string{"GEMINI_API_KEY", "GOOGLE_CLOUD_PROJECT", "GIT_AUTHOR_NAME", "SSH_AUTH_SOCK"}},
		{"deployment", Deployment, []string{"DOCKER_AUTH_CONFIG", "npm_config_registry", "GIT_AUTHOR_NAME", "SSH_AUTH_SOCK"}},
		{"installation", Installation, []string{"DOCKER_AUTH_CONFIG", "npm_config_registry"}},
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithScope(WithExtraEnvironment(context.Background(), []string{"PROJECT_TOOLCHAIN_TOKEN"}), tc.scope)
			cmd := CommandContext(ctx, binary, "-test.run=^TestChildEnvironmentProcess$")
			cmd.Env = append(cmd.Env, "CHILDPROCESS_TEST_CHILD=1")
			output, err := Output(cmd)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			if err := json.Unmarshal(output, &names); err != nil {
				t.Fatal(err)
			}
			for _, name := range variables {
				want := slices.Contains(tc.own, name) || slices.Contains([]string{"PROJECT_TOOLCHAIN_TOKEN", "http_proxy", "NODE_EXTRA_CA_CERTS", "LC_MESSAGES"}, name)
				if slices.Contains(names, name) != want {
					t.Errorf("%s inheritance in %s scope: want %v", name, tc.name, want)
				}
			}
		})
	}
}

func TestExtraEnvironmentNamesAreCopiedAndValuesReadAtCommandConstruction(t *testing.T) {
	t.Setenv("CUSTOM_TOOLCHAIN", "first")
	t.Setenv("REPLACED_TOOLCHAIN", "unapproved")
	names := []string{"CUSTOM_TOOLCHAIN"}
	ctx := WithExtraEnvironment(context.Background(), names)
	names[0] = "REPLACED_TOOLCHAIN"
	t.Setenv("CUSTOM_TOOLCHAIN", "second")
	cmd := CommandContext(ctx, "unused")
	if !slices.Contains(cmd.Env, "CUSTOM_TOOLCHAIN=second") || slices.Contains(cmd.Env, "REPLACED_TOOLCHAIN=unapproved") {
		t.Fatal("extra environment retained a value or caller-owned names slice")
	}
	if os.Getenv("CUSTOM_TOOLCHAIN") != "second" {
		t.Fatal("environment construction modified the parent")
	}
}

func TestExtraEnvironmentRejectsUnsafeNamesWithoutExposingAssignments(t *testing.T) {
	if err := ValidateExtraEnvironment([]string{"GOPATH", "custom_token", "CUSTOM_TOKEN2"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "WIO_MASTER_KEY", "wio_database_url", "*", "KEY=synthetic-secret", " KEY", "KEY\n", "1KEY", "NÖDE"} {
		if err := ValidateExtraEnvironment([]string{name}); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("unsafe extra environment entry was accepted or included in an error")
		}
	}
	ctx := WithExtraEnvironment(context.Background(), []string{"WIO_MASTER_KEY"})
	t.Setenv("WIO_MASTER_KEY", "parent-only")
	if slices.Contains(Environment(ctx), "WIO_MASTER_KEY=parent-only") {
		t.Fatal("extra environment bypassed Wio's inheritance exclusion")
	}
}
