package agent

import (
	"errors"
	"golang.org/x/sys/windows/svc"
	"os"
)

// The immutable service supervisor owns worker replacement. Exiting only after
// the operation result has flushed avoids overwriting a live executable.
func execAgentBinary(string) error {
	interactive, err := svc.IsAnInteractiveSession()
	if err != nil {
		return err
	}
	if interactive {
		return errors.New("Windows self-update requires the WioAgent service")
	}
	os.Exit(75)
	return nil
}
