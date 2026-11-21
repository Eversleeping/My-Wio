//go:build !windows

package main

import (
	"errors"
	"log/slog"
)

func runPlatformService(*slog.Logger, string, bool) (bool, error) { return false, nil }

func runCLIService([]string) error { return errors.New("CLI service installation requires Windows") }
