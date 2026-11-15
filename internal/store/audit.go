package store

import (
	"context"
	"encoding/json"
	"time"
)

func (s *Store) Audit(ctx context.Context, userID, action, resourceType, resourceID string, detail any, ip string) error {
	raw, _ := json.Marshal(detail)
	_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO audit_log(id,user_id,action,resource_type,resource_id,detail,ip_address) VALUES(?,?,?,?,?,?,?)"), NewID(), userID, action, resourceType, resourceID, string(raw), ip)
	return err
}

type AuditRecord struct {
	ID           string    `db:"id"`
	Action       string    `db:"action"`
	ResourceType string    `db:"resource_type"`
	ResourceID   string    `db:"resource_id"`
	Detail       string    `db:"detail"`
	IPAddress    string    `db:"ip_address"`
	OccurredAt   time.Time `db:"occurred_at"`
}

type AuditPage struct {
	Items   []AuditRecord
	HasMore bool
}

func (s *Store) ListAuditPage(ctx context.Context, limit, offset int) (AuditPage, error) {
	items := make([]AuditRecord, 0, limit)
	err := s.DB.SelectContext(ctx, &items, s.Q(`SELECT id,action,resource_type,resource_id,detail,ip_address,occurred_at
		FROM audit_log ORDER BY occurred_at DESC,id DESC LIMIT ? OFFSET ?`), limit+1, offset)
	if err != nil {
		return AuditPage{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return AuditPage{Items: items, HasMore: hasMore}, nil
}
