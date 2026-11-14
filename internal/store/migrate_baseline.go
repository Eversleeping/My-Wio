package store

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// migrateBaseline preserves the pre-versioning startup sequence. It is
// deliberately outside a transaction because the legacy helpers use *sqlx.DB.
func migrateBaseline(ctx context.Context, db *sqlx.DB, driver string) error {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	if err := migrateUserAuthMode(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateUserTOTPCounter(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateImportedProjectRemotes(ctx, db); err != nil {
		return err
	}
	if err := migrateCredentialProfileIdentity(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateGeminiModelConfig(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateClaudeModelConfig(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateGeminiThinking(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateProjectThreadPreferences(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateWorkspaceMetadata(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateWorkspaceFilePreviewMedia(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateProjectWorkspaceOperations(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateSecretSetVersions(ctx, db); err != nil {
		return err
	}
	if err := migrateManagedWorkspacePaths(ctx, db); err != nil {
		return err
	}
	if err := migrateDeploymentSources(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateDeploymentPublicURL(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateRepairEnrollments(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateServerControlPlane(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateServerGemini(ctx, db, driver); err != nil {
		return err
	}
	if err := (&Store{DB: db, driver: driver}).purgeRevokedServers(ctx); err != nil {
		return fmt.Errorf("purge revoked servers: %w", err)
	}
	if err := migrateServerGitCredentialProfiles(ctx, db, driver); err != nil {
		return err
	}
	if driver == "pgx" {
		if _, err := db.ExecContext(ctx, `ALTER TABLE metric_rollups
			ALTER COLUMN net_rx_bytes TYPE BIGINT USING net_rx_bytes::BIGINT,
			ALTER COLUMN net_tx_bytes TYPE BIGINT USING net_tx_bytes::BIGINT`); err != nil {
			return fmt.Errorf("migrate metric counters: %w", err)
		}
	}
	if err := migrateMetricCapacity(ctx, db, driver); err != nil {
		return err
	}
	if err := migrateRetentionAndCodexRequests(ctx, db); err != nil {
		return err
	}
	return nil
}
