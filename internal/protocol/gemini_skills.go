package protocol

import (
	"errors"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var geminiSkillName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func ValidateGeminiSkillManagement(c GeminiSkillManageCommand) error {
	if c.Scope != "workspace" && c.Scope != "user" {
		return errors.New("choose workspace or user scope")
	}
	switch c.Action {
	case "install":
		if c.Source == "" || len(c.Source) > 2048 || strings.HasPrefix(c.Source, "-") || strings.ContainsAny(c.Source, "\x00\r\n") || c.Name != "" {
			return errors.New("invalid skill source")
		}
		if strings.Contains(c.Source, "://") {
			u, err := url.Parse(c.Source)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("use an HTTPS repository without embedded credentials, or a path inside the workspace")
			}
		}
		if c.Path != "" && (path.IsAbs(c.Path) || path.Clean(c.Path) == ".." || strings.HasPrefix(path.Clean(c.Path), "../") || strings.ContainsAny(c.Path, "\\\x00\r\n:") || len(c.Path) > 1024) {
			return errors.New("invalid skill repository subdirectory")
		}
	case "enable", "disable", "uninstall":
		if !geminiSkillName.MatchString(c.Name) || c.Source != "" || c.Path != "" {
			return errors.New("invalid skill name or action options")
		}
	default:
		return errors.New("unsupported skill action")
	}
	return nil
}
