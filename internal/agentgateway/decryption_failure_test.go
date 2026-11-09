package agentgateway

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestUndecryptableDeploymentFinishesBusinessState(t *testing.T) {
	for _, kind := range []string{"deploy.start", "deploy.rollback"} {
		for _, lost := range []bool{false, true} {
			name := kind + "/flush"
			if lost {
				name = kind + "/lost"
			}
			t.Run(name, func(t *testing.T) {
				db, server := gatewayTestServer(t, "decrypt-deployment")
				ctx := context.Background()
				project, err := db.CreateProject(ctx, "decrypt", "https://example.com/decrypt.git")
				if err != nil {
					t.Fatal(err)
				}
				target, err := db.CreateDeploymentTarget(ctx, store.DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL})
				if err != nil {
					t.Fatal(err)
				}
				deployment, err := db.CreateDeployment(ctx, target.ID, "main")
				if err != nil {
					t.Fatal(err)
				}
				if kind == "deploy.rollback" {
					if err = db.SaveDeploymentStatus(ctx, protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "succeeded"}); err != nil {
						t.Fatal(err)
					}
					deployment, err = db.CreateRollbackDeployment(ctx, target.ID, deployment.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				op, err := db.QueueEncryptedResourceOperation(ctx, server.ID, kind, "v1:invalid-ciphertext", "decrypt", store.OperationResource{ProjectID: project.ID})
				if err != nil {
					t.Fatal(err)
				}
				if err = db.AttachDeploymentOperation(ctx, deployment.ID, op); err != nil {
					t.Fatal(err)
				}
				g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
				if lost {
					if err = db.MarkRunning(ctx, op); err != nil {
						t.Fatal(err)
					}
					err = g.reconcileOperation(ctx, server.ID, protocol.OperationReconcile{OperationID: op, State: protocol.OperationReconcileUnknown})
				} else {
					err = g.flush(&keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 4)}, server.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				current, err := db.Deployment(ctx, deployment.ID)
				if err != nil {
					t.Fatal(err)
				}
				state, err := db.DeploymentTarget(ctx, target.ID)
				if err != nil {
					t.Fatal(err)
				}
				operation, err := db.Operation(ctx, op)
				if err != nil {
					t.Fatal(err)
				}
				if current.Status != "failed" || operation.Status != "failed" || state.ContainerStatus == "pending" {
					t.Fatalf("unfinished operation: deployment=%s operation=%s container=%s", current.Status, operation.Status, state.ContainerStatus)
				}
				if _, err = db.CreateDeployment(ctx, target.ID, "main"); err != nil {
					t.Fatalf("retry still blocked: %v", err)
				}
			})
		}
	}
}

func TestUndecryptableContainerAndCredentialOperationsFinish(t *testing.T) {
	for _, action := range []string{"start", "restart", "stop", "remove", "delete"} {
		t.Run(action, func(t *testing.T) {
			db, server := gatewayTestServer(t, "decrypt-container")
			ctx := context.Background()
			project, err := db.CreateProject(ctx, "decrypt", "https://example.com/decrypt.git")
			if err != nil {
				t.Fatal(err)
			}
			target, err := db.CreateDeploymentTarget(ctx, store.DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL})
			if err != nil {
				t.Fatal(err)
			}
			op, err := db.QueueDeploymentContainerOperation(ctx, target.ID, server.ID, action, "v1:invalid-ciphertext", "decrypt-container")
			if err != nil {
				t.Fatal(err)
			}
			if action != "start" {
				if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE agent_operations SET kind=? WHERE id=?"), "deploy.container."+action, op); err != nil {
					t.Fatal(err)
				}
			}
			g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			stream := &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 4)}
			for i := 0; i < 2; i++ {
				if err = g.flush(stream, server.ID); err != nil {
					t.Fatal(err)
				}
			}
			current, err := db.DeploymentTarget(ctx, target.ID)
			if err != nil {
				t.Fatal(err)
			}
			operation, err := db.Operation(ctx, op)
			if err != nil {
				t.Fatal(err)
			}
			if current.ContainerStatus != "failed" || operation.Status != "failed" {
				t.Fatalf("container action stuck: %+v %+v", current, operation)
			}
		})
	}
	for _, kind := range []string{"credentials.configure", "codex.credentials.configure", "git.credentials.configure", "claude.credentials.configure", "gemini.credentials.configure"} {
		t.Run(kind, func(t *testing.T) {
			db, server := gatewayTestServer(t, "decrypt-credentials")
			ctx := context.Background()
			op, err := db.QueueOperation(ctx, server.ID, kind, map[string]any{}, kind)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE agent_operations SET payload=? WHERE id=?"), "v1:invalid-ciphertext", op); err != nil {
				t.Fatal(err)
			}
			g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			stream := &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 4)}
			for i := 0; i < 2; i++ {
				if err = g.flush(stream, server.ID); err != nil {
					t.Fatal(err)
				}
			}
			current, err := db.Operation(ctx, op)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status != "failed" || !strings.Contains(current.Result, "decrypt") {
				t.Fatalf("credentials operation not failed: %+v", current)
			}
		})
	}
}
