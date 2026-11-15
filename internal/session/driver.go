package session

import (
	"context"
	"fmt"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// Driver adapts native persistence and scheduling to the shared control plane.
// Optional protocols are exposed through small consumer-owned interfaces.
type Driver interface {
	Engine() Engine
	Session(context.Context, string) (Record, error)
	Native(context.Context, string) (any, error)
	Events(context.Context, string, EventQuery) ([]protocol.StreamEvent, error)
	DefaultScheduleMode() string
	ValidateSchedule(context.Context, ScheduledTaskInput) error
	EnqueueScheduled(context.Context, ScheduledTask, time.Time)
}
type Registry struct{ drivers map[Engine]Driver }

func NewRegistry(drivers ...Driver) *Registry {
	r := &Registry{drivers: map[Engine]Driver{}}
	for _, d := range drivers {
		if d == nil || !ValidEngine(d.Engine()) {
			panic("invalid session driver")
		}
		if _, ok := r.drivers[d.Engine()]; ok {
			panic("duplicate session driver")
		}
		r.drivers[d.Engine()] = d
	}
	return r
}
func (r *Registry) Driver(engine Engine) (Driver, error) {
	if r != nil {
		if d, ok := r.drivers[engine]; ok {
			return d, nil
		}
	}
	return nil, fmt.Errorf("unsupported session engine %q", engine)
}

// LookupError preserves the HTTP distinction between invalid input and failed
// persistence lookups while keeping transport concerns outside the drivers.
type LookupError struct{ Err error }

func (e LookupError) Error() string { return e.Err.Error() }
func (e LookupError) Unwrap() error { return e.Err }
