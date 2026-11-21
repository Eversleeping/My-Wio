//go:build !darwin && !windows && !linux

package installer

import (
	"context"
	"github.com/wio-platform/wio/internal/hostplatform"
)

func preparePortableService(context.Context, hostplatform.Layout) error {
	return ErrUnsupportedPlatform
}
func protectPortableState(context.Context, hostplatform.Layout, string) error {
	return ErrUnsupportedPlatform
}
func startPortableService(context.Context, hostplatform.Layout) error { return ErrUnsupportedPlatform }
func ensurePortableNode(context.Context) error                        { return ErrUnsupportedPlatform }
func uninstallPortableLocal(context.Context, hostplatform.Layout) error {
	return ErrUnsupportedPlatform
}

func ensurePortableGit(context.Context) error { return ErrUnsupportedPlatform }

func installPortableCLIs(context.Context, string, CLIVersions) ([]string, error) {
	return nil, ErrUnsupportedPlatform
}
