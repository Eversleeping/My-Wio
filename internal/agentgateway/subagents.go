package agentgateway

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
)

func (g *Gateway) publishSubagent(ctx context.Context, event protocol.StreamEvent) error {
	var update protocol.SubagentUpdate
	if err := json.Unmarshal(event.Payload, &update); err != nil {
		return err
	}
	if update.ID == "" || len(update.ID) > 512 {
		return errors.New("invalid subagent identity")
	}
	if update.Entry != nil && (update.Entry.ID == "" || len(update.Entry.ID) > 512) {
		return errors.New("invalid subagent entry identity")
	}
	event.Payload = security.RedactJSON(event.Payload)
	return g.publish(ctx, event)
}
