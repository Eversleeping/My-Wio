package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"io"
	"os"
)

func installationPreflight() error { return nil }
func lockAgentInstaller() (func(), error) {
	name, _ := windows.UTF16PtrFromString(`Global\WioAgentInstaller`)
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return nil, errors.New("another Agent installation is running")
	}
	wait, err := windows.WaitForSingleObject(h, 0)
	if err != nil || (wait != windows.WAIT_OBJECT_0 && wait != windows.WAIT_ABANDONED) {
		windows.CloseHandle(h)
		return nil, errors.New("another Agent installation is running")
	}
	return func() { windows.ReleaseMutex(h); windows.CloseHandle(h) }, nil
}
func readInstallationCode(ctx context.Context) (string, error) {
	tty, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer tty.Close()
	h := windows.Handle(tty.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return "", err
	}
	if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return "", err
	}
	defer windows.SetConsoleMode(h, mode)
	stop := context.AfterFunc(ctx, func() { windows.SetConsoleMode(h, mode); tty.Close() })
	defer stop()
	fmt.Fprint(os.Stdout, "One-time registration code: ")
	code, err := bufio.NewReader(io.LimitReader(tty, 257)).ReadString('\n')
	fmt.Fprintln(os.Stdout)
	if err != nil || len(code) > 256 {
		return "", errors.New("invalid registration code")
	}
	return code, nil
}
