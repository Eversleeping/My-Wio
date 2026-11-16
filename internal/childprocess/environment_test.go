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
	got := mergeEnvironment(parent, overrides)
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
