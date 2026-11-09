package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/mcpbridge"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

func (c *Client) sessionBridge(ctx context.Context, ref sessiontools.Ref, operation string) (map[string]any, func(), error) {
	return mcpbridge.Start(ctx, func(ctx context.Context, name string, args json.RawMessage) (sessiontools.Reply, error) {
		return c.callSessionTool(ctx, ref, operation, name, args)
	})
}

func (c *Client) callSessionTool(ctx context.Context, ref sessiontools.Ref, operation, name string, args json.RawMessage) (sessiontools.Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	id := uuid.NewString()
	replyCh := make(chan sessiontools.Reply, 1)
	c.toolMu.Lock()
	if c.toolReplies == nil {
		c.toolReplies = map[string]chan sessiontools.Reply{}
	}
	c.toolReplies[id] = replyCh
	c.toolMu.Unlock()
	defer func() { c.toolMu.Lock(); delete(c.toolReplies, id); c.toolMu.Unlock() }()
	payload, _ := json.Marshal(sessiontools.Call{ID: id, Source: ref, OperationID: operation, Name: name, Arguments: args})
	envelope := &protocol.AgentEnvelope{MessageID: id, ServerID: c.config.ServerID, Kind: "session_tool_request", OccurredAtUnixMS: time.Now().UnixMilli(), PayloadJSON: payload}
	if err := ctx.Err(); err != nil {
		return sessiontools.Reply{}, err
	}
	if err := c.enqueue(envelope, true); err != nil {
		return sessiontools.Reply{}, err
	}
	select {
	case reply := <-replyCh:
		return reply, nil
	case <-ctx.Done():
		return sessiontools.Reply{}, errors.New("tool response timed out; reconcile using the original request_id")
	}
}

func (c *Client) receiveSessionToolReply(raw json.RawMessage) {
	var reply sessiontools.Reply
	if json.Unmarshal(raw, &reply) != nil {
		return
	}
	c.toolMu.Lock()
	ch := c.toolReplies[reply.ID]
	c.toolMu.Unlock()
	if ch != nil {
		select {
		case ch <- reply:
		default:
		}
	}
}
