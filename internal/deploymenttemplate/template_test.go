package deploymenttemplate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectNodeViteWebsocket(t *testing.T) {
	root := t.TempDir()
	writeTemplateFixture(t, root, `{"scripts":{"build":"vite build","server":"tsx server/index.ts"},"devDependencies":{"vite":"^8.0.0"}}`, true)

	template, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if template.ID != NodeViteWebsocket || template.ContainerPort != 80 {
		t.Fatalf("unexpected template: %#v", template)
	}
}

func TestGenerateNodeViteWebsocket(t *testing.T) {
	root := t.TempDir()
	writeTemplateFixture(t, root, `{"scripts":{"build":"vite build","server":"tsx server/index.ts"},"devDependencies":{"vite":"^8.0.0"}}`, true)

	result, err := Generate(GenerateOptions{Root: root, ComposeFile: "compose.yaml", BuildMode: "build", PublicPort: 18432})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Generated || result.Template.ID != NodeViteWebsocket {
		t.Fatalf("unexpected generation result: %#v", result)
	}
	compose, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	composeText := string(compose)
	for _, expected := range []string{"services:", "api:", "web:", "${APP_PORT:?set APP_PORT}:80", "PORT: 8787"} {
		if !strings.Contains(composeText, expected) {
			t.Fatalf("generated Compose is missing %q: %s", expected, composeText)
		}
	}
	if strings.Contains(composeText, "8080:8080") {
		t.Fatalf("generated Compose hard-coded a host port: %s", composeText)
	}
	for _, file := range []string{GeneratedDockerfile, GeneratedNginxConfig, ".dockerignore"} {
		if _, err := os.Stat(filepath.Join(root, file)); err != nil {
			t.Fatalf("generated file %s is missing: %v", file, err)
		}
	}
}

func TestGenerateDomainModeDoesNotPublishHostPort(t *testing.T) {
	root := t.TempDir()
	writeTemplateFixture(t, root, `{"scripts":{"build":"vite build","server":"tsx server/index.ts"},"devDependencies":{"vite":"^8.0.0"}}`, true)

	if _, err := Generate(GenerateOptions{Root: root, ComposeFile: "compose.yaml", BuildMode: "build"}); err != nil {
		t.Fatal(err)
	}
	compose, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(compose), "ports:") {
		t.Fatalf("domain-mode template unexpectedly published a host port: %s", compose)
	}
}

func TestDetectTankWarFixture(t *testing.T) {
	root := os.Getenv("WIO_TANK_WAR_FIXTURE")
	if root == "" {
		t.Skip("WIO_TANK_WAR_FIXTURE is not set")
	}
	template, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if template.ID != NodeViteWebsocket {
		t.Fatalf("tank-war matched %s, want %s", template.ID, NodeViteWebsocket)
	}
}

func TestGenerateTankWarFixture(t *testing.T) {
	source := os.Getenv("WIO_TANK_WAR_GENERATE_FIXTURE")
	if source == "" {
		t.Skip("WIO_TANK_WAR_GENERATE_FIXTURE is not set")
	}
	root := t.TempDir()
	for _, relative := range []string{"package.json", "pnpm-lock.yaml", filepath.Join("server", "index.ts")} {
		data, err := os.ReadFile(filepath.Join(source, relative))
		if err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Generate(GenerateOptions{Root: root, ComposeFile: "compose.yaml", BuildMode: "build", PublicPort: 18987})
	if err != nil {
		t.Fatal(err)
	}
	if result.Template.ID != NodeViteWebsocket || !result.Generated {
		t.Fatalf("unexpected tank-war generation result: %#v", result)
	}
}

func TestGenerateRejectsUnsupportedProject(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"build":"custom-build"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(GenerateOptions{Root: root, ComposeFile: "compose.yaml", BuildMode: "build"}); err == nil {
		t.Fatal("unsupported project was accepted")
	}
}

func writeTemplateFixture(t *testing.T, root, manifest string, server bool) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if server {
		if err := os.MkdirAll(filepath.Join(root, "server"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "server", "index.ts"), []byte("export {};\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
