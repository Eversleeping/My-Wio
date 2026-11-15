package session

import (
	"encoding/json"
	"fmt"
	"reflect"
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

func TestCapabilitiesVersionBoundaries(t *testing.T) {
	// Independent, explicit compatibility table: minimum patch in Agent 0.2.x,
	// and optional CLI minimum. This is also the source for the engine docs.
	gates := []struct {
		engine, field string
		patch         int
		cli           string
	}{
		{Codex, "Steer", 51, ""}, {Codex, "SteerImages", 51, ""}, {Codex, "ForkMessages", 59, ""},
		{Gemini, "Turns", 60, ""}, {Gemini, "Fork", 68, ""}, {Gemini, "ForkMessages", 68, ""}, {Gemini, "ForkWorktree", 68, ""},
		{Gemini, "Thinking", 70, ""}, {Gemini, "Yolo", 72, ""}, {Gemini, "Images", 73, ""},
		{Gemini, "Steer", 79, "0.66.0"}, {Gemini, "Skills", 83, "0.66.0"}, {Gemini, "Plan", 83, "0.66.0"},
		{Gemini, "Goal", 84, "0.66.0"}, {Gemini, "Rewrite", 85, ""}, {Gemini, "SteerImages", 88, "0.66.0"}, {Gemini, "SkillsManage", 88, "0.66.0"},
		{Claude, "Turns", 90, ""}, {Claude, "Effort", 92, ""}, {Claude, "Images", 93, ""}, {Claude, "Fork", 93, ""}, {Claude, "ForkMessages", 93, ""}, {Claude, "ForkWorktree", 93, ""},
		{Claude, "Skills", 94, ""}, {Claude, "Steer", 95, "2.1.286"}, {Claude, "SteerImages", 95, "2.1.286"}, {Claude, "Rewrite", 97, ""}, {Claude, "Questions", 98, "2.1.286"},
	}
	value := func(c Capabilities, name string) bool { return reflect.ValueOf(c).FieldByName(name).Bool() }
	for _, g := range gates {
		t.Run(g.engine+"/"+g.field, func(t *testing.T) {
			for _, v := range []struct {
				agent string
				want  bool
			}{{"", false}, {"invalid", false}, {fmt.Sprintf("0.2.%d", g.patch-1), false}, {fmt.Sprintf("0.2.%d", g.patch), true}, {fmt.Sprintf("v0.2.%d+build", g.patch), true}, {fmt.Sprintf("0.2.%d", g.patch+1), true}, {"1.0.0", true}} {
				c := ResolveCapabilities(g.engine, v.agent, g.cli)
				if value(c, g.field) != v.want {
					t.Fatalf("Agent %q CLI %q: %s=%v, want %v", v.agent, g.cli, g.field, value(c, g.field), v.want)
				}
			}
			if g.cli != "" {
				below := "0.65.99"
				if g.engine == Claude {
					below = "2.1.285"
				}
				for _, cli := range []string{"", "invalid", below} {
					if value(ResolveCapabilities(g.engine, "1.0.0", cli), g.field) {
						t.Fatalf("CLI %q accepted", cli)
					}
				}
				above := "0.66.1"
				if g.engine == Claude {
					above = "2.1.287"
				}
				if !value(ResolveCapabilities(g.engine, "1.0.0", above), g.field) {
					t.Fatalf("CLI %q rejected", above)
				}
				if g.engine == Claude && !value(ResolveCapabilities(g.engine, "1.0.0", g.cli+" (Claude Code)"), g.field) {
					t.Fatal("Claude CLI description rejected")
				}
			}
		})
	}
	for _, engine := range []string{Codex, Gemini, Claude} {
		for _, v := range []struct {
			agent string
			want  bool
		}{{"0.3.05", false}, {"0.3.06", true}, {"0.3.07", true}, {"invalid", false}} {
			if c := ResolveCapabilities(engine, v.agent, "1.0.0"); c.Collaboration != v.want {
				t.Fatalf("collaboration %s %s: %+v", engine, v.agent, c)
			}
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
		if got := ResolveCapabilities(engine, "1.0.0", cli); got != want {
			t.Fatalf("%s: %+v want %+v", engine, got, want)
		}
	}
	// Ungated native operations remain available with legacy/unknown versions;
	// capability resolution must not introduce a new minimum Agent requirement.
	c := ResolveCapabilities(Codex, "", "")
	if !c.Turns || !c.Images || !c.Rewrite || !c.Fork || !c.ForkWorktree || c.Steer || c.ForkMessages {
		t.Fatalf("legacy Codex: %+v", c)
	}
}
