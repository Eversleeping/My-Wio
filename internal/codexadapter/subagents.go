package codexadapter

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/subagent"
)

type codexSubagent struct {
	Stream   string
	Info     protocol.SubagentUpdate
	Items    map[string]string
	Revision uint64
}

// Called under a.mu. Child turn lifecycle notifications must not change the
// parent turn's state or resolve its outstanding approvals.
func (a *Adapter) childNotification(message rpcMessage, native string) bool {
	if message.Method == "thread/started" {
		var payload struct {
			Thread struct {
				ID     string
				Source struct {
					SubAgent struct {
						ThreadSpawn struct {
							Parent string `json:"parent_thread_id"`
						} `json:"thread_spawn"`
					} `json:"subAgent"`
				}
			}
		}
		if json.Unmarshal(message.Params, &payload) == nil {
			parent := payload.Thread.Source.SubAgent.ThreadSpawn.Parent
			stream := a.threads[parent]
			if stream == "" {
				stream = a.subagents[parent].Stream
			}
			if stream != "" && payload.Thread.ID != "" {
				a.observeChild(stream, parent, protocol.SubagentUpdate{ID: payload.Thread.ID, NativeID: payload.Thread.ID, Status: "running"})
			}
		}
	}
	child, ok := a.subagents[native]
	if !ok {
		return false
	}
	if message.Method == "item/started" || message.Method == "item/completed" {
		var payload struct{ Item json.RawMessage }
		if json.Unmarshal(message.Params, &payload) == nil {
			if entry := subagent.CodexEntry(payload.Item); entry != nil {
				a.publishChild(child.Stream, protocol.SubagentUpdate{ID: native, Detail: "activity", Entry: entry})
			}
		}
	}
	if message.Method == "turn/started" {
		a.publishChild(child.Stream, protocol.SubagentUpdate{ID: native, Status: "running"})
	}
	if terminalTurnNotification(message.Method) {
		status := "succeeded"
		if message.Method == "turn/failed" {
			status = "failed"
		}
		if message.Method == "turn/cancelled" {
			status = "interrupted"
		}
		var payload struct{ Turn struct{ Status string } }
		if json.Unmarshal(message.Params, &payload) == nil && payload.Turn.Status != "" {
			status = subagent.Status(payload.Turn.Status)
		}
		a.publishChild(child.Stream, protocol.SubagentUpdate{ID: native, Status: status})
	}
	if message.Method == "serverRequest/resolved" {
		var payload struct {
			ID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(message.Params, &payload) == nil {
			delete(a.approve, idKey(payload.ID))
		}
		a.publishChild(child.Stream, protocol.SubagentUpdate{ID: native, Status: "running"})
	}
	for _, update := range subagent.CodexUpdates(message.Params) {
		a.observeChild(child.Stream, native, update)
	}
	return true
}

func (a *Adapter) publishChild(stream string, update protocol.SubagentUpdate) {
	child := a.subagents[update.ID]
	child.Stream = stream
	previous := child.Info
	next := previous
	next.ID = update.ID
	if update.ParentID != "" {
		next.ParentID = update.ParentID
	}
	if update.NativeID != "" {
		next.NativeID = update.NativeID
	}
	if update.Name != "" {
		next.Name = update.Name
	}
	if update.Status != "" {
		next.Status = update.Status
	}
	if update.Prompt != "" {
		next.Prompt = update.Prompt
	}
	if update.Model != "" {
		next.Model = update.Model
	}
	if update.Detail != "" && previous.Detail != "activity" {
		next.Detail = update.Detail
	}
	if !reflect.DeepEqual(previous, next) {
		a.publish(subagent.Event(stream, next))
		child.Info = next
		child.Revision++
	}
	if update.Entry != nil {
		if child.Items == nil {
			child.Items = map[string]string{}
		}
		raw, _ := json.Marshal(update.Entry)
		if child.Items[update.Entry.ID] != string(raw) {
			child.Items[update.Entry.ID] = string(raw)
			child.Revision++
			event := subagent.Event(stream, protocol.SubagentUpdate{ID: update.ID, Entry: update.Entry})
			event.EventID = uuid.NewSHA1(uuid.NameSpaceOID, append([]byte("codex-subagent:"+update.ID), raw...)).String()
			a.publish(event)
		}
	}
	a.subagents[update.ID] = child
}

func (a *Adapter) observeChild(stream, parent string, update protocol.SubagentUpdate) {
	if update.ID == parent {
		return
	}
	update.ParentID = parent
	a.publishChild(stream, update)
	if a.process == nil || a.subagentReads[update.ID] {
		return
	}
	if a.subagentReads == nil {
		a.subagentReads = map[string]bool{}
	}
	a.subagentReads[update.ID] = true
	process := a.process
	go func() {
		failures := 0
		defer func() {
			a.mu.Lock()
			if a.process == process {
				delete(a.subagentReads, update.ID)
			}
			a.mu.Unlock()
		}()
		for {
			a.mu.Lock()
			current := a.process == process
			revision := a.subagents[update.ID].Revision
			a.mu.Unlock()
			if !current {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			raw, err := a.request(ctx, "thread/read", map[string]any{"threadId": update.ID, "includeTurns": true})
			cancel()
			a.mu.Lock()
			if a.process != process {
				a.mu.Unlock()
				return
			}
			if err != nil {
				failures++
				if failures >= 3 {
					status := a.subagents[update.ID].Info.Status
					if status == "running" || status == "queued" {
						a.publishChild(stream, protocol.SubagentUpdate{ID: update.ID, Status: "unknown"})
					}
					a.mu.Unlock()
					return
				}
				a.mu.Unlock()
				time.Sleep(2 * time.Second)
				continue
			}
			failures = 0
			// A live event arrived after this snapshot was requested. Read
			// again so an older partial snapshot cannot replace newer output.
			if revision != a.subagents[update.ID].Revision {
				a.mu.Unlock()
				continue
			}
			var response struct {
				Thread struct {
					Turns []struct {
						ID, Status string
						Items      []json.RawMessage
					}
					Name   string
					Status struct{ Type string }
				}
			}
			if json.Unmarshal(raw, &response) != nil {
				a.mu.Unlock()
				return
			}
			meta := protocol.SubagentUpdate{ID: update.ID, Name: response.Thread.Name, Detail: "activity"}
			if response.Thread.Status.Type == "active" {
				meta.Status = "running"
			}
			if len(response.Thread.Turns) > 0 {
				last := response.Thread.Turns[len(response.Thread.Turns)-1]
				meta.Status = subagent.Status(last.Status)
			}
			a.publishChild(stream, meta)
			for _, turn := range response.Thread.Turns {
				for _, item := range turn.Items {
					entry := subagent.CodexEntry(item)
					if entry == nil {
						continue
					}
					a.publishChild(stream, protocol.SubagentUpdate{ID: update.ID, Entry: entry})
				}
			}
			status := a.subagents[update.ID].Info.Status
			done := status == "succeeded" || status == "failed" || status == "interrupted"
			a.mu.Unlock()
			if done {
				return
			}
			time.Sleep(2 * time.Second)
		}
	}()
}

func (a *Adapter) closeSubagents() {
	for id, child := range a.subagents {
		if child.Info.Status == "running" || child.Info.Status == "queued" || child.Info.Status == "waiting_user" {
			a.publishChild(child.Stream, protocol.SubagentUpdate{ID: id, Status: "unknown"})
		}
	}
}
