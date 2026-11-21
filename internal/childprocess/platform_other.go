//go:build !windows

package childprocess

import "os/exec"

func configurePlatformCommand(*exec.Cmd) {}
