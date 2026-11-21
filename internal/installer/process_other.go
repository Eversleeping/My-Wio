//go:build !linux

package installer

import "os/exec"

func configureInstallerProcess(cmd *exec.Cmd) {}
