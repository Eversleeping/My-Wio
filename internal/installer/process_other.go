//go:build !linux && !darwin

package installer

import "os/exec"

func configureInstallerProcess(cmd *exec.Cmd) {}
