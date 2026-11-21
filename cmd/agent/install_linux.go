package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"syscall"
)

func lockAgentInstaller() (func(), error) {
	directory := "/run/wio-installer"
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("installer lock directory must be private")
	}
	file, err := os.OpenFile(directory+"/install.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another Agent installation is running")
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}
func readInstallationCode(ctx context.Context) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", errors.New("open an interactive terminal to enter the one-time registration code")
	}
	defer tty.Close()
	state, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
	if err != nil {
		return "", err
	}
	hidden := *state
	hidden.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(int(tty.Fd()), unix.TCSETS, &hidden); err != nil {
		return "", err
	}
	defer unix.IoctlSetTermios(int(tty.Fd()), unix.TCSETS, state)
	stop := context.AfterFunc(ctx, func() { _ = unix.IoctlSetTermios(int(tty.Fd()), unix.TCSETS, state); _ = tty.Close() })
	defer stop()
	fmt.Fprint(tty, "One-time registration code: ")
	code, err := bufio.NewReader(io.LimitReader(tty, 257)).ReadString('\n')
	fmt.Fprintln(tty)
	if err != nil || len(code) > 256 {
		return "", errors.New("invalid registration code")
	}
	return code, nil
}
