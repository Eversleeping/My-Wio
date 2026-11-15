package session

import "time"

type Filter struct {
	Engine   string
	Archived *bool // nil selects both views; HTTP chooses its own active default.
	Query    string
	Cursor   string
	Limit    int // zero leaves internal callers unbounded
}

type Group struct {
	WorkspaceID     string     `db:"workspace_id" json:"workspace_id"`
	MatchedCount    int        `json:"matched_count"`
	LatestUpdatedAt time.Time  `db:"updated_at" json:"latest_updated_at"`
	ProjectPinnedAt *time.Time `db:"project_pinned_at" json:"project_pinned_at"`
}
type Page struct {
	Items      []Record `json:"items"`
	NextCursor *string  `json:"next_cursor"`
	Groups     []Group  `json:"groups"`
}
