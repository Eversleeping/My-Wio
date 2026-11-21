//go:build !windows

package childprocess

import "os/exec"

func Start(cmd *exec.Cmd) error { return cmd.Start() }
func Wait(cmd *exec.Cmd) error  { return cmd.Wait() }
func Run(cmd *exec.Cmd) error   { return cmd.Run() }
