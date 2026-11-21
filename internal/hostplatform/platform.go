// Package hostplatform defines installed Agent locations independently of the control-plane host.
package hostplatform

import (
	"path"
	"runtime"
	"strings"
)

type Layout struct {
	State, Config, Key, Binary, Installer, Home string
	Roots                                       []string
}

func Supported(os, arch string) bool {
	return (os == "linux" || os == "darwin") && (arch == "amd64" || arch == "arm64") || os == "windows" && arch == "amd64"
}
func Key(os, arch string) string { return os + "-" + arch }
func Filename(os, arch string) string {
	name := "wio-agent-" + Key(os, arch)
	if os == "windows" {
		name += ".exe"
	}
	return name
}
func Current() Layout { return nativeLayout(For(runtime.GOOS)) }
func For(os string) Layout {
	switch os {
	case "darwin":
		return Layout{State: "/Library/Application Support/Wio", Config: "/Library/Application Support/Wio/config.json", Key: "/Library/Application Support/Wio/codex.key", Binary: "/usr/local/bin/wio-agent", Installer: "/var/root/Library/WioInstaller", Home: "/Library/Application Support/Wio", Roots: []string{"/Users", "/opt"}}
	case "windows":
		return Layout{State: `C:\ProgramData\Wio`, Config: `C:\ProgramData\Wio\config.json`, Key: `C:\ProgramData\Wio\codex.key`, Binary: `C:\Program Files\Wio\wio-agent.exe`, Installer: `C:\ProgramData\WioInstaller`, Home: `C:\ProgramData\Wio`, Roots: []string{`C:\Projects`}}
	default:
		return Layout{State: "/var/lib/wio-agent", Config: "/etc/wio-agent/config.json", Key: "/etc/wio-agent/codex.key", Binary: "/usr/local/bin/wio-agent", Installer: "/var/lib/wio-installer", Home: "/var/lib/wio-agent", Roots: []string{"/srv", "/opt", "/home"}}
	}
}

// Windows paths are normalized without consulting the control plane's filepath rules.
func Windows(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\') || strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//")
}
func Absolute(os, value string) bool {
	if strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if os != "windows" {
		return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//")
	}
	v := strings.ReplaceAll(value, `\`, "/")
	if strings.HasPrefix(v, "//") {
		parts := strings.Split(strings.TrimPrefix(v, "//"), "/")
		return len(parts) >= 2 && parts[0] != "" && parts[1] != "" && parts[0] != "." && parts[0] != "?" && parts[1] != "." && parts[1] != ".." && !strings.Contains(v, ":")
	}
	return len(v) >= 3 && Windows(v) && !strings.Contains(v[2:], ":")
}
func Clean(os, value string) string {
	if os != "windows" {
		return path.Clean(value)
	}
	v := strings.ReplaceAll(value, `\`, "/")
	if strings.HasPrefix(v, "//") {
		parts := strings.SplitN(strings.TrimPrefix(v, "//"), "/", 3)
		if len(parts) < 2 {
			return v
		}
		suffix := "/"
		if len(parts) == 3 {
			suffix = path.Clean("/" + parts[2])
		}
		return strings.TrimSuffix("//"+parts[0]+"/"+parts[1]+suffix, "/")
	}
	if len(v) >= 3 && v[1] == ':' {
		return strings.ToUpper(v[:1]) + ":" + path.Clean(v[2:])
	}
	return path.Clean(v)
}
func TargetOS(value string) string {
	if Windows(value) {
		return "windows"
	}
	return "linux"
}
func Dir(value string) string {
	clean := Clean(TargetOS(value), value)
	if len(clean) == 3 && clean[1] == ':' {
		return clean
	}
	if strings.HasPrefix(clean, "//") {
		parts := strings.Split(strings.TrimPrefix(clean, "//"), "/")
		if len(parts) <= 2 {
			return clean
		}
		return strings.TrimSuffix(clean, "/"+parts[len(parts)-1])
	}
	return path.Dir(clean)
}
func Base(value string) string { return path.Base(Clean(TargetOS(value), value)) }
func Join(root, leaf string) string {
	return Clean(TargetOS(root), strings.TrimRight(root, `/\`)+"/"+leaf)
}

func Equal(left, right string) bool {
	os := TargetOS(left)
	if os != TargetOS(right) {
		return false
	}
	left, right = Clean(os, left), Clean(os, right)
	if os == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// Contains is lexical only. The Agent must validate symlinks and the actual
// filesystem before performing a write or deletion.
func Contains(root, other string) bool {
	os := TargetOS(root)
	if os != TargetOS(other) || !Absolute(os, root) || !Absolute(os, other) {
		return false
	}
	root, other = Clean(os, root), Clean(os, other)
	if os == "windows" {
		root, other = strings.ToLower(root), strings.ToLower(other)
	}
	return root == other || strings.HasPrefix(other, strings.TrimRight(root, "/")+"/")
}
