package task

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/wio-platform/wio/internal/session"
)

var logicalID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidateMeta(m Meta, update bool) error {
	if strings.TrimSpace(m.Actor) == "" || len(m.RequestID) < 1 || len(m.RequestID) > 128 || strings.TrimSpace(m.RequestID) != m.RequestID || strings.ContainsAny(m.RequestID, "\x00\r\n") {
		return Fail(Invalid, "actor and stable request_id are required")
	}
	if update && m.ExpectedVersion < 1 {
		return Fail(Invalid, "expected_version must be positive")
	}
	return nil
}
func ValidateSpec(title, requirement string, criteria []Criterion) error {
	if strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > 200 || strings.TrimSpace(requirement) == "" || len(requirement) > 64<<10 || !utf8.ValidString(title) || !utf8.ValidString(requirement) {
		return Fail(Invalid, "task title or requirement is empty or too long")
	}
	if len(criteria) < 1 || len(criteria) > 50 {
		return Fail(Invalid, "provide 1 to 50 acceptance criteria")
	}
	seen := map[string]bool{}
	for _, c := range criteria {
		if !logicalID.MatchString(c.ID) || seen[c.ID] || strings.TrimSpace(c.Text) == "" || len(c.Text) > 4<<10 || !utf8.ValidString(c.Text) {
			return Fail(Invalid, "acceptance criteria need unique IDs and nonempty text of at most 4 KiB")
		}
		seen[c.ID] = true
	}
	return nil
}

// ValidatePlan checks structure only. Resource scope and execution permissions
// must also be checked inside the persistence transaction.
func ValidatePlan(p AppendPlan, criteria []Criterion) error {
	if len(p.Members) < 1 || len(p.Members) > 8 || len(p.Steps) < 1 || len(p.Steps) > 50 || strings.TrimSpace(p.Reason) == "" || len(p.Reason) > 4<<10 {
		return Fail(Invalid, "plan needs 1–8 members, 1–50 steps and a reason of at most 4 KiB")
	}
	members := map[string]Member{}
	conditions := map[string]bool{}
	for _, c := range criteria {
		conditions[c.ID] = true
	}
	for _, m := range p.Members {
		if !logicalID.MatchString(m.ID) || members[m.ID].ID != "" || !session.ValidEngine(m.Engine) || m.WorkspaceID == "" || len(m.Model) > 128 || len(m.Mode) > 64 || len(m.Effort) > 32 {
			return Fail(Invalid, "invalid or duplicate plan member")
		}
		switch m.Role {
		case "coordinator", "developer", "researcher", "reviewer":
		default:
			return Fail(Invalid, "invalid member role")
		}
		if m.DirectoryMode != "shared" && m.DirectoryMode != "existing_worktree" {
			return Fail(Invalid, "choose a ready shared workspace or existing worktree")
		}
		members[m.ID] = m
	}
	steps := map[string]Step{}
	degrees := map[string]int{}
	children := map[string][]string{}
	edges := 0
	for _, s := range p.Steps {
		if _, ok := steps[s.ID]; ok || !logicalID.MatchString(s.ID) || members[s.MemberID].ID == "" || strings.TrimSpace(s.Title) == "" || utf8.RuneCountInString(s.Title) > 200 || strings.TrimSpace(s.Instructions) == "" || len(s.Instructions) > 64<<10 {
			return Fail(Invalid, "invalid or duplicate step, member or instructions")
		}
		switch s.Kind {
		case "plan", "develop", "research", "review", "summarize":
		default:
			return Fail(Invalid, "invalid step kind")
		}
		if !s.Required {
			return Fail(Unsupported, "only required steps are supported")
		}
		switch s.CompletionPolicy {
		case "execution":
		case "artifact":
			if s.Kind != "develop" {
				return Fail(Unsupported, "artifact completion requires a development step")
			}
		case "review":
			if s.Kind != "review" || len(s.CriterionIDs) == 0 || members[s.MemberID].DirectoryMode != "existing_worktree" {
				return Fail(Unsupported, "review completion requires criteria and an independent worktree")
			}
		default:
			return Fail(Unsupported, "unknown completion policy")
		}
		if len(s.Inputs) > 100 || len(s.CriterionIDs) > 50 {
			return Fail(Invalid, "too many step inputs or criteria")
		}
		seen := map[string]bool{}
		for _, id := range s.CriterionIDs {
			if !conditions[id] || seen[id] {
				return Fail(Invalid, "unknown or duplicate acceptance criterion")
			}
			seen[id] = true
		}
		steps[s.ID] = s
	}
	for _, s := range p.Steps {
		seen := map[string]bool{}
		for _, dep := range s.DependsOn {
			if _, ok := steps[dep]; !ok || dep == s.ID || seen[dep] {
				return Fail(Invalid, "unknown, duplicate or self dependency")
			}
			seen[dep] = true
			degrees[s.ID]++
			children[dep] = append(children[dep], s.ID)
			edges++
		}
	}
	if edges > 1000 {
		return Fail(Invalid, "plan has too many dependency edges")
	}
	queue := []string{}
	for id := range steps {
		if degrees[id] == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, child := range children[id] {
			degrees[child]--
			if degrees[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	if visited != len(steps) {
		return Fail(Invalid, "plan dependencies contain a cycle")
	}
	for _, s := range p.Steps {
		ancestors := map[string]bool{}
		var walk func(string)
		walk = func(id string) {
			for _, dep := range steps[id].DependsOn {
				if !ancestors[dep] {
					ancestors[dep] = true
					walk(dep)
				}
			}
		}
		walk(s.ID)
		artifactInputs := 0
		for _, input := range s.Inputs {
			switch input.Kind {
			case "text":
				if strings.TrimSpace(input.Text) == "" || len(input.Text) > 64<<10 || input.StepID != "" {
					return Fail(Invalid, "text input must be explicit and at most 64 KiB")
				}
			case "step_result":
				if !ancestors[input.StepID] || input.Text != "" {
					return Fail(Invalid, "step input must reference a dependency ancestor")
				}
			case "artifact":
				producer := steps[input.StepID]
				if !ancestors[input.StepID] || producer.CompletionPolicy != "artifact" || s.CompletionPolicy != "review" ||
					input.Text != "" || members[producer.MemberID].WorkspaceID == members[s.MemberID].WorkspaceID {
					return Fail(Invalid, "review artifact must come from a development ancestor in a separate workspace")
				}
				artifactInputs++
			default:
				return Fail(Unsupported, "file and implicit workspace handoffs are not supported")
			}
		}
		if s.CompletionPolicy == "review" && artifactInputs != 1 {
			return Fail(Invalid, "review requires one versioned artifact input")
		}
	}
	return nil
}
