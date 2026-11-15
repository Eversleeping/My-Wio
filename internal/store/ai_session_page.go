package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/wio-platform/wio/internal/session"
)

// The cursor stores the ordering values, never an offset or a row lookup. It
// remains usable after its anchor is deleted and is bound to the active filter.
type sessionCursor struct {
	Version         int        `json:"v"`
	Filter          string     `json:"filter"`
	ProjectPinnedAt *time.Time `json:"project_pin"`
	PinnedAt        *time.Time `json:"pin"`
	UpdatedAt       time.Time  `json:"updated"`
	Engine          string     `json:"engine"`
	ID              string     `json:"id"`
}

func sessionFilterKey(filter session.Filter) string {
	raw, _ := json.Marshal(struct {
		Engine   string
		Archived *bool
		Query    string
	}{filter.Engine, filter.Archived, filter.Query})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func cursorTime(at *time.Time) any {
	if at == nil {
		return time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return at.UTC()
}
func cursorFlag(at *time.Time) int {
	if at != nil {
		return 1
	}
	return 0
}
func (s *Store) AISessionPage(ctx context.Context, filter session.Filter) (session.Page, error) {
	page := session.Page{Items: []session.Record{}, Groups: []session.Group{}}
	where, args, err := aiSessionConditions(filter)
	if err != nil {
		return page, err
	}
	limit := filter.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return page, invalidInput(errors.New("limit must be between 1 and 100"))
	}
	key := sessionFilterKey(filter)
	var cursor sessionCursor
	if filter.Cursor != "" {
		if len(filter.Cursor) > 4096 {
			return page, invalidInput(errors.New("invalid session cursor"))
		}
		raw, e := base64.RawURLEncoding.DecodeString(filter.Cursor)
		if e != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Version != 1 || cursor.Filter != key || !session.ValidEngine(cursor.Engine) || cursor.ID == "" || cursor.UpdatedAt.IsZero() {
			return page, invalidInput(errors.New("invalid session cursor"))
		}
	}
	// Read declared timestamp columns directly: aggregate timestamp expressions
	// lose SQLite's time scanner metadata. This pass reads only group metadata.
	var groups []session.Group
	if err = s.DB.SelectContext(ctx, &groups, s.Q("SELECT workspace_id,updated_at,project_pinned_at FROM ("+aiSessionRows+") sessions WHERE "+where), args...); err != nil {
		return page, err
	}
	byWorkspace := map[string]*session.Group{}
	for _, row := range groups {
		g := byWorkspace[row.WorkspaceID]
		if g == nil {
			g = &session.Group{WorkspaceID: row.WorkspaceID, ProjectPinnedAt: row.ProjectPinnedAt}
			byWorkspace[row.WorkspaceID] = g
		}
		g.MatchedCount++
		if row.LatestUpdatedAt.After(g.LatestUpdatedAt) {
			g.LatestUpdatedAt = row.LatestUpdatedAt
		}
	}
	for _, g := range byWorkspace {
		page.Groups = append(page.Groups, *g)
	}
	sort.Slice(page.Groups, func(i, j int) bool { return page.Groups[i].WorkspaceID < page.Groups[j].WorkspaceID })
	if filter.Cursor != "" {
		where += ` AND (CASE WHEN project_pinned_at IS NULL THEN 0 ELSE 1 END,COALESCE(project_pinned_at,?),CASE WHEN pinned_at IS NULL THEN 0 ELSE 1 END,COALESCE(pinned_at,?),updated_at,engine,id)<(?,?,?,?,?,?,?)`
		args = append(args, cursorTime(nil), cursorTime(nil), cursorFlag(cursor.ProjectPinnedAt), cursorTime(cursor.ProjectPinnedAt), cursorFlag(cursor.PinnedAt), cursorTime(cursor.PinnedAt), cursor.UpdatedAt.UTC(), cursor.Engine, cursor.ID)
	}
	args = append(args, limit+1)
	query := "SELECT * FROM (" + aiSessionRows + ") sessions WHERE " + where + " ORDER BY " + aiSessionOrder + " LIMIT ?"
	if err = s.DB.SelectContext(ctx, &page.Items, s.Q(query), args...); err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		raw, _ := json.Marshal(sessionCursor{1, key, last.ProjectPinnedAt, last.PinnedAt, last.UpdatedAt, last.Engine, last.ID})
		value := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &value
	}
	for i := range page.Items {
		decorateAISession(&page.Items[i])
	}
	return page, nil
}
