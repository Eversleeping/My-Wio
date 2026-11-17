package agentgateway

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestRetiredOperationsAreNeverDeliveredToOlderAgents(t *testing.T) {
	for _, kind := range []string{protocol.OpAgentIsolationPlan, protocol.OpAgentIsolationInstall, protocol.OpAgentAccessUpdate} {
		for _, delivered := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/delivered=%t", kind, delivered), func(t *testing.T) {
				g, workspace := undeliveredWorkspace(t)
				ctx := context.Background()
				id, err := g.store.QueueResourceOperation(ctx, workspace.ServerID, kind, protocol.AgentAccessCommand{ServerID: workspace.ServerID, Settings: &protocol.AgentAccessSettings{SessionIsolation: true}}, store.NewID(), store.OperationResource{}, false)
				if err != nil {
					t.Fatal(err)
				}
				if delivered {
					if _, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE agent_operations SET status='delivered',delivered_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), id); err != nil {
						t.Fatal(err)
					}
				}
				stream := &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 4)}
				if err := g.flush(stream, workspace.ServerID); err != nil {
					t.Fatal(err)
				}
				if len(stream.sent) != 0 {
					t.Fatal("retired operation was delivered")
				}
				op, err := g.store.Operation(ctx, id)
				if err != nil || op.Status != "failed" || !strings.Contains(op.Result, "has been removed") {
					t.Fatalf("legacy operation not terminated: %+v %v", op, err)
				}
			})
		}
	}
}
