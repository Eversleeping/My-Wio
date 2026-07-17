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
	once     sync.Once
	packages map[string]Package
	err      error
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

func (s *Store) load() (map[string]Package, error) {
	s.once.Do(func() {
		s.packages = make(map[string]Package)
		for architecture, filename := range map[string]string{
			"amd64": "wio-agent-linux-amd64",
			"arm64": "wio-agent-linux-arm64",
		} {
			path := filepath.Join(s.assetDir, filename)
			file, err := os.Open(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				s.err = err
				return
			}
			hash := sha256.New()
			size, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				s.err = copyErr
				return
			}
			if closeErr != nil {
				s.err = closeErr
				return
			}
			s.packages[architecture] = Package{Architecture: architecture, Filename: filename, Path: path, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}
		}
		if len(s.packages) == 0 {
			s.err = ErrUnavailable
		}
	})
	return s.packages, s.err
}
