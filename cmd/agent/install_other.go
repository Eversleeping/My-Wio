//go:build !linux

package main

import (
	"context"
	"errors"
)

func lockAgentInstaller() (func(), error) { return nil, errors.New("unsupported platform") }
func readInstallationCode(ctx context.Context) (string, error) {
	return "", errors.New("unsupported platform")
}
