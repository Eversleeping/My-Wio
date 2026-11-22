package agent

import "golang.org/x/sys/windows"

func commitCredentialFile(temporary, path string) error {
	source, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	// FlushFileBuffers on a read-only directory handle fails on Windows. The
	// temporary file has already been flushed and closed; publish it through
	// Windows' replace-existing, write-through move instead of Unix directory fsync.
	return windows.MoveFileEx(source, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
