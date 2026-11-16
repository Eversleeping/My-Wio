// Package sandbox applies the complete Linux session boundary before executing
// a CLI. Filesystem mounts, process namespaces and Landlock scopes are required
// together; no failure falls back to an unrestricted process.
package sandbox

import (
	"errors"
	"path/filepath"
	"strings"
)

const Command = "session-sandbox"
const helperPath = "/run/wio-session/helper"
const policyPath = "/run/wio-session/policy.json"

// Mount contains paths and access rights, never upstream authentication.
type Mount struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Writable   bool   `json:"writable,omitempty"`
	Descriptor int    `json:"descriptor,omitempty"`
}

type Policy struct {
	Mounts      []Mount `json:"mounts"`
	WorkDir     string  `json:"work_dir"`
	Descriptors []int   `json:"descriptors,omitempty"`
}

// Options describes the trusted launcher. Root must choose a non-root account
// that already has access to the workspace; launching never changes its owner.
type Options struct {
	Helper string
	Bwrap  string
	UID    uint32
	GID    uint32
}

func (p Policy) Validate() error {
	if len(p.Mounts) == 0 || len(p.Mounts) > 128 || !safePath(p.WorkDir) {
		return errors.New("invalid session sandbox scope")
	}
	for _, mount := range p.Mounts {
		if !safePath(mount.Source) || !safePath(mount.Target) {
			return errors.New("invalid session sandbox mount")
		}
		if mount.Descriptor != 0 && (mount.Descriptor < 3 || mount.Descriptor > 255) {
			return errors.New("invalid session mount descriptor")
		}
	}
	seen := map[int]bool{}
	for _, fd := range p.Descriptors {
		if fd < 3 || fd > 63 || seen[fd] {
			return errors.New("unreviewed session descriptor")
		}
		seen[fd] = true
	}
	return nil
}

func safePath(path string) bool {
	return filepath.IsAbs(path) && path != "/" && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n")
}
