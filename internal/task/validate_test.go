package task

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPlanGraphAndInputs(t *testing.T) {
	base := AppendPlan{Reason: "explicit plan", Members: []Member{{ID: "dev", Role: "developer", Engine: "codex", WorkspaceID: "workspace", DirectoryMode: "shared"}}, Steps: []Step{
		{ID: "develop", MemberID: "dev", Kind: "develop", Title: "Build", Instructions: "implement", Required: true, CompletionPolicy: "execution", CriterionIDs: []string{"works"}},
		{ID: "summarize", MemberID: "dev", Kind: "summarize", Title: "Summarize", Instructions: "summarize", Required: true, CompletionPolicy: "execution", DependsOn: []string{"develop"}, Inputs: []Input{{Kind: "step_result", StepID: "develop"}}},
	}}
	criteria := []Criterion{{ID: "works", Text: "works"}}
	for _, test := range []struct {
		name string
		edit func(*AppendPlan)
		code string
	}{
		{"valid", func(*AppendPlan) {}, ""},
		{"cycle", func(p *AppendPlan) { p.Steps[0].DependsOn = []string{"summarize"} }, Invalid},
		{"self", func(p *AppendPlan) { p.Steps[0].DependsOn = []string{"develop"} }, Invalid},
		{"unknown dependency", func(p *AppendPlan) { p.Steps[1].DependsOn = []string{"missing"} }, Invalid},
		{"duplicate member", func(p *AppendPlan) { p.Members = append(p.Members, p.Members[0]) }, Invalid},
		{"duplicate step", func(p *AppendPlan) { p.Steps[1].ID = "develop" }, Invalid},
		{"unknown member", func(p *AppendPlan) { p.Steps[0].MemberID = "other" }, Invalid},
		{"unknown criterion", func(p *AppendPlan) { p.Steps[0].CriterionIDs = []string{"other"} }, Invalid},
		{"non ancestor", func(p *AppendPlan) { p.Steps[0].Inputs = []Input{{Kind: "step_result", StepID: "summarize"}} }, Invalid},
		{"implicit file", func(p *AppendPlan) { p.Steps[0].Inputs = []Input{{Kind: "file", Text: "/srv/code"}} }, Unsupported},
		{"optional", func(p *AppendPlan) { p.Steps[0].Required = false }, Unsupported},
		{"review policy", func(p *AppendPlan) { p.Steps[0].CompletionPolicy = "review" }, Unsupported},
		{"unready worktree", func(p *AppendPlan) { p.Members[0].DirectoryMode = "worktree" }, Invalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var p AppendPlan
			_ = json.Unmarshal(raw, &p)
			test.edit(&p)
			err := ValidatePlan(p, criteria)
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) || e.Code != test.code {
				t.Fatalf("error=%v want %s", err, test.code)
			}
		})
	}
}
