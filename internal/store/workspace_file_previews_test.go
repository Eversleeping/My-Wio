package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestOpenMigratesLegacyFilePreviewsAndPreservesMedia(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preview.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE workspace_file_previews (
		workspace_id TEXT PRIMARY KEY,path TEXT NOT NULL DEFAULT '',content TEXT NOT NULL DEFAULT '',
		size BIGINT NOT NULL DEFAULT 0,truncated INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL DEFAULT 'idle',
		error TEXT NOT NULL DEFAULT '',requested_at TIMESTAMP,updated_at TIMESTAMP);
		INSERT INTO workspace_file_previews(workspace_id,path,content,status) VALUES('workspace','README.md','legacy text','succeeded')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for attempt := 0; attempt < 2; attempt++ {
		database, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		preview, err := database.WorkspaceFilePreview(ctx, "workspace", "README.md")
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 && (preview.Content != "legacy text" || preview.Encoding != "" || preview.MIMEType != "") {
			t.Fatalf("legacy text preview changed: %#v", preview)
		}
		if attempt == 1 && (preview.Content != "AAAA" || preview.MIMEType != "image/png" || preview.Encoding != "base64") {
			t.Fatalf("image metadata did not survive reopen: %#v", preview)
		}
		if err := database.SaveWorkspaceFilePreview(ctx, "workspace", "README.md", protocol.WorkspaceFilePreviewResult{Content: "AAAA", MIMEType: "image/png", Encoding: "base64", Size: 3}); err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
