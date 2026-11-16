//go:build !linux

package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func ABI() (int, error)                                  { return 0, errors.New("session isolation requires Linux") }
func Run([]string) error                                 { _, err := ABI(); return err }
func Probe(context.Context, Options) error               { _, err := ABI(); return err }
func Prepare(*exec.Cmd, Policy, Options) (func(), error) { _, err := ABI(); return nil, err }
func SystemMounts() []Mount                              { return nil }
func DescriptorFile([]byte) (*os.File, error)            { _, err := ABI(); return nil, err }
