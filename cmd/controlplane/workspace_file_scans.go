package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/wio-platform/wio/internal/store"
)

func runWorkspaceFileScanMaintenance(ctx context.Context, database *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		checkContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := database.ExpireWorkspaceFileScans(checkContext, time.Now()); err != nil && ctx.Err() == nil {
			log.Warn("could not expire workspace file scans", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
