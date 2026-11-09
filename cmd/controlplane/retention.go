package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/store"
)

// Retention is opt-in for audit/operation history; zero or unset means forever.
func retentionPolicyFromEnv() (store.RetentionPolicy, error) {
	var policy store.RetentionPolicy
	for name, target := range map[string]*time.Duration{
		"WIO_AUDIT_RETENTION_DAYS":     &policy.AuditLog,
		"WIO_OPERATION_RETENTION_DAYS": &policy.OperationHistory,
	} {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		days, err := strconv.Atoi(raw)
		if err != nil || days < 0 || int64(days) > int64((time.Duration(1<<63-1))/(24*time.Hour)) {
			return policy, fmt.Errorf("%s must be a non-negative number of days", name)
		}
		*target = time.Duration(days) * 24 * time.Hour
	}
	return policy, nil
}

func postgresPoolLimitFromEnv() (int, error) {
	raw := strings.TrimSpace(os.Getenv("WIO_POSTGRES_MAX_OPEN_CONNS"))
	if raw == "" {
		return 20, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 0, fmt.Errorf("WIO_POSTGRES_MAX_OPEN_CONNS must be positive")
	}
	return limit, nil
}

func runDataRetention(ctx context.Context, database *store.Store, policy store.RetentionPolicy, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := database.PruneRetainedData(sweepCtx, time.Now(), policy)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Warn("could not prune retained data", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
