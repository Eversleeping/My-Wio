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
func uninstallPortableLocal(ctx context.Context, _ hostplatform.Layout) error {
	host := localHost{ctx}
	if _, err := run(host, uninstallServicesScript()); err != nil {
		return err
	}
	_, err := run(host, uninstallFilesScript())
	return err
}

func ensurePortableGit(context.Context, string) error { return ErrUnsupportedPlatform }

func installPortableCLIs(context.Context, string, CLIVersions) ([]string, error) {
	return nil, ErrUnsupportedPlatform
}
