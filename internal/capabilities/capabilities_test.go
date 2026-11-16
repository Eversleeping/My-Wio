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
	if len(names) != 32 || len(gates) != 32 {
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

func TestAgentDeclarationCLIRequirements(t *testing.T) {
	for _, gemini := range []string{"", "invalid", "0.65.99", "0.66.0", "v0.66.1+build"} {
		for _, claude := range []string{"", "invalid", "2.1.285", "2.1.286", "2.1.286 (Claude Code)", "v2.1.287+build", " 2.1.286"} {
			declared := AgentDeclaration(gemini, claude)
			expected := 24
			goodGemini := gemini == "0.66.0" || gemini == "v0.66.1+build"
			goodClaude := claude == "2.1.286" || claude == "2.1.286 (Claude Code)" || claude == "v2.1.287+build"
			if goodGemini {
				expected += 6
			}
			if goodClaude {
				expected += 2
			}
			if len(declared) != expected {
				t.Fatalf("Gemini=%q Claude=%q: %v", gemini, claude, declared)
			}
			for _, name := range []string{protocol.CapabilityGeminiSteer, protocol.CapabilityGeminiSkills, protocol.CapabilityGeminiPlan, protocol.CapabilityGeminiGoal, protocol.CapabilityGeminiSteerImages, protocol.CapabilityGeminiSkillsManage} {
				if Supports(name, buildinfo.MinimumAgentVersion, declared) != goodGemini {
					t.Fatalf("Gemini CLI gate %s: %q", name, gemini)
				}
			}
			for _, name := range []string{protocol.CapabilityClaudeSteer, protocol.CapabilityClaudeQuestions} {
				if Supports(name, buildinfo.MinimumAgentVersion, declared) != goodClaude {
					t.Fatalf("Claude CLI gate %s: %q", name, claude)
				}
			}
		}
	}
}

func TestDeclaredSetAndMinimumAreAuthoritative(t *testing.T) {
	all := AgentDeclaration("0.66.0", "2.1.286")
	for _, gate := range gates {
		for _, declared := range []Declaration{nil, {}, {"future.unknown"}} {
			if Supports(gate.name, "9.9.9", declared) {
				t.Fatalf("missing declaration grants %s", gate.name)
			}
		}
		if !Supports(gate.name, buildinfo.MinimumAgentVersion, Declaration{gate.name}) {
			t.Fatalf("declaration ignored %s", gate.name)
		}
		for _, version := range []string{"", "invalid", "0.1.0", "0.2.0", "0.3.9", "0.3.25"} {
			want := gate.name == protocol.CapabilityAgentSelfUpdate && (version == "0.2.0" || version == "0.3.9" || version == "0.3.25")
			if got := Supports(gate.name, version, all); got != want {
				t.Fatalf("minimum bypassed: %s %q: %v", gate.name, version, got)
			}
			if got := Supports(gate.name, version, nil); got != want {
				t.Fatalf("legacy rescue: %s %q: %v", gate.name, version, got)
			}
		}
	}
	if Supports("future.unknown", "9.9.9", Declaration{"future.unknown"}) {
		t.Fatal("unknown capability granted")
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
