package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/task"
)

type taskSessionPermit struct{}

func (s *Store) taskMutationGuard(ctx context.Context, q sqlx.QueryerContext, engine, id string) error {
	if ref, ok := ctx.Value(taskSessionPermit{}).(session.Ref); ok && ref.Engine == engine && ref.ID == id {
		return nil
	}
	var n int
	if err := sqlx.GetContext(ctx, q, &n, s.Q("SELECT COUNT(*) FROM task_session_bindings WHERE engine=? AND session_id=? AND released_at IS NULL"), engine, id); err != nil {
		return err
	}
	if n > 0 {
		return stateConflict("session is owned by an active task run; pause and take over the attempt first")
	}
	return nil
}

// taskSessionPort keeps native session creation and Send in the dispatcher's
// transaction. Its permit is local to this exact session, never a user field.
type taskSessionPort struct {
	store *Store
	tx    *sqlx.Tx
}

func (p taskSessionPort) send(ctx context.Context, ref session.Ref, member task.Member, prompt string) (string, error) {
	t, err := p.store.collaborationSession(ctx, p.tx, ref)
	if err != nil {
		return "", err
	}
	t.Model, t.Mode, t.Effort = member.Model, member.Mode, member.Effort
	ctx = context.WithValue(ctx, taskSessionPermit{}, ref)
	return p.store.collaborationSend(ctx, p.tx, t, prompt)
}

func (s *Store) taskResourceBlockTx(ctx context.Context, tx *sqlx.Tx, project string, m task.Member, requiresArtifacts, requiresTools bool) (string, Workspace, error) {
	var w Workspace
	if err := tx.GetContext(ctx, &w, s.Q("SELECT * FROM workspaces WHERE id=? AND project_id=?"), m.WorkspaceID, project); err != nil {
		return "", w, err
	}
	if w.Status != "ready" {
		return "workspace_not_ready", w, nil
	}
	var server Server
	query := `SELECT s.id,s.agent_version,s.last_seen_at,` + serverCapabilitySelect + `,COALESCE(e.cli_version,'') codex_version,COALESCE(e.ready,0) codex_ready,COALESCE(e.credential_profile_id,'') codex_profile_id FROM servers s LEFT JOIN server_engines e ON e.server_id=s.id AND e.engine=? WHERE s.id=? AND s.revoked_at IS NULL`
	// All three engine installations use the same versioned engine table.
	if err := tx.GetContext(ctx, &server, s.Q(query), m.Engine, w.ServerID); err != nil {
		return "", w, err
	}
	if server.LastSeenAt == nil || time.Since(*server.LastSeenAt) > ServerOnlineGracePeriod {
		return "server_offline", w, nil
	}
	if !session.ResolveCapabilities(m.Engine, server.AgentVersion, server.CodexVersion, server.DeclaredCapabilities).Turns {
		return "agent_update", w, nil
	}
	if requiresArtifacts && !capabilities.Supports(protocol.CapabilityTaskArtifacts, server.AgentVersion, server.DeclaredCapabilities) ||
		requiresTools && !capabilities.Supports(protocol.CapabilityTaskCollaboration, server.AgentVersion, server.DeclaredCapabilities) {
		return "agent_update", w, nil
	}
	if server.CodexReady != 1 {
		return "cli_missing", w, nil
	}
	if server.CodexProfileID == "" {
		return "credentials_missing", w, nil
	}
	var uncertain int
	if err := tx.GetContext(ctx, &uncertain, s.Q("SELECT COUNT(*) FROM task_artifact_handoffs WHERE workspace_id=? AND action='apply' AND state='unknown'"), w.ID); err != nil {
		return "", w, err
	}
	if uncertain > 0 {
		return "artifact_handoff_unknown", w, nil
	}
	var n int
	if err := tx.GetContext(ctx, &n, s.Q("SELECT COUNT(*) FROM agent_operations WHERE server_id=? AND kind IN ('cli.install','agent.update','agent.isolation.install','codex.credentials.configure','claude.credentials.configure','gemini.credentials.configure') AND status IN ('queued','delivered','running')"), w.ServerID); err != nil {
		return "", w, err
	}
	if n > 0 {
		return "server_configuring", w, nil
	}
	return "", w, nil
}

func (s *Store) lockTaskTx(ctx context.Context, tx *sqlx.Tx, id string) error {
	r, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET version=version WHERE id=?"), id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return sql.ErrNoRows
	}
	return err
}
func (s *Store) taskBlockStepTx(ctx context.Context, tx *sqlx.Tx, id, run, step, code string) error {
	r, err := tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state='blocked',block_code=?,version=version+1 WHERE run_id=? AND step_id=? AND (state<>'blocked' OR block_code IS NULL OR block_code<>?)"), code, run, step, code)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	return s.taskEventTx(ctx, tx, id, "task.step.state.changed", task.Meta{Actor: "task:" + id}, map[string]any{"run_id": run, "step_id": step, "state": "blocked", "block_code": code})
}

func (s *Store) DispatchTaskStep(ctx context.Context, id, run, step, owner string) (string, error) {
	if owner == "" {
		return "", task.Fail(task.Invalid, "worker identity is required")
	}
	// The first read locates the server only; every decision is repeated while
	// holding server -> task -> workspace locks below.
	var hint struct {
		Server  string `db:"server_id"`
		Project string `db:"project_id"`
	}
	err := s.DB.GetContext(ctx, &hint, s.Q(`SELECT w.server_id,t.project_id FROM task_runs r JOIN tasks t ON t.id=r.task_id JOIN task_plan_steps st ON st.plan_id=r.plan_id JOIN task_plan_members m ON m.plan_id=st.plan_id AND m.id=st.member_id JOIN workspaces w ON w.id=m.workspace_id WHERE r.task_id=? AND r.id=? AND st.id=?`), id, run, step)
	if err != nil {
		return "", err
	}
	operation := ""
	err = s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		operation = ""
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=?"), hint.Server); err != nil {
			return err
		}
		if err := s.lockTaskTx(ctx, tx, id); err != nil {
			return err
		}
		t, err := s.taskTx(ctx, tx, hint.Project, id)
		if err != nil {
			return err
		}
		r, err := s.taskRunTx(ctx, tx, id, run)
		if err != nil {
			return err
		}
		if t.CurrentRunID == nil || *t.CurrentRunID != run || t.ArchivedAt != nil || r.State != "running" && r.State != "blocked" {
			return nil
		}
		var sr struct {
			State   string  `db:"state"`
			Attempt *string `db:"current_attempt_id"`
		}
		if err := tx.GetContext(ctx, &sr, s.Q("SELECT state,current_attempt_id FROM task_step_runs WHERE task_id=? AND run_id=? AND step_id=?"), id, run, step); err != nil {
			return err
		}
		if sr.State == "active" || sr.State == "succeeded" || sr.State == "failed" || sr.State == "cancelled" {
			if sr.Attempt != nil {
				return tx.GetContext(ctx, &operation, s.Q("SELECT COALESCE(operation_id,'') FROM task_attempts WHERE id=? AND run_id=? AND step_id=?"), *sr.Attempt, run, step)
			}
			return nil
		}
		// A failed or unknown previous attempt requires an explicit user action.
		if sr.Attempt != nil {
			return nil
		}
		p, err := s.taskPlanTx(ctx, tx, id, r.PlanID)
		if err != nil {
			return err
		}
		var def task.Step
		var member task.Member
		for _, st := range p.Steps {
			if st.ID == step {
				def = st
			}
		}
		for _, m := range p.Members {
			if m.ID == def.MemberID {
				member = m
			}
		}
		for _, dep := range def.DependsOn {
			var state string
			if err := tx.GetContext(ctx, &state, s.Q("SELECT state FROM task_step_runs WHERE run_id=? AND step_id=?"), run, dep); err != nil {
				return err
			}
			if state != "succeeded" {
				return nil
			}
		}
		if t.CurrentGrantRevision == nil {
			return s.taskBlockStepTx(ctx, tx, id, run, step, "authorization_required")
		}
		g, err := s.taskGrantTx(ctx, tx, id, *t.CurrentGrantRevision)
		if err != nil {
			return err
		}
		if err := validateTaskGrant(g, p); err != nil {
			return s.taskBlockStepTx(ctx, tx, id, run, step, "authorization_required")
		}
		now, err := taskDBTimeTx(ctx, tx)
		if err != nil {
			return err
		}
		if r.DeadlineAt != nil && !r.DeadlineAt.After(now) {
			return nil
		}
		if t.Counters.Attempts >= g.Budgets.Attempts {
			return s.taskBlockStepTx(ctx, tx, id, run, step, "attempt_budget")
		}
		var n int
		if err := tx.GetContext(ctx, &n, s.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=? AND state IN ('queued','running','unknown')"), run); err != nil {
			return err
		}
		if n >= g.Budgets.Concurrency {
			return s.taskBlockStepTx(ctx, tx, id, run, step, "concurrency_limit")
		}
		if err := tx.GetContext(ctx, &n, s.Q("SELECT COUNT(*) FROM task_attempts WHERE state IN ('queued','running','unknown') AND (workspace_id=? OR (run_id=? AND member_id=?))"), member.WorkspaceID, run, member.ID); err != nil {
			return err
		}
		if n > 0 {
			return s.taskBlockStepTx(ctx, tx, id, run, step, "task_resource_busy")
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status=status WHERE id=?"), member.WorkspaceID); err != nil {
			return err
		}
		code, w, err := s.taskResourceBlockTx(ctx, tx, t.ProjectID, member, def.CompletionPolicy != "execution", g.TaskTools)
		if err != nil {
			return err
		}
		if w.ServerID != hint.Server {
			return task.Fail(task.Conflict, "workspace server changed")
		}
		if code != "" {
			return s.taskBlockStepTx(ctx, tx, id, run, step, code)
		}
		if busy, err := s.workspaceWriteBusy(ctx, tx, w.ID, true); err != nil {
			return err
		} else if busy {
			return s.taskBlockStepTx(ctx, tx, id, run, step, "workspace_busy")
		}
		artifact, review, err := s.taskReviewInputTx(ctx, tx, run, w.ID, def)
		if err != nil {
			return err
		}
		if def.CompletionPolicy == "review" && review == nil {
			return s.taskBlockStepTx(ctx, tx, id, run, step, "review_workspace_required")
		}
		if review != nil {
			review.Workspace = w.Path
		}
		var number int
		if err := tx.GetContext(ctx, &number, s.Q("SELECT COALESCE(MAX(number),0)+1 FROM task_attempts WHERE run_id=? AND step_id=?"), run, step); err != nil {
			return err
		}
		if number > g.Budgets.StepAttempts {
			return s.taskBlockStepTx(ctx, tx, id, run, step, "step_attempt_budget")
		}
		ref := session.Ref{Engine: member.Engine}
		var binding string
		err = tx.GetContext(ctx, &binding, s.Q("SELECT session_id FROM task_session_bindings WHERE run_id=? AND member_id=? AND released_at IS NULL"), run, member.ID)
		if errors.Is(err, sql.ErrNoRows) {
			ref.ID = NewID()
			if err := s.createCollaborationSessionTx(ctx, tx, ref, w.ID, t.Title, member.Model, member.Mode, member.Effort); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO task_session_bindings(id,task_id,run_id,engine,session_id,member_id) VALUES(?,?,?,?,?,?)"), NewID(), id, run, member.Engine, ref.ID, member.ID); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			ref.ID = binding
		}
		prompt, err := s.taskPromptTx(ctx, tx, p, def, run)
		if err != nil {
			var inputError *task.Error
			if errors.As(err, &inputError) && inputError.Code == task.Invalid {
				return s.taskBlockStepTx(ctx, tx, id, run, step, "compiled_input_limit")
			}
			return err
		}
		if review != nil {
			prompt += fmt.Sprintf("\nReview prepared artifact %s at digest %s. Check the required conditions and report concrete evidence for the user.\n", artifact, review.Digest)
		}
		if g.TaskTools {
			prompt += "\nTask member tools are available. Use wio_get_task_context for scope and team requests.\n"
			messages, err := s.taskTeamPromptTx(ctx, tx, run, member.ID, step, artifact)
			if err != nil {
				return err
			}
			if len(prompt)+len(messages) > 256<<10 {
				return s.taskBlockStepTx(ctx, tx, id, run, step, "compiled_input_limit")
			}
			prompt += messages
		}
		operation, err = (taskSessionPort{s, tx}).send(ctx, ref, member, prompt)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("task:%s:%s:%d", run, step, number)
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET idempotency_key=? WHERE id=?"), key, operation); err != nil {
			return err
		}
		deadline := now.Add(time.Duration(g.Budgets.StepSeconds) * time.Second)
		lease := now.Add(30 * time.Second)
		attempt := NewID()
		input, _ := json.Marshal(map[string]any{"prompt": prompt, "member": member, "grant_revision": g.Revision})
		source, _ := json.Marshal(w)
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO task_attempts(id,task_id,run_id,plan_id,step_id,member_id,number,dispatch_key,input_hash,input_json,state,engine,session_id,server_id,workspace_id,source_json,operation_id,lease_owner,lease_expires_at,deadline_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,'queued',?,?,?,?,?,?,?,?,?,?)`), attempt, id, run, p.ID, step, member.ID, number, key, HashToken(string(input)), string(input), member.Engine, ref.ID, w.ServerID, w.ID, string(source), operation, owner, lease, deadline, now); err != nil {
			return err
		}
		if review != nil {
			if err := s.bindTaskReviewTx(ctx, tx, id, run, attempt, operation, artifact, p, def, review); err != nil {
				return err
			}
			if err := s.fulfillTaskReviewsTx(ctx, tx, run, step, artifact); err != nil {
				return err
			}

		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_session_bindings SET active_attempt_id=? WHERE run_id=? AND member_id=? AND released_at IS NULL"), attempt, run, member.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state='active',current_attempt_id=?,block_code=NULL,version=version+1 WHERE run_id=? AND step_id=?"), attempt, run, step); err != nil {
			return err
		}
		t.Counters.Attempts++
		counters, _ := json.Marshal(t.Counters)
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET counters_json=?,version=version+1,updated_at=? WHERE id=?"), string(counters), now, id); err != nil {
			return err
		}
		return s.taskEventTx(ctx, tx, id, "task.attempt.queued", task.Meta{Actor: "task:" + id}, map[string]any{"run_id": run, "step_id": step, "attempt_id": attempt, "operation_id": operation, "dispatch_key": key, "grant_revision": g.Revision})
	})
	return operation, err
}

func (s *Store) taskPromptTx(ctx context.Context, tx *sqlx.Tx, p task.Plan, step task.Step, run string) (string, error) {
	var b strings.Builder
	b.WriteString("Task requirement:\n" + p.Requirement + "\nAcceptance criteria (execution does not imply acceptance):\n")
	for _, c := range p.Criteria {
		fmt.Fprintf(&b, "- %s: %s\n", c.ID, c.Text)
	}
	b.WriteString("\nStep: " + step.Title + "\n" + step.Instructions + "\n")
	for _, input := range step.Inputs {
		if input.Kind == "text" {
			b.WriteString("\nExplicit task input:\n" + input.Text)
		} else if input.Kind == "step_result" {
			var summary string
			if err := tx.GetContext(ctx, &summary, s.Q("SELECT COALESCE(a.result_summary,'') FROM task_step_runs st JOIN task_attempts a ON a.id=st.current_attempt_id WHERE st.run_id=? AND st.step_id=? AND st.state='succeeded'"), run, input.StepID); err != nil {
				return "", err
			}
			b.WriteString("\nRecorded predecessor result (task material):\n" + summary)
		}
	}
	if b.Len() > 256<<10 {
		return "", task.Fail(task.Invalid, "compiled task input exceeds 256 KiB")
	}
	return b.String(), nil
}

// Generic operation entry points must enforce the same ownership rule as the
// native drivers, including nested worktree forks. Approval replies remain usable.
func (s *Store) taskOperationGuard(ctx context.Context, kind, payload string) error {
	engine, _, _ := strings.Cut(kind, ".")
	if kind == protocol.OpCodexApproval || kind == protocol.OpClaudeQuestionAnswer || kind == protocol.OpGeminiQuestionAnswer || kind == protocol.OpCodexGoalGet || kind == protocol.OpCodexStatusSnapshot || kind == protocol.OpCodexMCPList {
		return nil
	}
	if engine != "codex" && engine != "claude" && engine != "gemini" && kind != protocol.OpGitWorktreeCreate {
		return nil
	}
	var command struct {
		ThreadID       string `json:"thread_id"`
		SourceThreadID string `json:"source_thread_id"`
		Start          struct {
			ThreadID string `json:"thread_id"`
		} `json:"start"`
		ClaudeFork *protocol.ClaudeForkCommand `json:"claude_fork"`
		GeminiFork *protocol.GeminiForkCommand `json:"gemini_fork"`
	}
	if json.Unmarshal([]byte(payload), &command) != nil {
		return nil
	}
	if kind == protocol.OpGitWorktreeCreate {
		engine = "codex"
		if command.ClaudeFork != nil {
			if err := s.taskMutationGuard(ctx, s.DB, "claude", command.ClaudeFork.SourceThreadID); err != nil {
				return err
			}
		}
		if command.GeminiFork != nil {
			if err := s.taskMutationGuard(ctx, s.DB, "gemini", command.GeminiFork.SourceThreadID); err != nil {
				return err
			}
		}
	}
	for _, id := range []string{command.ThreadID, command.SourceThreadID, command.Start.ThreadID} {
		if id != "" {
			if err := s.taskMutationGuard(ctx, s.DB, engine, id); err != nil {
				return err
			}
		}
	}
	return nil
}
