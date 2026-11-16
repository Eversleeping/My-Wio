package deployer

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestDeploymentChildFiltersControlPlaneAndKeepsVaultEnvironment(t *testing.T) {
	for _, name := range []string{"WIO_MASTER_KEY", "WIO_DATABASE_URL", "WIO_SETUP_TOKEN", "wio_private_setting"} {
		t.Setenv(name, "parent-only")
	}
	binary, _ := os.Executable()
	output, err := runIn(context.Background(), t.TempDir(), []string{"DEPLOY_TEST_CHILD=1", "DEPLOY_VAULT_TOKEN=test-secret"}, binary, "-test.run=^TestDeploymentEnvironmentProcess$")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal([]byte(output), &names); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasPrefix(strings.ToUpper(name), "WIO_") {
			t.Fatalf("inherited Wio setting reached deployment command: %s", name)
		}
	}
	if !slices.Contains(names, "DEPLOY_VAULT_TOKEN") {
		t.Fatal("explicit Vault environment was lost")
	}
}

func TestDeploymentEnvironmentProcess(t *testing.T) {
	if os.Getenv("DEPLOY_TEST_CHILD") != "1" {
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
