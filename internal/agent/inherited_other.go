//go:build !linux && !darwin

package agent

import "log/slog"

// Windows workers are replaced by their service supervisor, which owns their process job.
func ReapInheritedChildren(*slog.Logger) int { return 0 }
