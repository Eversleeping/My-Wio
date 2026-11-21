//go:build !linux && !darwin && !windows

package agent

import "errors"

func execAgentBinary(string) error {
	return errors.New("Agent process replacement is supported only on Linux")
}
