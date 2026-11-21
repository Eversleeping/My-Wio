package childprocess

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// New process launch sites must be reviewed for an explicit filtered Env.
// The Agent self-update is the one intentional full-environment exec.
func TestProcessLaunchAndEnvironmentSourcesAreReviewed(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	expectedEnviron := map[string]int{"internal/childprocess/environment.go": 1, "internal/agent/exec_unix.go": 1}
	expectedCommands := map[string]int{
		"internal/childprocess/childprocess.go":     1,
		"internal/childprocess/platform_windows.go": 1,
		"internal/codexadapter/adapter.go":          1,
		"internal/claudeadapter/adapter.go":         1,
		"internal/claudeadapter/skills.go":          1,
		"internal/geminiadapter/adapter.go":         1,
		"internal/geminiadapter/acp.go":             1,
		"internal/geminiadapter/skills.go":          2,
	}
	environ, commands := map[string]int{}, map[string]int{}
	for _, directory := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && entry.Name() == "testdata" {
				return fs.SkipDir
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			imports := map[string]string{}
			for _, declaration := range file.Imports {
				importPath, err := strconv.Unquote(declaration.Path.Value)
				if err != nil {
					return err
				}
				name := filepath.Base(importPath)
				if declaration.Name != nil {
					name = declaration.Name.Name
				}
				if name == "." && (importPath == "os" || importPath == "os/exec") {
					t.Errorf("dot import bypasses the process environment guard: %s", path)
				}
				imports[name] = importPath
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case imports[pkg.Name] == "os" && selector.Sel.Name == "Environ":
					environ[relative]++
				case imports[pkg.Name] == "os/exec" && (selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext"):
					commands[relative]++
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for label, counts := range map[string]struct{ got, want map[string]int }{"os.Environ": {environ, expectedEnviron}, "exec.Command*": {commands, expectedCommands}} {
		for path, count := range counts.got {
			if counts.want[path] != count {
				t.Errorf("unreviewed %s in %s: %d calls (review Env and update the source guard)", label, path, count)
			}
		}
		for path, count := range counts.want {
			if counts.got[path] != count {
				t.Errorf("%s source list changed in %s: got %d calls, want %d", label, path, counts.got[path], count)
			}
		}
	}
}
