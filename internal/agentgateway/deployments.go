package agentgateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"strings"
)

// The Agent reports deployment progress separately from the operation result,
// so a lost deploy would otherwise stay "running" on the deployment page.
func (g *Gateway) failLostDeployment(ctx context.Context, operation store.Operation) error {
	return g.failDeploymentOperation(ctx, operation, protocol.OperationLostMessage)
}

func (g *Gateway) failDeploymentOperation(ctx context.Context, operation store.Operation, message string) error {
	if operation.Kind != protocol.OpDeployStart && operation.Kind != protocol.OpDeployRollback {
		return nil
	}
	var command struct {
		DeploymentID string `json:"deployment_id"`
	}
	// The relation is readable even when the Vault key no longer decrypts the
	// command. Fall back to the payload for legacy operations not yet linked.
	err := g.store.DB.GetContext(ctx, &command.DeploymentID, g.store.Q("SELECT id FROM deployments WHERE operation_id=?"), operation.ID)
	if errors.Is(err, sql.ErrNoRows) {
		payload := json.RawMessage(operation.Payload)
		if strings.HasPrefix(operation.Payload, "v1:") {
			if err := g.vault.Decrypt(operation.Payload, &payload); err != nil {
				return nil
			}
		}
		if err := json.Unmarshal(payload, &command); err != nil || command.DeploymentID == "" {
			return nil
		}
	} else if err != nil {
		return err
	}
	deployment, err := g.store.Deployment(ctx, command.DeploymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	switch deployment.Status {
	case "succeeded", "failed", "rolled_back", "canceled":
		return nil
	}
	update := protocol.DeploymentStatus{DeploymentID: command.DeploymentID, Status: "failed", Message: message}
	if err := g.store.SaveDeploymentStatus(ctx, update); err != nil {
		return err
	}
	event, err := json.Marshal(update)
	if err != nil {
		return err
	}
	return g.publish(ctx, protocol.StreamEvent{StreamID: command.DeploymentID, Kind: protocol.EventDeploymentFailed, Payload: event})
}
