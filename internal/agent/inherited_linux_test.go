//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestParentPIDHandlesUnusualCommandNames(t *testing.T) {
	for _, test := range []struct {
		stat string
		ppid int
		ok   bool
	}{
		{"41655 (sh) Z 41653 41653 1 0 -1", 41653, true},
		{"12 (node (worker) x) S 7 12 12 0", 7, true},
		{"13 (a b) ) R 99 1", 99, true},
		{"14 (broken", 0, false},
		{"15 (sh) Z", 0, false},
	} {
		ppid, ok := parentPID([]byte(test.stat))
		if ppid != test.ppid || ok != test.ok {
			t.Errorf("parentPID(%q) = %d, %v; want %d, %v", test.stat, ppid, ok, test.ppid, test.ok)
		}
	}
}

func TestInheritedChildrenSelectsOnlyDirectChildren(t *testing.T) {
	root := t.TempDir()
	write := func(name, stat string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("100", "100 (wio-agent) S 1 100 100 0")
	write("201", "201 (codex) Z 100 100 100 0")
	write("202", "202 (node) Z 100 100 100 0")
	write("203", "203 (bash) S 201 100 100 0")
	write("204", "204 (other) S 1 204 204 0")
	write("self", "100 (wio-agent) S 1 100 100 0")
	if err := os.MkdirAll(filepath.Join(root, "300"), 0o755); err != nil {
		t.Fatal(err)
	}
	children := inheritedChildren(root, 100)
	sort.Ints(children)
	if want := []int{201, 202}; !reflect.DeepEqual(children, want) {
		t.Fatalf("inheritedChildren = %v, want %v", children, want)
	}
}
