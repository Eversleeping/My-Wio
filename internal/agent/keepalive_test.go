package agent

import (
	"context"
	"testing"
	"time"
)

func TestControlTrafficWatchdogCancelsSilentConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchControlTraffic(ctx, cancel, make(chan struct{}), 20*time.Millisecond)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("silent control stream was not cancelled")
	}
}

func TestControlTrafficWatchdogResetsOnDownlinkMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	traffic := make(chan struct{})
	go watchControlTraffic(ctx, cancel, traffic, 200*time.Millisecond)
	for i := 0; i < 3; i++ {
		select {
		case <-ctx.Done():
			t.Fatal("watchdog cancelled a live stream")
		case <-time.After(120 * time.Millisecond):
		}
		select {
		case traffic <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("watchdog stopped accepting live traffic")
		}
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("watchdog did not expire after downlink traffic stopped")
	}
}
