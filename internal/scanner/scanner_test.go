package scanner

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestDiscoverGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "service")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.email", "test@example.com")
	runGit(t, repository, "config", "user.name", "Wio Test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "commit", "-m", "initial")
	runGit(t, repository, "remote", "add", "origin", "https://example.com/team/service.git")
	inventory, err := Discover(context.Background(), []string{root}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repositories) != 1 {
		t.Fatalf("expected one repository, got %d", len(inventory.Repositories))
	}
	item := inventory.Repositories[0]
	if item.Name != "service" || item.RemoteURL != "https://example.com/team/service.git" || item.Branch == "" || item.CommitSHA == "" || item.Dirty {
		t.Fatalf("unexpected repository: %#v", item)
	}
}

func TestDiscoverSkipsImportStagingDirectories(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, importStagingPrefix+"partial")
	if err := os.MkdirAll(filepath.Join(staging, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	inventory, err := Discover(context.Background(), []string{root}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repositories) != 0 {
		t.Fatalf("import staging repository was discovered: %#v", inventory.Repositories)
	}
}

func TestImportRemovesStagingAfterCloneFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	cloneRoot := filepath.Join(root, "projects")
	destination := filepath.Join(cloneRoot, "service")
	_, err := Import(context.Background(), cloneRoot, protocol.GitImportCommand{
		Name:        "service",
		RemoteURL:   filepath.Join(root, "missing.git"),
		Destination: destination,
	})
	if err == nil {
		t.Fatal("expected clone to fail")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed import left the final destination behind: %v", err)
	}
	assertNoImportStagingDirectories(t, cloneRoot)
	inventory, discoverErr := Discover(context.Background(), []string{cloneRoot}, 10)
	if discoverErr != nil {
		t.Fatal(discoverErr)
	}
	if len(inventory.Repositories) != 0 {
		t.Fatalf("failed import left a discoverable repository: %#v", inventory.Repositories)
	}
}

func TestImportPublishesRepositoryAtFinalDestination(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "test@example.com")
	runGit(t, source, "config", "user.name", "Wio Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "commit", "-m", "initial")

	cloneRoot := filepath.Join(root, "projects")
	destination := filepath.Join(cloneRoot, "service")
	imported, err := Import(context.Background(), cloneRoot, protocol.GitImportCommand{
		Name:        "service",
		RemoteURL:   source,
		Destination: destination,
	})
	if err != nil {
		t.Fatal(err)
	}
	if imported != destination {
		t.Fatalf("import returned %q, want %q", imported, destination)
	}
	if _, err := os.Stat(filepath.Join(destination, ".git")); err != nil {
		t.Fatalf("published repository is missing .git: %v", err)
	}
	assertNoImportStagingDirectories(t, cloneRoot)

	inventory, err := Discover(context.Background(), []string{cloneRoot}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repositories) != 1 {
		t.Fatalf("unexpected imported inventory: %#v", inventory.Repositories)
	}
	destinationInfo, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	discoveredInfo, err := os.Stat(inventory.Repositories[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(destinationInfo, discoveredInfo) {
		t.Fatalf("discovered path %q is not the imported destination %q", inventory.Repositories[0].Path, destination)
	}
}

func TestImportRejectsSymlinksEscapingCloneRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	source := newSourceRepository(t, root)
	cloneRoot := filepath.Join(root, "projects")
	outside := filepath.Join(root, "outside")
	for _, directory := range []string{cloneRoot, outside} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(cloneRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(cloneRoot, "dangling")); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{
		filepath.Join(cloneRoot, "escape", "service"),
		filepath.Join(cloneRoot, "escape", "nested", "service"),
		filepath.Join(cloneRoot, "dangling", "service"),
		"escape/relative",
		cloneRoot,
	} {
		if _, err := Import(context.Background(), cloneRoot, protocol.GitImportCommand{Name: "service", RemoteURL: source, Destination: destination}); err == nil {
			t.Fatalf("import through %s was accepted", destination)
		}
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("import wrote outside the clone root: %v", entries)
	}
}

func TestImportAllowsSymlinksThatStayInsideCloneRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	source := newSourceRepository(t, root)
	realRoot := filepath.Join(root, "data", "projects")
	if err := os.MkdirAll(filepath.Join(realRoot, "group"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Both the clone root and a directory inside it are symbolic links.
	cloneRoot := filepath.Join(root, "projects")
	if err := os.Symlink(realRoot, cloneRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(realRoot, "group"), filepath.Join(realRoot, "alias")); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(cloneRoot, "alias", "nested", "service")
	imported, err := Import(context.Background(), cloneRoot, protocol.GitImportCommand{Name: "service", RemoteURL: source, Destination: destination})
	if err != nil {
		t.Fatal(err)
	}
	if imported != destination {
		t.Fatalf("import returned %q, want %q", imported, destination)
	}
	if _, err := os.Stat(filepath.Join(realRoot, "group", "nested", "service", ".git")); err != nil {
		t.Fatalf("repository was not published inside the clone root: %v", err)
	}
	assertNoImportStagingDirectories(t, filepath.Join(realRoot, "group", "nested"))
}

func TestDiscoverRedactsRemoteCredentials(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repository := newSourceRepository(t, root)
	runGit(t, repository, "remote", "add", "origin", "https://deploy:synthetic-secret@example.com/team/service.git")
	inventory, err := Discover(context.Background(), []string{root}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repositories) != 1 || inventory.Repositories[0].RemoteURL != "https://example.com/team/service.git" {
		t.Fatalf("unexpected inventory: %#v", inventory.Repositories)
	}
}

// TestDiscoverDoesNotRunRepositoryPrograms is the regression test for
// repository configuration (core.fsmonitor, filter drivers) executing
// programs as the agent user during inventory scans.
func TestDiscoverDoesNotRunRepositoryPrograms(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repository, markers := maliciousRepository(t, root)

	// Precondition: plain Git runs the configured programs.
	touchLater(t, repository)
	if output, err := exec.Command("git", "-C", repository, "status", "--porcelain").CombinedOutput(); err != nil {
		t.Fatalf("plain git status: %v: %s", err, output)
	}
	for _, name := range []string{"fsmonitor", "filter"} {
		if _, err := os.Stat(filepath.Join(markers, name)); err != nil {
			t.Fatalf("test repository does not trigger %s: %v", name, err)
		}
	}
	if err := os.RemoveAll(markers); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(markers, 0o755); err != nil {
		t.Fatal(err)
	}

	touchLater(t, repository)
	inventory, err := Discover(context.Background(), []string{root}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repositories) != 1 {
		t.Fatalf("unexpected inventory: %#v", inventory.Repositories)
	}
	item := inventory.Repositories[0]
	if item.RemoteURL != "https://example.com/team/service.git" || item.Branch != "main" || item.CommitSHA == "" || item.Dirty {
		t.Fatalf("unexpected repository metadata: %#v", item)
	}
	assertNoMarkers(t, markers)
}

func TestDiscoverReportsForeignRepositoriesWithoutRunningGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repository, markers := maliciousRepository(t, root)
	previous := repositoryOwned
	var checked []string
	repositoryOwned = func(path string) bool {
		checked = append(checked, path)
		return false
	}
	t.Cleanup(func() { repositoryOwned = previous })

	touchLater(t, repository)
	inventory, err := Discover(context.Background(), []string{root}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repositories) != 1 || len(checked) != 1 {
		t.Fatalf("unexpected inventory: %#v (checked %#v)", inventory.Repositories, checked)
	}
	item := inventory.Repositories[0]
	if item.Name != "service" || item.Path != checked[0] || item.Discovered == "" {
		t.Fatalf("foreign repository was not reported: %#v", item)
	}
	if item.RemoteURL != "" || item.Branch != "" || item.CommitSHA != "" || item.Dirty {
		t.Fatalf("Git ran in a foreign repository: %#v", item)
	}
	assertNoMarkers(t, markers)
}

func maliciousRepository(t *testing.T, root string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("marker programs are shell scripts")
	}
	repository := filepath.Join(root, "service")
	markers := filepath.Join(t.TempDir(), "markers")
	scripts := t.TempDir()
	for _, directory := range []string{repository, markers} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := func(name, body string) string {
		path := filepath.Join(scripts, name)
		content := "#!/bin/sh\ntouch '" + filepath.Join(markers, name) + "'\n" + body + "\n"
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	runGit(t, repository, "init", "-q", "-b", "main")
	runGit(t, repository, "config", "user.email", "test@example.com")
	runGit(t, repository, "config", "user.name", "Wio Test")
	runGit(t, repository, "remote", "add", "origin", "https://example.com/team/service.git")
	if err := os.WriteFile(filepath.Join(repository, ".gitattributes"), []byte("*.txt filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "same.txt"), []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-q", "-m", "initial")
	runGit(t, repository, "config", "core.fsmonitor", script("fsmonitor", "exit 1"))
	runGit(t, repository, "config", "filter.evil.clean", script("filter", "cat"))
	runGit(t, repository, "config", "filter.evil.required", "true")
	return repository, markers
}

var touchClock = time.Now().Add(-48 * time.Hour)

// touchLater changes same.txt's mtime but not its content, so Git compares
// its content (through the clean filter) again.
func touchLater(t *testing.T, repository string) {
	t.Helper()
	touchClock = touchClock.Add(time.Minute)
	if err := os.Chtimes(filepath.Join(repository, "same.txt"), touchClock, touchClock); err != nil {
		t.Fatal(err)
	}
}

func assertNoMarkers(t *testing.T, markers string) {
	t.Helper()
	entries, err := os.ReadDir(markers)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("repository-configured program ran during discovery: %s", entry.Name())
	}
}

func TestWithinRejectsTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if !within(root, filepath.Join(root, "project")) {
		t.Fatal("child path was rejected")
	}
	if within(root, filepath.Join(root, "..", "outside")) {
		t.Fatal("traversal path was accepted")
	}
}

func TestListWorkspaceFilesFiltersGeneratedDirectoriesAndEnforcesRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	for _, directory := range []string{filepath.Join(root, "src"), filepath.Join(root, "node_modules", "package"), filepath.Join(root, ".git")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(root, "README.md"):                       "readme",
		filepath.Join(root, "src", "main.ts"):                  "export {}",
		filepath.Join(root, "node_modules", "package", "x.js"): "ignored",
		filepath.Join(root, ".git", "config"):                  "ignored",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := ListWorkspaceFiles(context.Background(), root, []string{base})
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]string{}
	for _, entry := range result.Files {
		entries[entry.Path] = entry.Kind
	}
	if entries["README.md"] != "file" || entries["src"] != "directory" || entries["src/main.ts"] != "file" {
		t.Fatalf("expected source files were not returned: %#v", entries)
	}
	if _, exists := entries["node_modules"]; exists {
		t.Fatalf("node_modules was included: %#v", entries)
	}
	if _, exists := entries[".git"]; exists {
		t.Fatalf(".git was included: %#v", entries)
	}

	if _, err := ListWorkspaceFiles(context.Background(), root, []string{filepath.Join(base, "other")}); err == nil {
		t.Fatal("workspace outside the configured roots was accepted")
	}
}

func TestListWorkspaceFilesReturnsMoreThan4000Entries(t *testing.T) {
	root := t.TempDir()
	const count = 4001
	for i := 0; i < count; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d.txt", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := ListWorkspaceFiles(context.Background(), root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Truncated || len(result.Files) != count {
		t.Fatalf("expected all %d entries, got %d (truncated=%v)", count, len(result.Files), result.Truncated)
	}
	for i, entry := range result.Files {
		if want := fmt.Sprintf("file-%04d.txt", i); entry.Path != want || entry.Kind != "file" {
			t.Fatalf("entry %d = %#v, want file %q", i, entry, want)
		}
	}
}

func TestReadWorkspaceFileEnforcesWorkspaceAndTextLimits(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.ts"), []byte("export const answer = 42;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.dat"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadWorkspaceFile(context.Background(), root, "src/main.ts", []string{base}, 12)
	if err != nil || result.Path != "src/main.ts" || result.Content != "export const" || !result.Truncated || result.Size != 26 {
		t.Fatalf("unexpected preview: %#v %v", result, err)
	}
	if _, err := ReadWorkspaceFile(context.Background(), root, "../outside.txt", []string{base}, 1024); err == nil {
		t.Fatal("workspace traversal was accepted")
	}
	if _, err := ReadWorkspaceFile(context.Background(), root, "binary.dat", []string{base}, 1024); err == nil {
		t.Fatal("binary file was accepted")
	}

	outside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside-link.txt")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := ReadWorkspaceFile(context.Background(), root, "outside-link.txt", []string{base}, 1024); err == nil {
			t.Fatal("symlink outside the workspace was accepted")
		}
	}
}

func TestReadWorkspaceFilePreviewsImagesByContent(t *testing.T) {
	bitmap := image.NewRGBA(image.Rect(0, 0, 2, 3))
	fixtures := map[string]struct {
		mime string
		data []byte
	}{}
	for _, format := range []string{"jpeg", "png", "gif"} {
		var buffer bytes.Buffer
		var err error
		switch format {
		case "jpeg":
			err = jpeg.Encode(&buffer, bitmap, nil)
		case "png":
			err = png.Encode(&buffer, bitmap)
		case "gif":
			err = gif.Encode(&buffer, bitmap, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		fixtures[format] = struct {
			mime string
			data []byte
		}{"image/" + format, buffer.Bytes()}
	}
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	fixtures["webp"] = struct {
		mime string
		data []byte
	}{"image/webp", webp}
	root := t.TempDir()
	for format, fixture := range fixtures {
		t.Run(format, func(t *testing.T) {
			// A misleading extension cannot turn binary data into editable text.
			path := format + ".txt"
			if err := os.WriteFile(filepath.Join(root, path), fixture.data, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := ReadWorkspaceFile(context.Background(), root, path, []string{root}, 4)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := base64.StdEncoding.DecodeString(result.Content)
			if err != nil || !bytes.Equal(decoded, fixture.data) || result.MIMEType != fixture.mime || result.Encoding != "base64" || result.Truncated || result.Size != int64(len(fixture.data)) {
				t.Fatalf("image bytes or metadata changed: %#v %v", result, err)
			}
		})
	}
	// Images above the text limit still load in full, through the image limit.
	for _, size := range []int{protocol.MaxWorkspaceFileBytes + 1, protocol.MaxWorkspaceImageBytes, protocol.MaxWorkspaceImageBytes + 1} {
		data := make([]byte, size)
		copy(data, fixtures["png"].data)
		if err := os.WriteFile(filepath.Join(root, "large.png"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := ReadWorkspaceFile(context.Background(), root, "large.png", []string{root}, 0)
		if size > protocol.MaxWorkspaceImageBytes {
			if err == nil || !strings.Contains(err.Error(), "4 MB limit") {
				t.Fatalf("oversized image was not rejected: %v", err)
			}
		} else if err != nil || result.Size != int64(size) || result.Truncated {
			t.Fatalf("valid image size was truncated or rejected: %d %v", size, err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, fixtures["png"].data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorkspaceFile(context.Background(), root, "escape.png", []string{root}, 0); err == nil {
		t.Fatal("image symlink escaped the workspace")
	}
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func newSourceRepository(t *testing.T, root string) string {
	t.Helper()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "test@example.com")
	runGit(t, source, "config", "user.name", "Wio Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "commit", "-m", "initial")
	return source
}

func assertNoImportStagingDirectories(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), importStagingPrefix) {
			t.Fatalf("import staging directory was not removed: %s", entry.Name())
		}
	}
}
