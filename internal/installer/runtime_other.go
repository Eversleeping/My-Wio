//go:build !windows

package installer

import "context"

func PrepareRuntime(context.Context, string, string) error { return nil }
