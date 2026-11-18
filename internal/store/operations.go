package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
)

var ErrWorkspaceWriteActive = errors.New("workspace already has an active write operation")

func (s *Store) QueueOperation(ctx context.Context, serverID, kind string, payload any, idempotency string) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.queueOperationPayload(ctx, serverID, kind, string(raw), idempotency, OperationResource{}, false)
}

type OperationResource struct {
	ProjectID   string
	WorkspaceID string
	ThreadID    string
}

func (s *Store) QueueResourceOperation(ctx context.Context, serverID, kind string, payload any, idempotency string, resource OperationResource, write bool) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.queueOperationPayload(ctx, serverID, kind, string(raw), idempotency, resource, write)
}

func (s *Store) QueueEncryptedOperation(ctx context.Context, serverID, kind, ciphertext, idempotency string) (string, error) {
	return s.QueueEncryptedResourceOperation(ctx, serverID, kind, ciphertext, idempotency, OperationResource{})
}

func (s *Store) QueueEncryptedResourceOperation(ctx context.Context, serverID, kind, ciphertext, idempotency string, resource OperationResource) (string, error) {
	if !strings.HasPrefix(ciphertext, "v1:") {
		return "", errors.New("encrypted operation payload must use a supported Vault format")
	}
	return s.queueOperationPayload(ctx, serverID, kind, ciphertext, idempotency, resource, false)
}

func (s *Store) queueOperationPayload(ctx context.Context, serverID, kind, payload, idempotency string, resource OperationResource, write bool) (string, error) {
	var existing string
	err := s.DB.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err := s.taskOperationGuard(ctx, kind, payload); err != nil {
		return "", err
	}

	// Legacy callers supply import metadata only in the payload. Indexed import
	// lookups need the project association on new writes as well as migrated rows.
	if kind == protocol.OpGitImport && resource.ProjectID == "" {
		var command protocol.GitImportCommand
		if json.Unmarshal([]byte(payload), &command) == nil {
			resource.ProjectID = command.ProjectID
		}
	}
	resolved, err := s.resolveOperationResource(ctx, serverID, resource)
	if err != nil {
		return "", err
	}
	workspaceWrite := 0
	if write {
		if resolved.WorkspaceID == "" {
			return "", errors.New("workspace write operation requires a workspace")
		}
		workspaceWrite = 1
	}
	id := NewID()
	query := s.Q("INSERT INTO agent_operations(id,server_id,project_id,workspace_id,thread_id,kind,payload,workspace_write,idempotency_key,created_at) VALUES(?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, ?, ?, ?, ?) ON CONFLICT(idempotency_key) DO NOTHING")
	args := []any{id, serverID, resolved.ProjectID, resolved.WorkspaceID, resolved.ThreadID, kind, payload, workspaceWrite, idempotency, time.Now().UTC()}
	if write {
		err = func() error {
			tx, err := s.DB.BeginTxx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			busy, err := s.collaborationWorkspaceBusy(ctx, tx, resolved.WorkspaceID)
			if err != nil {
				return err
			}
			// A concurrent retry may already have queued this exact operation.
			if err := tx.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency); err == nil {
				return nil
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if busy {
				return ErrWorkspaceWriteActive
			}
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				return err
			}
			return tx.Commit()
		}()
	} else {
		_, err = s.DB.ExecContext(ctx, query, args...)
	}
	if err != nil {
		if getErr := s.DB.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency); getErr == nil {
			return existing, nil
		}
		if write {
			active, activeErr := s.HasActiveWorkspaceWriteOperation(ctx, resolved.WorkspaceID)
			if activeErr == nil && active {
				return "", ErrWorkspaceWriteActive
			}
		}
		return "", err
	}
	if err := s.DB.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency); err != nil {
		return "", err
	}
	return existing, nil
}

func (s *Store) resolveOperationResource(ctx context.Context, serverID string, resource OperationResource) (OperationResource, error) {
	if resource.ThreadID != "" {
		var thread struct {
			WorkspaceID string `db:"workspace_id"`
			ProjectID   string `db:"project_id"`
			ServerID    string `db:"server_id"`
		}
		if err := s.DB.GetContext(ctx, &thread, s.Q(`SELECT t.workspace_id,w.project_id,w.server_id
			FROM codex_threads t JOIN workspaces w ON w.id=t.workspace_id WHERE t.id=?`), resource.ThreadID); err != nil {
			return OperationResource{}, err
		}
		if thread.ServerID != serverID {
			return OperationResource{}, errors.New("operation thread does not belong to server")
		}
		if resource.WorkspaceID != "" && resource.WorkspaceID != thread.WorkspaceID {
			return OperationResource{}, errors.New("operation thread does not belong to workspace")
		}
		if resource.ProjectID != "" && resource.ProjectID != thread.ProjectID {
			return OperationResource{}, errors.New("operation thread does not belong to project")
		}
		resource.WorkspaceID = thread.WorkspaceID
		resource.ProjectID = thread.ProjectID
		return resource, nil
	}
	if resource.WorkspaceID != "" {
		var workspace struct {
			ProjectID string `db:"project_id"`
			ServerID  string `db:"server_id"`
		}
		if err := s.DB.GetContext(ctx, &workspace, s.Q("SELECT project_id,server_id FROM workspaces WHERE id=?"), resource.WorkspaceID); err != nil {
			return OperationResource{}, err
		}
		if workspace.ServerID != serverID {
			return OperationResource{}, errors.New("operation workspace does not belong to server")
		}
		if resource.ProjectID != "" && resource.ProjectID != workspace.ProjectID {
			return OperationResource{}, errors.New("operation workspace does not belong to project")
		}
		resource.ProjectID = workspace.ProjectID
		return resource, nil
	}
	if resource.ProjectID != "" {
		var count int
		if err := s.DB.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM projects WHERE id=?"), resource.ProjectID); err != nil {
			return OperationResource{}, err
		}
		if count != 1 {
			return OperationResource{}, sql.ErrNoRows
		}
	}
	return resource, nil
}

type Operation struct {
	ID             string     `db:"id" json:"id"`
	ServerID       string     `db:"server_id" json:"server_id"`
	ProjectID      string     `db:"project_id" json:"project_id"`
	WorkspaceID    string     `db:"workspace_id" json:"workspace_id"`
	ThreadID       string     `db:"thread_id" json:"thread_id"`
	Kind           string     `db:"kind" json:"kind"`
	Payload        string     `db:"payload" json:"-"`
	Status         string     `db:"status" json:"status"`
	WorkspaceWrite int        `db:"workspace_write" json:"workspace_write"`
	Result         string     `db:"result" json:"result"`
	ResultData     string     `db:"result_data" json:"result_data"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
	DeliveredAt    *time.Time `db:"delivered_at" json:"delivered_at"`
	StartedAt      *time.Time `db:"started_at" json:"started_at"`
	CompletedAt    *time.Time `db:"completed_at" json:"completed_at"`
}

type OperationSummary struct {
	DeclaredCapabilities capabilities.Declaration `db:"declared_capabilities" json:"-"`

	AgentVersion       string     `db:"agent_version" json:"-"`
	InterruptSupported bool       `json:"interrupt_supported"`
	InterruptStatus    string     `db:"interrupt_status" json:"interrupt_status"`
	InterruptError     string     `db:"interrupt_error" json:"interrupt_error"`
	ID                 string     `db:"id" json:"id"`
	ServerID           string     `db:"server_id" json:"server_id"`
	ServerName         string     `db:"server_name" json:"server_name"`
	ProjectID          string     `db:"project_id" json:"project_id"`
	ProjectName        string     `db:"project_name" json:"project_name"`
	WorkspaceID        string     `db:"workspace_id" json:"workspace_id"`
	WorkspacePath      string     `db:"workspace_path" json:"workspace_path"`
	WorkspaceName      string     `db:"workspace_name" json:"workspace_name"`
	ThreadID           string     `db:"thread_id" json:"thread_id"`
	ThreadTitle        string     `db:"thread_title" json:"thread_title"`
	ResourceType       string     `db:"resource_type" json:"resource_type"`
	ResourceID         string     `db:"resource_id" json:"resource_id"`
	Kind               string     `db:"kind" json:"kind"`
	Status             string     `db:"status" json:"status"`
	Result             string     `db:"result" json:"result"`
	CreatedAt          time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt          string     `db:"updated_at" json:"updated_at"`
	DeliveredAt        *time.Time `db:"delivered_at" json:"delivered_at"`
	StartedAt          *time.Time `db:"started_at" json:"started_at"`
	CompletedAt        *time.Time `db:"completed_at" json:"completed_at"`
}

var ErrInvalidOperationStatus = errors.New("invalid operation status")

var operationStatuses = map[string]struct{}{
	"queued": {}, "waiting": {}, "delivered": {}, "running": {}, "succeeded": {}, "failed": {}, "canceled": {}, "cancelled": {}, "superseded": {},
}

func (s *Store) ListOperations(ctx context.Context, status string, limit int) ([]OperationSummary, error) {
	return s.ListOperationsPage(ctx, status, limit, 0)
}

func (s *Store) ListOperationsPage(ctx context.Context, status string, limit, offset int) ([]OperationSummary, error) {
	if offset < 0 {
		offset = 0
	}
	status = strings.TrimSpace(strings.ToLower(status))
	if status != "" {
		if _, ok := operationStatuses[status]; !ok {
			return nil, ErrInvalidOperationStatus
		}
	}
	canceledStatus := ""
	if status == "canceled" || status == "cancelled" {
		canceledStatus = "canceled"
	}
	limit = operationListLimit(limit)
	const updatedAt = "COALESCE(o.completed_at,o.started_at,o.delivered_at,o.created_at)"
	query := `SELECT o.id,o.server_id,COALESCE(s.name,'') server_name,COALESCE(s.agent_version,'') agent_version,` + serverCapabilitySelect + `,
		COALESCE(o.project_id,'') project_id,COALESCE(p.name,'') project_name,
		COALESCE(o.workspace_id,'') workspace_id,COALESCE(w.path,'') workspace_path,COALESCE(w.display_name,'') workspace_name,
		COALESCE(o.thread_id,'') thread_id,COALESCE(t.title,'') thread_title,
		CASE WHEN o.thread_id IS NOT NULL THEN 'thread' WHEN o.workspace_id IS NOT NULL THEN 'workspace' WHEN o.project_id IS NOT NULL THEN 'project' ELSE 'server' END resource_type,
		CASE WHEN o.thread_id IS NOT NULL THEN o.thread_id WHEN o.workspace_id IS NOT NULL THEN o.workspace_id WHEN o.project_id IS NOT NULL THEN o.project_id ELSE o.server_id END resource_id,
		o.kind,o.status,COALESCE(o.result,'') result,o.created_at,` + updatedAt + ` updated_at,o.delivered_at,o.started_at,o.completed_at
		FROM agent_operations o
		LEFT JOIN servers s ON s.id=o.server_id
		LEFT JOIN projects p ON p.id=o.project_id
		LEFT JOIN workspaces w ON w.id=o.workspace_id
		LEFT JOIN codex_threads t ON t.id=o.thread_id
		WHERE (?='' OR (?='canceled' AND o.status IN ('canceled','cancelled')) OR o.status=?)
		ORDER BY CASE WHEN o.status IN ('queued','waiting','delivered','running') THEN 0 ELSE 1 END,` + updatedAt + ` DESC,o.id DESC LIMIT ? OFFSET ?`
	var out []OperationSummary
	if err := s.DB.SelectContext(ctx, &out, s.Q(query), status, canceledStatus, status, limit, offset); err != nil {
		return nil, err
	}
	// Interruption progress is matched by exact key prefix in Go; see
	// operationInterruptKeyPrefix.
	if err := s.attachOperationInterrupts(ctx, out); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].InterruptSupported = protocol.CanInterruptOperation(out[i].Kind) && capabilities.Supports(protocol.CapabilityOperationInterrupt, out[i].AgentVersion, out[i].DeclaredCapabilities)
	}
	return out, nil
}

const operationSelect = `SELECT id,server_id,COALESCE(project_id,'') project_id,COALESCE(workspace_id,'') workspace_id,COALESCE(thread_id,'') thread_id,kind,payload,status,workspace_write,COALESCE(result,'') result,COALESCE(result_data,'{}') result_data,created_at,delivered_at,started_at,completed_at FROM agent_operations`

func (s *Store) Operation(ctx context.Context, id string) (Operation, error) {
	var operation Operation
	err := s.DB.GetContext(ctx, &operation, s.Q(operationSelect+" WHERE id=?"), id)
	return operation, err
}

func (s *Store) PendingOperations(ctx context.Context, serverID string) ([]Operation, error) {
	var out []Operation
	cutoff := time.Now().UTC().Add(-30 * time.Second)
	err := s.DB.SelectContext(ctx, &out, s.Q(operationSelect+" WHERE server_id=? AND (status='queued' OR (status='delivered' AND delivered_at<? AND kind NOT IN ('gemini.turn.start','gemini.turn.rewrite','claude.turn.start')) OR (kind='operation.interrupt' AND status='running' AND COALESCE(started_at,delivered_at,created_at)<?)) ORDER BY CASE WHEN kind='operation.interrupt' THEN 0 ELSE 1 END,created_at LIMIT 100"), serverID, cutoff, cutoff)
	return out, err
}

func (s *Store) ListProjectOperations(ctx context.Context, projectID string, limit int) ([]Operation, error) {
	limit = operationListLimit(limit)
	out := make([]Operation, 0)
	err := s.DB.SelectContext(ctx, &out, s.Q(operationSelect+` WHERE project_id=? OR workspace_id IN (SELECT id FROM workspaces WHERE project_id=?) ORDER BY created_at DESC LIMIT ?`), projectID, projectID, limit)
	return out, err
}

func (s *Store) ListWorkspaceOperations(ctx context.Context, workspaceID string, limit int) ([]Operation, error) {
	limit = operationListLimit(limit)
	var out []Operation
	err := s.DB.SelectContext(ctx, &out, s.Q(operationSelect+" WHERE workspace_id=? ORDER BY created_at DESC LIMIT ?"), workspaceID, limit)
	return out, err
}

func operationListLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func (s *Store) HasActiveWorkspaceWriteOperation(ctx context.Context, workspaceID string) (bool, error) {
	var count int
	err := s.DB.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM agent_operations WHERE workspace_id=? AND workspace_write=1 AND status IN ('waiting','queued','delivered','running')"), workspaceID)
	return count > 0, err
}

func (s *Store) HasActiveOperation(ctx context.Context, serverID, kind string) (bool, error) {
	var count int
	err := s.DB.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM agent_operations WHERE server_id=? AND kind=? AND status IN ('queued','delivered')"), serverID, kind)
	return count > 0, err
}

// MarkDelivered also stamps re-sends. PendingOperations re-sends a delivered
// operation (or a running interruption) once its timestamp is 30s old; without
// refreshing it every flush (~2s) would send the same operation again forever.
// A running interruption keeps delivered_at for queue metrics and refreshes the
// started_at that its re-send cutoff is based on. The two conditional updates
// are mutually exclusive, so at most one applies.
func (s *Store) MarkDelivered(ctx context.Context, id string) error {
	now := time.Now().UTC()
	if _, err := s.DB.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='delivered',delivered_at=? WHERE id=? AND status IN ('queued','delivered')"), now, id); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE agent_operations SET started_at=? WHERE id=? AND status='running' AND kind='operation.interrupt'"), now, id)
	return err
}

// Operation kinds excluded from the generic result reconcile: Gemini and
// Claude turns have their own ownership probe (their CLI can outlive the
// Agent), and interruptions are re-sent by PendingOperations instead.
var reconcileExcludedKinds = []string{protocol.OpGeminiTurnStart, protocol.OpGeminiTurnRewrite, protocol.OpClaudeTurnStart, protocol.OpOperationInterrupt}

// ReconcileOperationLimit bounds one keepalive's reconcile request.
const ReconcileOperationLimit = 200

// OverdueRunningOperations lists running operations whose result has not
// arrived since startedBefore. Delivered-but-unstarted operations are not
// listed: PendingOperations re-sends them, and the Agent either starts them or
// replays their receipt, so an "unknown" answer could only race that re-send.
func (s *Store) OverdueRunningOperations(ctx context.Context, serverID string, startedBefore time.Time) ([]string, error) {
	query, args, err := sqlx.In("SELECT id FROM agent_operations WHERE server_id=? AND status='running' AND COALESCE(started_at,delivered_at,created_at)<? AND kind NOT IN (?) ORDER BY COALESCE(started_at,delivered_at,created_at),id LIMIT ?", serverID, startedBefore.UTC(), reconcileExcludedKinds, ReconcileOperationLimit)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	err = s.DB.SelectContext(ctx, &ids, s.Q(query), args...)
	return ids, err
}

// ReconcilesOperationKind reports whether a lost result of this kind is
// resolved by the generic reconcile rather than a kind-specific protocol.
func ReconcilesOperationKind(kind string) bool {
	for _, excluded := range reconcileExcludedKinds {
		if kind == excluded {
			return false
		}
	}
	return true
}

func (s *Store) MarkRunning(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='running',delivered_at=COALESCE(delivered_at,?),started_at=COALESCE(started_at,?) WHERE id=? AND status IN ('queued','delivered','running')"), now, now, id)
	return err
}

func (s *Store) CompleteOperation(ctx context.Context, r protocol.OperationResult) error {
	_, err := s.CompleteOperationIfActive(ctx, r)
	return err
}

func (s *Store) CompleteOperationIfActive(ctx context.Context, r protocol.OperationResult) (bool, error) {
	data, err := operationResultData(r.Data)
	if err != nil {
		return false, err
	}
	updated, err := s.DB.ExecContext(ctx, s.Q("UPDATE agent_operations SET status=?,result=?,result_data=?,completed_at=? WHERE id=? AND status IN ('queued','delivered','running')"), r.Status, r.Message, data, time.Now().UTC(), r.OperationID)
	if err != nil {
		return false, err
	}
	count, err := updated.RowsAffected()
	return count == 1, err
}

func operationResultData(data json.RawMessage) (string, error) {
	if len(data) == 0 {
		return "{}", nil
	}
	if !json.Valid(data) {
		return "", errors.New("operation result data must be valid JSON")
	}
	return string(data), nil
}
