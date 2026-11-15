package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

// AISession is a read model, independent of the engine's physical thread table.
// Login sessions continue to use Session. Native writes stay in their drivers.
type AISession = session.Record

type AISessionFilter = session.Filter

// The source includes revoked servers because scheduled-task history retains
// those rows as offline. Public session/collaboration reads filter them out.
var aiSessionRows = buildAISessionRows()

func buildAISessionRows() string {
	sources := []struct{ engine, native, model, mode, effort, pin, cli, operation, execution, join string }{
		{engine: session.Codex, native: "t.codex_thread_id", model: "''", mode: "'on-request'", effort: "''", pin: "t.pinned_at", cli: codexEngineVersion,
			operation: "COALESCE((SELECT o.id FROM agent_operations o WHERE o.thread_id=t.id AND o.kind IN ('codex.turn.start','codex.turn.rewrite') ORDER BY o.created_at DESC LIMIT 1),'')",
			execution: "COALESCE((SELECT o.payload FROM agent_operations o WHERE o.thread_id=t.id AND o.kind IN ('codex.turn.start','codex.turn.rewrite') ORDER BY o.created_at DESC LIMIT 1),(SELECT execution_json FROM session_tool_requests WHERE target_engine='codex' AND target_id=t.id AND tool IN ('wio_create_session','wio_fork_session') ORDER BY created_at LIMIT 1),'')"},
		{engine: session.Claude, native: "t.session_id", model: "t.model", mode: "t.permission_mode", effort: "COALESCE(cto.effort,'')", pin: "cm.pinned_at", cli: "COALESCE(ccli.version,'')", operation: "t.operation_id", execution: "''",
			join: " LEFT JOIN claude_thread_options cto ON cto.thread_id=t.id LEFT JOIN claude_thread_management cm ON cm.thread_id=t.id LEFT JOIN " + claudeEngineCLI + " ccli ON ccli.server_id=s.id"},
		{engine: session.Gemini, native: "t.session_id", model: "t.model", mode: "t.approval_mode", effort: "t.thinking_level", pin: "t.pinned_at", cli: geminiEngineVersion, operation: "t.operation_id", execution: "''"},
	}
	var queries []string
	for _, source := range sources {
		queries = append(queries, "SELECT '"+source.engine+"' AS engine,t.id,t.title,t.status,t.workspace_id,w.project_id,w.server_id,w.path,w.display_name AS workspace_name,"+projectServerName+" AS project_name,s.name AS server_name,"+source.native+" AS native_id,"+source.model+" AS model,"+source.mode+" AS mode,"+source.effort+" AS effort,"+source.operation+" AS operation_id,"+source.pin+" AS pinned_at,t.archived_at,"+projectServerPin+" AS project_pinned_at,"+projectServerHidden+" AS project_hidden_at,t.created_at,t.updated_at,s.agent_version,"+source.cli+" AS cli_version,s.last_seen_at,s.revoked_at AS server_revoked_at,"+source.execution+" AS execution_json FROM "+source.engine+"_threads t JOIN workspaces w ON w.id=t.workspace_id JOIN servers s ON s.id=w.server_id JOIN projects p ON p.id=w.project_id"+projectServerJoin+source.join)
	}
	return strings.Join(queries, " UNION ALL ")
}

func decorateAISession(t *AISession) {
	t.ServerStatus = "offline"
	if t.ServerRevokedAt == nil && t.LastSeenAt != nil && t.LastSeenAt.After(time.Now().UTC().Add(-ServerOnlineGracePeriod)) {
		t.ServerStatus = "online"
	}
	t.Capabilities = session.ResolveCapabilities(t.Engine, t.AgentVersion, t.CLIVersion)
	if t.Engine == session.Codex && t.ExecutionJSON != "" {
		if t.OperationID == "" {
			var execution struct{ Model, Mode, Effort string }
			if json.Unmarshal([]byte(t.ExecutionJSON), &execution) == nil && execution.Mode != "" {
				t.Model, t.Mode, t.Effort = execution.Model, execution.Mode, execution.Effort
			}
		} else {
			var command protocol.StartTurnCommand
			if json.Unmarshal([]byte(t.ExecutionJSON), &command) == nil {
				if command.ThreadID == "" {
					var rewrite protocol.RewriteTurnCommand
					_ = json.Unmarshal([]byte(t.ExecutionJSON), &rewrite)
					command = rewrite.Start
				}
				t.Model, t.Effort = command.Model, command.ReasoningEffort
				if command.ApprovalMode != "" {
					t.Mode = command.ApprovalMode
				}
			}
		}
	}
}

func (s *Store) aiSession(ctx context.Context, db sqlx.QueryerContext, ref session.Ref, includeRevoked bool) (AISession, error) {
	var t AISession
	if !ref.Valid() {
		return t, invalidInput(errors.New("invalid session reference or filter"))
	}
	query := "SELECT * FROM (" + aiSessionRows + ") sessions WHERE engine=? AND id=?"
	if !includeRevoked {
		query += " AND server_revoked_at IS NULL"
	}
	err := sqlx.GetContext(ctx, db, &t, s.Q(query), ref.Engine, ref.ID)
	if err == nil {
		decorateAISession(&t)
	}
	return t, err
}

func (s *Store) AISession(ctx context.Context, ref session.Ref) (AISession, error) {
	return s.aiSession(ctx, s.DB, ref, false)
}

func aiSessionConditions(filter AISessionFilter) (string, []any, error) {
	query := "server_revoked_at IS NULL"
	args := []any{}
	if filter.Engine != "" {
		if !session.ValidEngine(filter.Engine) {
			return "", nil, invalidInput(errors.New("invalid session reference or filter"))
		}
		query += " AND engine=?"
		args = append(args, filter.Engine)
	}
	if filter.Archived != nil {
		if *filter.Archived {
			query += " AND archived_at IS NOT NULL"
		} else {
			query += " AND archived_at IS NULL"
		}
	}
	if filter.Query != "" {
		value := strings.ToLower(filter.Query)
		value = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(value)
		query += " AND (LOWER(workspace_name) LIKE ? ESCAPE '\\' OR LOWER(project_name) LIKE ? ESCAPE '\\' OR LOWER(path) LIKE ? ESCAPE '\\' OR LOWER(title) LIKE ? ESCAPE '\\' OR LOWER(server_name) LIKE ? ESCAPE '\\' OR LOWER(id) LIKE ? ESCAPE '\\' OR LOWER(native_id) LIKE ? ESCAPE '\\')"
		for i := 0; i < 7; i++ {
			args = append(args, "%"+value+"%")
		}
	}
	return query, args, nil
}

const aiSessionOrder = "CASE WHEN project_pinned_at IS NULL THEN 0 ELSE 1 END DESC,project_pinned_at DESC,CASE WHEN pinned_at IS NULL THEN 0 ELSE 1 END DESC,pinned_at DESC,updated_at DESC,engine DESC,id DESC"

func (s *Store) AISessions(ctx context.Context, filter AISessionFilter) ([]AISession, error) {
	items := []AISession{}
	where, args, err := aiSessionConditions(filter)
	if err != nil {
		return items, err
	}
	query := "SELECT * FROM (" + aiSessionRows + ") sessions WHERE " + where + " ORDER BY " + aiSessionOrder
	if filter.Limit < 0 {
		return items, invalidInput(errors.New("limit must not be negative"))
	}
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if err = s.DB.SelectContext(ctx, &items, s.Q(query), args...); err != nil {
		return items, err
	}
	for i := range items {
		decorateAISession(&items[i])
	}
	return items, nil
}
