package protocol

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Read the declarations rather than maintaining a second hand-written registry:
// adding, deleting, typing or duplicating a constant must also be checked.
func kindConstantValues(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "kinds.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := new(types.Config).Check("protocol", fset, []*ast.File{file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	for _, name := range pkg.Scope().Names() {
		object := pkg.Scope().Lookup(name)
		value, ok := object.(*types.Const)
		if !ok || value.Type() != types.Typ[types.UntypedString] {
			t.Fatalf("%s must be an untyped string constant", name)
		}
		wire := constant.StringVal(value.Val())
		if previous, exists := values[wire]; exists {
			t.Fatalf("%s and %s duplicate wire value %q", previous, name, wire)
		}
		values[wire] = name
	}
	return values
}

func TestKindConstantsMatchGolden(t *testing.T) {
	values := kindConstantValues(t)
	actual := make([]string, 0, len(values))
	for value := range values {
		actual = append(actual, value)
	}
	sort.Strings(actual)
	raw, err := os.ReadFile("testdata/kinds.golden")
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			expected = append(expected, line)
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("wire kinds differ from the pre-refactor baseline\nactual: %q\nexpected: %q", actual, expected)
	}
}

func TestSQLKindLiteralsAreKnown(t *testing.T) {
	values := kindConstantValues(t)
	exceptions := map[string]map[string]string{
		"codex_snapshots.go": {
			// Snapshot cache keys omit the codex operation prefix.
			"mcp.list":        "Codex MCP snapshot cache key",
			"models.list":     "Codex model snapshot cache key",
			"skills.list":     "Codex skill snapshot cache key",
			"status.snapshot": "Codex status snapshot cache key",
		},
		"schema.sql": {
			"compose.yaml": "default deployment Compose filename",
		},
	}
	seenExceptions := make(map[string]bool)
	quoted := regexp.MustCompile(`'([a-z_]+(?:\.[a-zA-Z_]+)+)'`)
	check := func(path, source string) {
		for _, match := range quoted.FindAllStringSubmatch(source, -1) {
			kind := match[1]
			if _, ok := values[kind]; ok {
				continue
			}
			if path == filepath.Join("..", "store", filepath.Base(path)) && exceptions[filepath.Base(path)][kind] != "" {
				seenExceptions[path+":"+kind] = true
				continue
			}
			t.Errorf("%s contains unregistered SQL kind %q", path, kind)
		}
	}
	for _, root := range []string{"../store", "../agentgateway"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			switch filepath.Ext(path) {
			case ".sql":
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				check(path, string(raw))
			case ".go":
				file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
				if err != nil {
					return err
				}
				ast.Inspect(file, func(node ast.Node) bool {
					if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
						value, err := strconv.Unquote(literal.Value)
						if err != nil {
							t.Fatal(err)
						}
						check(path, value)
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for file, kinds := range exceptions {
		for kind := range kinds {
			if !seenExceptions[filepath.Join("..", "store", file)+":"+kind] {
				t.Errorf("unused SQL kind exception %s:%q", file, kind)
			}
		}
	}
}

func TestCredentialOperationKinds(t *testing.T) {
	for _, test := range []struct {
		scope string
		kind  string
	}{
		{"", "credentials.configure"},
		{"codex", "codex.credentials.configure"},
		{"git", "git.credentials.configure"},
		{"gemini", ""},
		{"claude", ""},
		{"Codex", ""},
		{" codex", ""},
		{"codex ", ""},
		{"unknown", ""},
	} {
		t.Run(test.scope, func(t *testing.T) {
			kind, ok := CredentialOperationKind(test.scope)
			if kind != test.kind || ok != (test.kind != "") {
				t.Fatalf("scope %q: got %q, %v; want %q", test.scope, kind, ok, test.kind)
			}
		})
	}
}
