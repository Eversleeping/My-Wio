// Package sessionservice coordinates engine-neutral collaboration and bounded waits.
package sessionservice

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wio-platform/wio/internal/sessiontools"
	"github.com/wio-platform/wio/internal/store"
)

type Service struct{ Store *store.Store }

func (s *Service) Execute(ctx context.Context, serverID string, call sessiontools.Call) sessiontools.Reply {
	reply := s.Store.ExecuteCollaboration(ctx, serverID, call)
	if call.Name != "wio_wait_sessions" || reply.Error != nil || reply.Status != "running" {
		reply.ID = call.ID
		return reply
	}
	var p sessiontools.Params
	_ = json.Unmarshal(call.Arguments, &p)
	if p.TimeoutMS == 0 {
		reply.ID = call.ID
		return reply
	}
	if err := s.Store.RegisterCollaborationWait(ctx, call.Source, p.Sessions, time.Duration(p.TimeoutMS)*time.Millisecond); err != nil {
		return sessiontools.Reply{ID: call.ID, Status: "failed", Error: &sessiontools.Error{Code: "WAIT_DEPENDENCY", Message: err.Error()}}
	}
	defer s.Store.ClearCollaborationWait(context.WithoutCancel(ctx), call.Source)
	deadline := time.NewTimer(time.Duration(p.TimeoutMS) * time.Millisecond)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			reply.Status = "unknown"
			reply.ID = call.ID
			return reply
		case <-deadline.C:
			reply.ID = call.ID
			return reply
		case <-tick.C:
			reply = s.Store.ExecuteCollaboration(ctx, serverID, call)
			if reply.Error != nil || reply.Status != "running" {
				reply.ID = call.ID
				return reply
			}
		}
	}
}
