package store

import "context"

// RestoreWorkspaceGitRefresh undoes BeginWorkspaceGitRefresh for a request that
// was rejected before it queued an operation, putting back the state read
// before the refresh began. It leaves a snapshot that is no longer refreshing
// alone: another operation has completed the refresh since.
func (s *Store) RestoreWorkspaceGitRefresh(ctx context.Context, previous WorkspaceGitSnapshot) error {
	_, err := s.DB.ExecContext(ctx, s.Q(`UPDATE workspace_git_snapshots SET status=?,error=?,requested_at=? WHERE workspace_id=? AND status='refreshing'`), previous.Status, previous.Error, previous.RequestedAt, previous.WorkspaceID)
	return err
}
