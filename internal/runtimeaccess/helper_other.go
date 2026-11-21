//go:build !linux

package runtimeaccess

import (
	"context"
	"errors"
	"github.com/wio-platform/wio/internal/protocol"
	"io"
)

type Helper struct{ Root string }

func (Helper) Handle(context.Context, Request) (protocol.AgentAccessSettings, error) {
	return protocol.AgentAccessSettings{}, errors.New("Linux runtime access helper is unavailable on this platform")
}
func ServeOne(io.Reader, io.Writer, bool) error {
	return errors.New("Linux runtime access helper is unavailable on this platform")
}
