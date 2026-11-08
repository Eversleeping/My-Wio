package agentgateway

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

// RunSessionCollaboration recovers durable workflows even while their source
// Agent is offline. Target Agents pick up the queued operations normally.
func (g *Gateway) RunSessionCollaboration(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	since := time.Now().UTC()
	for {
		started := time.Now().UTC()
		if err := g.store.ReconcileAllCollaboration(ctx); err != nil && ctx.Err() == nil {
			g.log.Warn("collaboration reconciliation failed", "error", err)
		}
		var streams []string
		if err := g.store.DB.SelectContext(ctx, &streams, g.store.Q("SELECT DISTINCT stream_id FROM events WHERE kind IN ('session.collaboration.request','session.collaboration.updated','session.collaboration.child') AND occurred_at>=?"), since); err == nil {
			for _, stream := range streams {
				g.hub.Publish(protocol.StreamEvent{StreamID: stream, Kind: "session.collaboration.updated", OccurredAt: started})
			}
			since = started
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (g *Gateway) receiveSessionTool(stream protocol.AgentServiceConnectServer, serverID string, msg *protocol.AgentEnvelope, replies chan<- *protocol.ControlEnvelope, limit chan struct{}) {
	var call sessiontools.Call
	if json.Unmarshal(msg.PayloadJSON, &call) != nil || call.ID == "" || len(call.Arguments) > 300<<10 {
		return
	}
	respond := func(reply sessiontools.Reply) {
		reply.ID = call.ID
		raw, _ := json.Marshal(reply)
		select {
		case replies <- &protocol.ControlEnvelope{Kind: "session_tool_response", CreatedAtUnixMS: time.Now().UnixMilli(), PayloadJSON: raw}:
		case <-stream.Context().Done():
		}
	}
	select {
	case limit <- struct{}{}:
	default:
		respond(sessiontools.Reply{Status: "failed", Error: &sessiontools.Error{Code: "RATE_LIMITED", Message: "too many outstanding collaboration calls"}})
		return
	}
	go func() {
		defer func() { <-limit }()
		reply := g.sessions.Execute(stream.Context(), serverID, call)
		respond(reply)
	}()
}
