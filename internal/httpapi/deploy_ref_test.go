package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

// Refs reach `git fetch` on the target server, so the API must refuse anything
// Git could parse as an option before it is stored or queued.
func TestDeploymentRefsAreValidatedBeforeStorage(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "deployment-ref-validation-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.42", ScanRoots: []string{"/srv"}}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	session := &store.Session{UserID: "test-user"}
	const injected = "--upload-pack=touch /tmp/wio-ref-injection; git-upload-pack"

	rejected := directJSONRequest(t, http.MethodPost, "/api/deployment-targets", map[string]any{"source_type": "remote", "server_id": server.ID, "environment": "production", "repository": "https://example.com/ref-validation.git", "git_ref": injected}, session, api.createDeploymentTarget)
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "git_ref") {
		t.Fatalf("injected git_ref returned %d: %s", rejected.Code, rejected.Body.String())
	}

	created := directJSONRequest(t, http.MethodPost, "/api/deployment-targets", map[string]any{"source_type": "remote", "server_id": server.ID, "environment": "production", "repository": "https://example.com/ref-validation.git", "git_ref": "release/2026.11"}, session, api.createDeploymentTarget)
	if created.Code != http.StatusCreated {
		t.Fatalf("valid git_ref returned %d: %s", created.Code, created.Body.String())
	}
	targets, err := database.ListDeploymentTargets(ctx)
	if err != nil || len(targets) != 1 {
		t.Fatalf("expected one target: %#v %v", targets, err)
	}
	target := targets[0]

	for _, ref := range []string{injected, "+refs/heads/main:refs/heads/x", "main branch", "a..b"} {
		response := deploymentResourceRequest(t, http.MethodPost, "/api/deployment-targets/"+target.ID+"/deploy", "targetID", target.ID, map[string]string{"commit_ref": ref}, api.startDeployment)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "commit_ref") {
			t.Fatalf("commit_ref %q returned %d: %s", ref, response.Code, response.Body.String())
		}
	}
	deployments, err := database.ListDeployments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 0 {
		t.Fatalf("rejected refs created deployments: %#v", deployments)
	}

	accepted := deploymentResourceRequest(t, http.MethodPost, "/api/deployment-targets/"+target.ID+"/deploy", "targetID", target.ID, map[string]string{"commit_ref": " 0123abcd "}, api.startDeployment)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("valid commit_ref returned %d: %s", accepted.Code, accepted.Body.String())
	}
}
