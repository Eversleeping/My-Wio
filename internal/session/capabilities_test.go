package session

import (
	"encoding/json"
	"fmt"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/capabilities"
	"testing"
)

func TestRefWireCompatibility(t *testing.T) {
	for _, engine := range []string{Codex, Gemini, Claude, "unknown", ""} {
		r := Ref{Engine: engine, ID: "wio-id"}
		raw, err := json.Marshal(r)
		if err != nil || string(raw) != fmt.Sprintf(`{"engine":%q,"id":"wio-id"}`, engine) {
			t.Fatalf("wire: %s %v", raw, err)
		}
		var decoded Ref
		if err := json.Unmarshal(raw, &decoded); err != nil || decoded != r || r.Key() != engine+":wio-id" {
			t.Fatalf("roundtrip: %+v %v", decoded, err)
		}
		want := engine == Codex || engine == Gemini || engine == Claude
		if r.Valid() != want {
			t.Fatalf("valid %s", engine)
		}
		r.ID = ""
		if r.Valid() {
			t.Fatal("empty id accepted")
		}
	}
}

func TestSessionCapabilitiesRequireMinimum(t *testing.T) {
	all := capabilities.AgentDeclaration("0.66.0", "2.1.286")
	for _, engine := range []string{Codex, Gemini, Claude} {
		for _, version := range []string{"", "invalid", "0.3.9", "0.3.25"} {
			if got := ResolveCapabilities(engine, version, "9.9.9", all); got != (Capabilities{}) {
				t.Fatalf("minimum bypass %s %q: %+v", engine, version, got)
			}
		}
		without := ResolveCapabilities(engine, buildinfo.MinimumAgentVersion, "9.9.9")
		if without.Steer || without.Collaboration || without.ForkMessages {
			t.Fatalf("version inference remains: %+v", without)
		}
	}
}

func TestCapabilitiesEngineShape(t *testing.T) {
	expected := map[string]Capabilities{
		Codex:     {Turns: true, Images: true, Steer: true, SteerImages: true, Rewrite: true, Fork: true, ForkMessages: true, ForkWorktree: true, Questions: true, Approvals: true, Goal: true, Compact: true, Skills: true, Effort: true, Yolo: true, Collaboration: true},
		Gemini:    {Turns: true, Images: true, Steer: true, SteerImages: true, Rewrite: true, Fork: true, ForkMessages: true, ForkWorktree: true, Questions: true, Approvals: true, Goal: true, Plan: true, Skills: true, SkillsManage: true, Thinking: true, Yolo: true, ConfirmStopped: true, Collaboration: true},
		Claude:    {Turns: true, Images: true, Steer: true, SteerImages: true, Rewrite: true, Fork: true, ForkMessages: true, ForkWorktree: true, Questions: true, Approvals: true, Compact: true, Skills: true, Effort: true, Yolo: true, ConfirmStopped: true, Collaboration: true},
		"unknown": {},
	}
	for engine, want := range expected {
		cli := "0.66.0"
		if engine == Claude {
			cli = "2.1.286"
		}
		if got := ResolveCapabilities(engine, "1.0.0", cli, capabilities.AgentDeclaration("0.66.0", "2.1.286")); got != want {
			t.Fatalf("%s: %+v want %+v", engine, got, want)
		}
	}
}
