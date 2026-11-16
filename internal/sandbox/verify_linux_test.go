//go:build linux

package sandbox

func verifyChildPrivileges() error { return verifyProcess() }
