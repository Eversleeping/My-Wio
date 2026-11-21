package hostplatform

import "testing"

func TestTargetPathSemantics(t *testing.T) {
	for _, tc := range []struct {
		os, raw, want string
		abs           bool
	}{
		{"linux", "/srv/a/../repo", "/srv/repo", true},
		{"darwin", "/Users/a/My Repo/../work", "/Users/a/work", true},
		{"windows", `c:\Projects\a\..\repo`, "C:/Projects/repo", true},
		{"windows", `\\host\share\..\..\repo`, "//host/share/repo", true},
		{"windows", `C:\`, "C:/", true},
		{"windows", `C:relative`, "C:relative", false},
		{"windows", `\rooted`, "/rooted", false},
		{"windows", `\\?\C:\repo`, "//?/C:/repo", false},
		{"windows", `C:\repo:stream`, "C:/repo:stream", false},
	} {
		if got := Clean(tc.os, tc.raw); got != tc.want {
			t.Errorf("Clean(%q,%q)=%q want %q", tc.os, tc.raw, got, tc.want)
		}
		if got := Absolute(tc.os, tc.raw); got != tc.abs {
			t.Errorf("Absolute(%q,%q)=%v", tc.os, tc.raw, got)
		}
	}
	for _, tc := range []struct {
		root, other string
		want        bool
	}{
		{`C:\Projects`, `c:/projects/repo`, true}, {`C:\Projects`, `C:\Projects-evil`, false}, {`C:\Projects`, `D:\Projects\repo`, false},
		{`\\host\share\projects`, `//host/other/projects`, false}, {`/srv/repos`, `/srv/repos/../secret`, false}, {`/srv/repos`, `/srv/repos/app`, true},
	} {
		if Contains(tc.root, tc.other) != tc.want {
			t.Errorf("Contains(%q,%q)", tc.root, tc.other)
		}
	}
	if got := Join(Dir(`C:\Projects\repo`), Base(`C:\Projects\repo`)+"-feature"); got != "C:/Projects/repo-feature" {
		t.Fatal(got)
	}
}
func TestSupportedPackageMatrix(t *testing.T) {
	for _, os := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64", "386"} {
			want := arch == "amd64" || arch == "arm64" && os != "windows"
			if Supported(os, arch) != want {
				t.Fatalf("%s/%s", os, arch)
			}
		}
	}
	if Supported("freebsd", "amd64") {
		t.Fatal("unsupported OS")
	}
	if Filename("windows", "amd64") != "wio-agent-windows-amd64.exe" {
		t.Fatal("Windows extension")
	}
}
