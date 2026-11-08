//go:build !linux

package agent

import "log/slog"

// ReapInheritedChildren is only needed where the Agent replaces itself with
// syscall.Exec, which is Linux-only.
func ReapInheritedChildren(*slog.Logger) int { return 0 }
