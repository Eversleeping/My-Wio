//go:build !linux

package sandbox

func verifyChildPrivileges() error { _, err := ABI(); return err }
