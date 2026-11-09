package agentupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
)

var ErrUnavailable = errors.New("agent update package is unavailable")

type Package struct {
	Architecture string
	Filename     string
	Path         string
	SHA256       string
	Size         int64
}

type Store struct {
	assetDir string
	mu       sync.Mutex
	files    map[string]os.FileInfo
	packages map[string]Package
}

func New(assetDir string) *Store {
	if assetDir == "" {
		assetDir = "/usr/local/share/wio"
	}
	return &Store{assetDir: assetDir}
}

func (s *Store) Command() (protocol.AgentUpdateCommand, error) {
	packages, err := s.load()
	if err != nil {
		return protocol.AgentUpdateCommand{}, err
	}
	descriptors := make(map[string]protocol.AgentUpdatePackage, len(packages))
	for architecture, pkg := range packages {
		descriptors[architecture] = protocol.AgentUpdatePackage{
			URL:    "/api/agent/update-package/" + architecture + "?version=" + url.QueryEscape(buildinfo.Version),
			SHA256: pkg.SHA256,
			Size:   pkg.Size,
		}
	}
	return protocol.AgentUpdateCommand{Version: buildinfo.Version, Packages: descriptors}, nil
}

func (s *Store) Package(architecture string) (Package, error) {
	packages, err := s.load()
	if err != nil {
		return Package{}, err
	}
	pkg, ok := packages[architecture]
	if !ok {
		return Package{}, fmt.Errorf("%w: %s", ErrUnavailable, architecture)
	}
	return pkg, nil
}

func (s *Store) load() (result map[string]Package, loadErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if loadErr != nil {
			s.packages, s.files = nil, nil
		}
	}()
	packages := make(map[string]Package)
	files := make(map[string]os.FileInfo)
	for architecture, filename := range map[string]string{
		"amd64": "wio-agent-linux-amd64",
		"arm64": "wio-agent-linux-arm64",
	} {
		path := filepath.Join(s.assetDir, filename)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("Agent update package is not a regular file: %s", path)
		}
		if old := s.files[architecture]; old != nil && samePackageFile(old, info) {
			packages[architecture] = s.packages[architecture]
			files[architecture] = info
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		opened, statErr := file.Stat()
		if statErr != nil {
			file.Close()
			return nil, statErr
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, file)
		after, statErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if statErr != nil {
			return nil, statErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		current, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if size != opened.Size() || !samePackageFile(opened, after) || !samePackageFile(after, current) {
			return nil, errors.New("Agent update package changed while reading; retry the request")
		}
		packages[architecture] = Package{Architecture: architecture, Filename: filename, Path: path, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}
		files[architecture] = current
	}
	if len(packages) == 0 {
		return nil, ErrUnavailable
	}
	// Publish only a complete, successful snapshot. Errors are retried on the
	// next call, and every call checks for additions, removals and replacements.
	s.packages, s.files = packages, files
	return packages, nil
}

func samePackageFile(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}
