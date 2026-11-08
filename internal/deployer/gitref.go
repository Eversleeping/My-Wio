package deployer

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wio-platform/wio/internal/protocol"
)

const maxGitRefLength = 1024

var objectIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

// ValidateGitRef checks a branch, tag or commit before it is handed to Git.
// It accepts hexadecimal object IDs and names that satisfy
// `git check-ref-format --allow-onelevel`, and additionally rejects names Git
// would read as an option ("-...") or a refspec modifier ("+..."), and any
// whitespace or control character.
func ValidateGitRef(ref string) error {
	if ref == "" {
		return errors.New("Git ref is required")
	}
	if len(ref) > maxGitRefLength {
		return errors.New("Git ref is too long")
	}
	if !utf8.ValidString(ref) {
		return errors.New("Git ref is not valid UTF-8")
	}
	if strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "+") {
		return fmt.Errorf("Git ref %q must not start with %q", ref, ref[:1])
	}
	for _, char := range ref {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return fmt.Errorf("Git ref %q contains whitespace or control characters", ref)
		}
	}
	if objectIDPattern.MatchString(ref) || validRefName(ref) {
		return nil
	}
	return fmt.Errorf("Git ref %q is not a valid branch, tag or commit", ref)
}

// validRefName mirrors Git's check_refname_format with REFNAME_ALLOW_ONELEVEL.
func validRefName(name string) bool {
	if name == "@" || strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.HasSuffix(name, ".") {
		return false
	}
	for index := 0; index < len(name); index++ {
		if char := name[index]; char < 0x20 || char == 0x7f || strings.IndexByte(" ~^:?*[\\", char) >= 0 {
			return false
		}
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

// deploymentRef returns the validated ref a deployment checks out. Workspace
// deployments default to the workspace's HEAD.
func deploymentRef(command protocol.DeployCommand) (string, error) {
	ref := command.CommitRef
	if command.SourceType == "workspace" && ref == "" {
		ref = "HEAD"
	}
	if err := ValidateGitRef(ref); err != nil {
		return "", fmt.Errorf("invalid deployment Git ref: %w", err)
	}
	return ref, nil
}
