package store

import (
	"context"
	"time"
)

type Alert struct {
	ID             string     `db:"id" json:"id"`
	ServerID       string     `db:"server_id" json:"server_id"`
	Kind           string     `db:"kind" json:"kind"`
	Severity       string     `db:"severity" json:"severity"`
	Title          string     `db:"title" json:"title"`
	Detail         string     `db:"detail" json:"detail"`
	Status         string     `db:"status" json:"status"`
	ServerName     string     `db:"server_name" json:"server_name"`
	OpenedAt       time.Time  `db:"opened_at" json:"opened_at"`
	ResolvedAt     *time.Time `db:"resolved_at" json:"resolved_at"`
	AcknowledgedAt *time.Time `db:"acknowledged_at" json:"acknowledged_at"`
}

func (s *Store) ListAlerts(ctx context.Context) ([]Alert, error) {
	var out []Alert
	err := s.DB.SelectContext(ctx, &out, `SELECT a.id,COALESCE(a.server_id,'') server_id,a.kind,a.severity,a.title,a.detail,a.status,COALESCE(s.name,'') server_name,a.opened_at,a.resolved_at,a.acknowledged_at FROM alerts a LEFT JOIN servers s ON s.id=a.server_id ORDER BY CASE a.status WHEN 'open' THEN 0 ELSE 1 END,a.opened_at DESC LIMIT 200`)
	return out, err
}
