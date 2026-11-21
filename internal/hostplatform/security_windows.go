package hostplatform

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
)

func ProtectInstaller(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("installer directory must not be a link")
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
func Private(_ os.FileInfo, path string, _ os.FileMode) bool {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	text := sd.String()
	aces := strings.Split(text, "(")
	count := 0
	for _, ace := range aces[1:] {
		end := strings.IndexByte(ace, ')')
		if end < 0 {
			return false
		}
		fields := strings.Split(ace[:end], ";")
		if len(fields) != 6 {
			return false
		}
		if fields[0] != "A" {
			continue
		}
		count++
		if fields[5] != "SY" && fields[5] != "BA" {
			return false
		}
	}
	return count > 0
}
func Administrator() bool { return windows.GetCurrentProcessToken().IsElevated() }

func nativeLayout(layout Layout) Layout {
	if data, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0); err == nil {
		layout.State = filepath.Join(data, "Wio")
		layout.Home = layout.State
		layout.Config = filepath.Join(layout.State, "config.json")
		layout.Key = filepath.Join(layout.State, "codex.key")
		layout.Installer = filepath.Join(data, "WioInstaller")
	}
	if programs, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0); err == nil {
		layout.Binary = filepath.Join(programs, "Wio", "wio-agent.exe")
	}
	return layout
}

// ProtectUserDirectory also permits the current service identity. Apply before
// writing credentials into a newly created temporary directory.
func ProtectUserDirectory(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
