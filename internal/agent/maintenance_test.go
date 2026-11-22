package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestMaintenanceCollectorsAreIndependentAndSerial(t *testing.T) {
	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var blockedCalls atomic.Int32
	entered := make(chan struct{}, 1)
	ticks := make(chan struct{}, 16)
	client.startMaintenance(ctx,
		maintenanceTask{5 * time.Millisecond, 20 * time.Millisecond, func(context.Context) error {
			blockedCalls.Add(1)
			entered <- struct{}{}
			<-release // Simulate a platform API which does not honor cancellation.
			return nil
		}},
		maintenanceTask{10 * time.Millisecond, time.Second, func(context.Context) error {
			select {
			case ticks <- struct{}{}:
			default:
			}
			return nil
		}},
	)
	defer func() {
		cancel()
		close(release)
		client.workers.Wait()
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("blocked collector did not start")
	}
	for i := 0; i < 5; i++ {
		select {
		case <-ticks:
		case <-time.After(time.Second):
			t.Fatal("one platform probe blocked independent maintenance")
		}
	}
	if blockedCalls.Load() != 1 {
		t.Fatal("stalled collector accumulated concurrent invocations")
	}
}

func TestMaintenanceCollectorsReceiveDeadline(t *testing.T) {
	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); client.workers.Wait() }()
	result := make(chan error, 1)
	client.startMaintenance(ctx, maintenanceTask{time.Hour, 20 * time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		result <- ctx.Err()
		return ctx.Err()
	}})
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not receive a bounded context")
	}
}

func TestMaintenanceSkipsDisconnectedCollection(t *testing.T) {
	client := &Client{}
	calls := 0
	collect := client.connectedCollector(func(context.Context) error { calls++; return nil })
	_ = collect(context.Background())
	if calls != 0 {
		t.Fatal("offline maintenance accumulated telemetry")
	}
	client.controlConnected.Store(true)
	_ = collect(context.Background())
	client.controlConnected.Store(false)
	_ = collect(context.Background())
	if calls != 1 {
		t.Fatal("collector did not follow connection state")
	}
}

func TestMaintenanceWakesWithoutWaitingForNextInterval(t *testing.T) {
	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); client.workers.Wait() }()
	collected := make(chan struct{}, 2)
	wake := client.startMaintenance(ctx, maintenanceTask{time.Hour, time.Second, func(context.Context) error {
		collected <- struct{}{}
		return nil
	}})
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			wake()
		}
		select {
		case <-collected:
		case <-time.After(time.Second):
			t.Fatal("collector did not run promptly after wake")
		}
	}
}
