//go:build !unix

package gitrepository

import "os"

// fileOwner cannot determine ownership on this platform, so repositories are
// never treated as owned by the agent user and Git always runs hardened.
func fileOwner(os.FileInfo) (int, bool) {
	return 0, false
}
