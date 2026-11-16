package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
)

type heartbeatSequenceStream struct {
	*keepaliveStream
	messages []*protocol.AgentEnvelope
}

func (s *heartbeatSequenceStream) Recv() (*protocol.AgentEnvelope, error) {
	if len(s.messages) == 0 {
		return nil, io.EOF
	}
	m := s.messages[0]
	s.messages = s.messages[1:]
	return m, nil
}

func TestConnectionRequiresItsOwnSuccessfulHeartbeat(t *testing.T) {
	ctx := context.Background()
	db, server := gatewayTestServer(t, "first-heartbeat")
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	id, err := db.QueueOperation(ctx, server.ID, protocol.OpInventoryScan, map[string]any{}, "ready-only")
	if err != nil {
		t.Fatal(err)
	}
	stream := &heartbeatSequenceStream{keepaliveStream: &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 8)}}
	connection := g.registerConnection(stream, server.ID, "first-heartbeat", make(chan struct{}, 1))
	defer connection.cancel()
	flush := func(c *gatewayConnection) {
		t.Helper()
		if err := c.run(func() error { return g.flush(c, server.ID) }); err != nil {
			t.Fatal(err)
		}
	}
	receive := func(raw string) {
		t.Helper()
		stream.messages = []*protocol.AgentEnvelope{{Kind: protocol.AgentKindHeartbeat, PayloadJSON: []byte(raw)}}
		if err := g.receiveWithTools(connection, server.ID, make(chan *protocol.ControlEnvelope, 1)); err != io.EOF {
			t.Fatalf("receive: %v", err)
		}
	}
	flush(connection)
	if len(stream.sent) != 0 {
		t.Fatal("persisted heartbeat allowed delivery on a fresh connection")
	}
	receive(`{"agent_version":"0.3.26","capabilities":{}}`)
	flush(connection)
	if connection.heartbeatProcessed || len(stream.sent) != 0 {
		t.Fatal("malformed heartbeat unlocked delivery")
	}
	if _, err := db.DB.Exec(`CREATE TRIGGER reject_first_heartbeat BEFORE UPDATE ON servers BEGIN SELECT RAISE(ABORT,'heartbeat storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	receive(`{"agent_version":"0.3.26","capabilities":[]}`)
	if connection.heartbeatProcessed {
		t.Fatal("uncommitted heartbeat unlocked delivery")
	}
	if _, err := db.DB.Exec(`DROP TRIGGER reject_first_heartbeat`); err != nil {
		t.Fatal(err)
	}
	receive(`{"agent_version":"0.3.26","capabilities":[]}`)
	if !connection.heartbeatProcessed {
		t.Fatal("successful heartbeat did not unlock delivery")
	}
	flush(connection)
	if len(stream.sent) != 1 || (<-stream.sent).OperationID != id {
		t.Fatal("ready connection failed to deliver work")
	}
	// Replacement cannot inherit readiness, even with fresh persisted metadata.
	replacement := g.registerConnection(stream, server.ID, "first-heartbeat", make(chan struct{}, 1))
	defer replacement.cancel()
	if _, err := db.QueueOperation(ctx, server.ID, protocol.OpInventoryScan, map[string]any{}, "reconnect-ready-only"); err != nil {
		t.Fatal(err)
	}
	flush(replacement)
	if replacement.heartbeatProcessed || len(stream.sent) != 0 {
		t.Fatal("reconnect inherited the previous heartbeat")
	}
}

func TestLegacyHeartbeatAllowsOnlyAgentUpdate(t *testing.T) {
	for _, version := range []string{"", "invalid", "0.2.0", "0.3.9", "0.3.25"} {
		t.Run(version, func(t *testing.T) {
			ctx := context.Background()
			db, server := gatewayTestServer(t, "legacy-update")
			g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			update, err := db.QueueOperation(ctx, server.ID, protocol.OpAgentUpdate, map[string]any{}, "rescue")
			if err != nil {
				t.Fatal(err)
			}
			scan, err := db.QueueOperation(ctx, server.ID, protocol.OpInventoryScan, map[string]any{}, "refuse")
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(protocol.Heartbeat{AgentVersion: version, Capabilities: testCapabilities("0.66.0", "2.1.286")})
			stream := &heartbeatSequenceStream{keepaliveStream: &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 8)}, messages: []*protocol.AgentEnvelope{{Kind: protocol.AgentKindHeartbeat, PayloadJSON: raw}}}
			connection := g.registerConnection(stream, server.ID, "legacy-update", make(chan struct{}, 1))
			defer connection.cancel()
			if err := g.receiveWithTools(connection, server.ID, make(chan *protocol.ControlEnvelope, 1)); err != io.EOF {
				t.Fatal(err)
			}
			if err := connection.run(func() error { return g.flush(connection, server.ID) }); err != nil {
				t.Fatal(err)
			}
			current, err := db.Server(ctx, server.ID)
			if err != nil || current.AgentVersion != version || current.LastSeenAt == nil || !current.AgentUpgradeRequired {
				t.Fatalf("legacy heartbeat not recorded: %+v %v", current, err)
			}
			if len(stream.sent) != 1 {
				t.Fatalf("unexpected deliveries: %d", len(stream.sent))
			}
			if msg := <-stream.sent; msg.OperationID != update || msg.Kind != protocol.OpAgentUpdate {
				t.Fatalf("not rescue update: %+v", msg)
			}
			op, err := db.Operation(ctx, scan)
			if err != nil || op.Status != "failed" || op.DeliveredAt != nil {
				t.Fatalf("unready operation escaped: %+v %v", op, err)
			}
		})
	}
}

func TestMinimumRejectionPreservesUncertaintyAfterPriorDelivery(t *testing.T) {
	ctx := context.Background()
	g, w := undeliveredWorkspace(t)
	if _, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE workspaces SET management_mode='managed',status='ready' WHERE id=?"), w.ID); err != nil {
		t.Fatal(err)
	}
	w.ManagementMode = "managed"
	command := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: w.ID, ProjectID: w.ProjectID, SourcePath: w.Path, Action: "move", TargetPath: "/srv/moved"}
	id, err := g.store.QueueWorkspaceLifecycle(ctx, w, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE agent_operations SET status='delivered',delivered_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), id); err != nil {
		t.Fatal(err)
	}
	if err := g.store.Heartbeat(ctx, w.ServerID, protocol.Heartbeat{AgentVersion: "0.3.25"}); err != nil {
		t.Fatal(err)
	}
	stream := &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 4)}
	if err := g.flush(stream, w.ServerID); err != nil {
		t.Fatal(err)
	}
	current, err := g.store.Workspace(ctx, w.ID)
	if err != nil || current.Status != "partial" {
		t.Fatalf("prior delivery falsely reported unchanged: %+v %v", current, err)
	}
	if len(stream.sent) != 0 {
		t.Fatal("old Agent received prior operation")
	}
}
