package main

import (
	"testing"
	"time"
)

func TestRetentionConfigurationDefaultsAndOptIn(t *testing.T) {
	t.Setenv("WIO_AUDIT_RETENTION_DAYS", "")
	t.Setenv("WIO_OPERATION_RETENTION_DAYS", "")
	policy, err := retentionPolicyFromEnv()
	if err != nil || policy.AuditLog != 0 || policy.OperationHistory != 0 {
		t.Fatalf("defaults: %#v %v", policy, err)
	}
	t.Setenv("WIO_AUDIT_RETENTION_DAYS", "90")
	t.Setenv("WIO_OPERATION_RETENTION_DAYS", "30")
	policy, err = retentionPolicyFromEnv()
	if err != nil || policy.AuditLog != 90*24*time.Hour || policy.OperationHistory != 30*24*time.Hour {
		t.Fatalf("opt-in: %#v %v", policy, err)
	}
	for _, raw := range []string{"-1", "bad", "99999999999999999999"} {
		t.Setenv("WIO_AUDIT_RETENTION_DAYS", raw)
		if _, err := retentionPolicyFromEnv(); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestPostgresPoolLimitConfiguration(t *testing.T) {
	for raw, want := range map[string]int{"": 20, "3": 3, "0": 0, "-1": 0, "bad": 0} {
		t.Setenv("WIO_POSTGRES_MAX_OPEN_CONNS", raw)
		got, err := postgresPoolLimitFromEnv()
		if want == 0 {
			if err == nil {
				t.Fatalf("accepted %q", raw)
			}
			continue
		}
		if err != nil || got != want {
			t.Fatalf("%q: got %d %v", raw, got, err)
		}
	}
}
