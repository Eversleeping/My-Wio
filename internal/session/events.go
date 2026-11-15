package session

// EventQuery is shared by all engines. Absent cursors select the recent window.
type EventQuery struct {
	After, Before *int64
	Limit         int
	Raw           bool
}
