package agent

import (
	"context"
	"time"
)

// The control plane sends a downlink keepalive every 20 seconds. Heartbeat
// writes alone cannot detect a half-open TCP connection, so bound read silence.
func watchControlTraffic(ctx context.Context, cancel context.CancelFunc, traffic <-chan struct{}, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-traffic:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		case <-timer.C:
			cancel()
			return
		}
	}
}
