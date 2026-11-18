// Package taskartifact verifies the bounded, portable content of task artifacts.
package taskartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wio-platform/wio/internal/protocol"
)

func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hexID(value string, lengths ...int) bool {
	for _, length := range lengths {
		if len(value) == length {
			_, err := hex.DecodeString(value)
			return err == nil && strings.ToLower(value) == value
		}
	}
	return false
}

func Paths(paths []string) error {
	if len(paths) == 0 || len(paths) > protocol.MaxArtifactFiles {
		return errors.New("select between 1 and 64 files")
	}
	seen := make(map[string]bool, len(paths))
	for _, name := range paths {
		if len(name) > 512 || !utf8.ValidString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") ||
			strings.ContainsAny(name, "\\:*?\"<>|") || strings.IndexFunc(name, unicode.IsControl) != -1 {
			return errors.New("invalid artifact path")
		}
		for _, part := range strings.Split(strings.ToLower(name), "/") {
			// Never carry Git metadata, platform aliases, or common local
			// credential files even when a project has tracked them.
			if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part ||
				part == ".git" || part == ".env" || strings.HasPrefix(part, ".env.") || part == ".ssh" ||
				part == ".netrc" || part == ".npmrc" || part == ".git-credentials" ||
				strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") {
				return errors.New("artifact path is reserved or sensitive")
			}
		}
		folded := strings.ToLower(name)
		if seen[folded] {
			return errors.New("duplicate artifact path")
		}
		seen[folded] = true
	}
	for name := range seen {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return errors.New("artifact paths cannot contain one another")
			}
		}
	}
	return nil
}

func validFile(mode, hash string) bool {
	return (mode == "" && hash == "") || ((mode == "100644" || mode == "100755") && hexID(hash, 64))
}

func Validate(manifest protocol.ArtifactManifest) (string, error) {
	if !hexID(manifest.BaseSHA, 40, 64) {
		return "", errors.New("invalid artifact Git baseline")
	}
	size := 0
	paths := make([]string, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		paths = append(paths, file.Path)
		size += len(file.Content)
		if size > protocol.MaxArtifactBytes || !validFile(file.BeforeMode, file.BeforeHash) || !validFile(file.AfterMode, file.AfterHash) ||
			(file.AfterMode == "" && len(file.Content) != 0) ||
			(file.AfterMode != "" && Hash(file.Content) != file.AfterHash) ||
			(file.BeforeMode == file.AfterMode && file.BeforeHash == file.AfterHash) {
			return "", errors.New("invalid artifact file content or digest")
		}
	}
	if err := Paths(paths); err != nil {
		return "", err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return Hash(raw), nil
}
