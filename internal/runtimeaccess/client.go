// Package runtimeaccess manages only the installed Agent's two runtime options.
// Helpers accept no caller-provided paths, programs, environment or shell text.
package runtimeaccess

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wio-platform/wio/internal/protocol"
	"net"
	"time"
)

type Request struct {
	Action   string                        `json:"action"`
	ServerID string                        `json:"server_id"`
	Settings *protocol.AgentAccessSettings `json:"settings,omitempty"`
	Revision string                        `json:"revision,omitempty"`
}
type Reply struct {
	Settings protocol.AgentAccessSettings `json:"settings"`
	Error    string                       `json:"error,omitempty"`
}

func Call(ctx context.Context, socket string, request Request) (protocol.AgentAccessSettings, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return protocol.AgentAccessSettings{}, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(2 * time.Minute))
	if err = json.NewEncoder(c).Encode(struct {
		Action string  `json:"action"`
		Access Request `json:"access"`
	}{"runtime-access", request}); err != nil {
		return protocol.AgentAccessSettings{}, err
	}
	var reply Reply
	if err = json.NewDecoder(c).Decode(&reply); err != nil {
		return reply.Settings, err
	}
	if reply.Error != "" {
		return reply.Settings, errors.New(reply.Error)
	}
	return reply.Settings, nil
}
