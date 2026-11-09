//go:build postgresintegration

package store

import (
	"context"
	"testing"
	"time"
)

func assertCollaborationOrphanCleanupOnPostgres(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()

	// Pick or create a valid workspace and source thread in codex_threads
	var workspaceID string
	err := database.DB.GetContext(ctx, &workspaceID, "SELECT id FROM workspaces LIMIT 1")
	if err != nil {
		t.Fatalf("get workspace on postgres: %v", err)
	}

	sourceID := "source-thread-pg-" + NewID()[:8]
	sourceKey := "codex:" + sourceID
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO codex_threads(id,workspace_id,title,status) VALUES(?,?,?,?)"), sourceID, workspaceID, "Source", "idle"); err != nil {
		t.Fatalf("insert source thread on postgres: %v", err)
	}

	// 1. Preparing child: target thread does not exist yet, but request is in progress
	prepReqID := "prep-req-pg-" + NewID()[:8]
	prepChildID := "prep-child-pg-" + NewID()[:8]
	prepTargetKey := "codex:" + prepChildID

	if _, err := database.DB.ExecContext(ctx, database.Q(`INSERT INTO session_tool_requests(id,source_key,request_key,tool,arguments,grant_version,phase,status,target_engine,target_id) VALUES(?,?,?,?,?,1,'worktree','accepted','codex',?)`), prepReqID, sourceKey, prepReqID, "wio_create_session", "{}", prepChildID); err != nil {
		t.Fatalf("insert prep request on postgres: %v", err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO session_links(target_key,source_key,root_key,depth,request_id,relation) VALUES(?,?,?,1,?,'child')"), prepTargetKey, sourceKey, sourceKey, prepReqID); err != nil {
		t.Fatalf("insert prep link on postgres: %v", err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO session_subscriptions(target_key,source_key,auto_continue,grant_version) VALUES(?,?,0,1)"), prepTargetKey, sourceKey); err != nil {
		t.Fatalf("insert prep sub on postgres: %v", err)
	}

	// 2. True orphan: target thread does not exist, and request has failed/finished (phase is empty)
	orphanReqID := "orphan-req-pg-" + NewID()[:8]
	orphanChildID := "orphan-child-pg-" + NewID()[:8]
	orphanTargetKey := "codex:" + orphanChildID

	if _, err := database.DB.ExecContext(ctx, database.Q(`INSERT INTO session_tool_requests(id,source_key,request_key,tool,arguments,grant_version,phase,status,target_engine,target_id) VALUES(?,?,?,?,?,1,'','failed','codex',?)`), orphanReqID, sourceKey, orphanReqID, "wio_create_session", "{}", orphanChildID); err != nil {
		t.Fatalf("insert orphan request on postgres: %v", err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO session_links(target_key,source_key,root_key,depth,request_id,relation) VALUES(?,?,?,1,?,'child')"), orphanTargetKey, sourceKey, sourceKey, orphanReqID); err != nil {
		t.Fatalf("insert orphan link on postgres: %v", err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO session_subscriptions(target_key,source_key,auto_continue,grant_version) VALUES(?,?,0,1)"), orphanTargetKey, sourceKey); err != nil {
		t.Fatalf("insert orphan sub on postgres: %v", err)
	}

	// Verify both exist before prune
	var count int
	if err := database.DB.GetContext(ctx, &count, database.Q("SELECT COUNT(*) FROM session_subscriptions WHERE target_key IN (?,?)"), prepTargetKey, orphanTargetKey); err != nil || count != 2 {
		t.Fatalf("expected 2 subscriptions before prune, got %d %v", count, err)
	}
	if err := database.DB.GetContext(ctx, &count, database.Q("SELECT COUNT(*) FROM session_links WHERE target_key IN (?,?)"), prepTargetKey, orphanTargetKey); err != nil || count != 2 {
		t.Fatalf("expected 2 links before prune, got %d %v", count, err)
	}

	// Execute PruneRetainedData on PostgreSQL
	if err := database.PruneRetainedData(ctx, time.Now().UTC(), RetentionPolicy{}); err != nil {
		t.Fatalf("PruneRetainedData on postgres: %v", err)
	}

	// Assert preparing child is preserved
	if err := database.DB.GetContext(ctx, &count, database.Q("SELECT COUNT(*) FROM session_subscriptions WHERE target_key=?"), prepTargetKey); err != nil || count != 1 {
		t.Fatalf("expected preparing subscription to be preserved on postgres, got %d %v", count, err)
	}
	if err := database.DB.GetContext(ctx, &count, database.Q("SELECT COUNT(*) FROM session_links WHERE target_key=?"), prepTargetKey); err != nil || count != 1 {
		t.Fatalf("expected preparing link to be preserved on postgres, got %d %v", count, err)
	}

	// Assert orphan child is pruned
	if err := database.DB.GetContext(ctx, &count, database.Q("SELECT COUNT(*) FROM session_subscriptions WHERE target_key=?"), orphanTargetKey); err != nil || count != 0 {
		t.Fatalf("expected orphan subscription to be pruned on postgres, got %d %v", count, err)
	}
	if err := database.DB.GetContext(ctx, &count, database.Q("SELECT COUNT(*) FROM session_links WHERE target_key=?"), orphanTargetKey); err != nil || count != 0 {
		t.Fatalf("expected orphan link to be pruned on postgres, got %d %v", count, err)
	}
}
