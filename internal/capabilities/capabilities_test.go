package capabilities

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestGateRegistryCoversProtocolNames(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../protocol/capabilities.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "Capability") {
				continue
			}
			value, err := strconv.Unquote(spec.Values[i].(*ast.BasicLit).Value)
			if err != nil {
				t.Fatal(err)
			}
			names[value] = true
		}
		return false
	})
	if len(names) != 31 || len(gates) != 31 {
		t.Fatalf("protocol=%d gates=%d", len(names), len(gates))
	}
	for _, gate := range gates {
		if !names[gate.name] {
			t.Fatalf("unknown/duplicate gate %q", gate.name)
		}
		delete(names, gate.name)
	}
	if len(names) != 0 {
		t.Fatalf("missing gates %v", names)
	}
}

func TestAgentDeclarationsMatchLegacyInference(t *testing.T) {
	for _, gemini := range []string{"", "invalid", "0.65.99", "0.66.0", "v0.66.1+build"} {
		for _, claude := range []string{"", "invalid", "2.1.285", "2.1.286", "2.1.286 (Claude Code)", "v2.1.287+build"} {
			declared := AgentDeclaration(gemini, claude)
			count := 0
			for _, gate := range gates {
				cli := gemini
				if strings.HasPrefix(gate.name, "claude.") {
					cli = claude
				}
				want := gate.legacy(buildinfo.Version, cli)
				if want {
					count++
				}
				if got := Supports(gate.name, "invalid", "invalid", declared); got != want {
					t.Fatalf("%s Gemini=%q Claude=%q: declared=%v legacy=%v", gate.name, gemini, claude, got, want)
				}
				if Supports(gate.name, buildinfo.Version, cli, nil) != want {
					t.Fatalf("legacy fallback %s", gate.name)
				}
			}
			if len(declared) != count {
				t.Fatalf("unexpected/duplicate names %v", declared)
			}
		}
	}
	if len(AgentDeclaration("", "")) != 23 || len(AgentDeclaration("0.66.0", "2.1.286")) != 31 {
		t.Fatal("fixed/conditional capability count changed")
	}
}

func TestDeclaredSetIsAuthoritative(t *testing.T) {
	for _, gate := range gates {
		for _, declared := range []Declaration{{}, {"future.unknown"}} {
			if Supports(gate.name, "9.9.9", "9.9.9", declared) {
				t.Fatalf("missing declaration grants %s", gate.name)
			}
		}
		if !Supports(gate.name, "", "", Declaration{gate.name}) {
			t.Fatalf("version overrides declaration %s", gate.name)
		}
	}
	if Supports("future.unknown", "9.9.9", "9.9.9", Declaration{"future.unknown"}) {
		t.Fatal("unknown capability granted")
	}
	if !Supports(protocol.CapabilityAgentSelfUpdate, "0.2.0", "", nil) {
		t.Fatal("legacy self update lost")
	}
}

func TestDeclarationScanPreservesAbsentAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		raw  any
		want Declaration
	}{
		{nil, nil}, {"", nil}, {[]byte{}, nil}, {"null", nil}, {"[]", Declaration{}},
		{`["codex.steer","future.unknown"]`, Declaration{"codex.steer", "future.unknown"}},
		{[]byte(`["operation.interrupt"]`), Declaration{"operation.interrupt"}},
	} {
		declared := Declaration{"old"}
		if err := declared.Scan(tc.raw); err != nil || !reflect.DeepEqual(declared, tc.want) {
			t.Fatalf("%v -> %v want %v: %v", tc.raw, declared, tc.want, err)
		}
	}
	for _, raw := range []any{12, "{}", "[broken"} {
		var declared Declaration
		if err := declared.Scan(raw); err == nil {
			t.Fatalf("accepted invalid declaration %v", raw)
		}
	}
}
