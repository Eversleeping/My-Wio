// Package session defines the shared AI session contract. It must not depend
// on Store: both persistence and Agent collaboration import these wire types.
package session

// Engine remains a string alias so existing collaboration callers can keep
// assigning protocol strings without conversions or JSON changes.
type Engine = string

const (
	Codex  Engine = "codex"
	Gemini Engine = "gemini"
	Claude Engine = "claude"
)

type Ref struct {
	Engine Engine `json:"engine"`
	ID     string `json:"id"`
}

func ValidEngine(engine Engine) bool { return engine == Codex || engine == Gemini || engine == Claude }
func (r Ref) Valid() bool            { return ValidEngine(r.Engine) && r.ID != "" }
func (r Ref) Key() string            { return r.Engine + ":" + r.ID }
