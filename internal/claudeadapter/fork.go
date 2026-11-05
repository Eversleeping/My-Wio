package claudeadapter

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

func SnapshotPath(stateDir, id string) (string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", errors.New("invalid Claude snapshot ID")
	}
	return filepath.Join(stateDir, "claude", "forks", id+".jsonl"), nil
}

// Snapshot preserves the complete native transcript. Claude itself interprets
// it via --resume, so Wio does not rewrite undocumented transcript fields.
func Snapshot(ctx context.Context, stateDir string, c protocol.ClaudeForkCommand) (protocol.ClaudeForkResult, error) {
	result := protocol.ClaudeForkResult{SnapshotID: c.TargetThreadID}
	target, err := SnapshotPath(stateDir, c.TargetThreadID)
	if err != nil {
		return result, err
	}
	if _, err = uuid.Parse(c.SessionID); err != nil {
		return result, errors.New("invalid Claude session ID")
	}
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return result, errors.New("invalid snapshot file")
		}
		return result, nil
	}
	config := os.Getenv("CLAUDE_CONFIG_DIR")
	if config == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return result, e
		}
		config = filepath.Join(home, ".claude")
	}
	var source string
	err = filepath.WalkDir(filepath.Join(config, "projects"), func(path string, d fs.DirEntry, e error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == c.SessionID+".jsonl" && d.Type().IsRegular() {
			if source != "" {
				return errors.New("ambiguous Claude transcript")
			}
			source = path
		}
		return nil
	})
	if err != nil || source == "" {
		return result, errors.New("Claude transcript not found; check the Agent account and Claude configuration directory")
	}
	in, err := os.Open(source)
	if err != nil {
		return result, err
	}
	defer in.Close()
	stat, err := in.Stat()
	if err != nil {
		return result, err
	}
	if !stat.Mode().IsRegular() || stat.Size() == 0 || stat.Size() > 64<<20 {
		return result, errors.New("Claude transcript is empty or too large")
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return result, err
	}
	out, err := os.CreateTemp(filepath.Dir(target), "snapshot-*.tmp")
	if err != nil {
		return result, err
	}
	defer os.Remove(out.Name())
	if _, err = io.CopyN(out, in, stat.Size()); err != nil {
		out.Close()
		return result, err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return result, err
	}
	if err = out.Close(); err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	err = os.Rename(out.Name(), target)
	return result, err
}
