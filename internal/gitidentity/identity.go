package gitidentity

import (
	"errors"
	"net/mail"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	nameLimit  = 256
	emailLimit = 320
)

func Normalize(name, email string) (string, string, error) {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if name == "" || utf8.RuneCountInString(name) > nameLimit || !printable(name) {
		return "", "", errors.New("Git commit name is invalid")
	}
	if email == "" || len(email) > emailLimit || !printable(email) {
		return "", "", errors.New("Git commit email is invalid")
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return "", "", errors.New("Git commit email is invalid")
	}
	return name, email, nil
}

func Configuration(name, email, credentialFile string) (string, error) {
	name, email, err := Normalize(name, email)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(credentialFile) && !path.IsAbs(credentialFile) || !printable(credentialFile) {
		return "", errors.New("Git credential file path is invalid")
	}
	// Git unquotes the config value and then runs the helper through the
	// shell, so the path is shell-quoted inside the config string.
	values := make([]string, 0, 3)
	for _, value := range []string{name, email, "store --file=" + shellQuote(credentialFile)} {
		quoted, err := quoteConfigValue(value)
		if err != nil {
			return "", err
		}
		values = append(values, quoted)
	}
	return "[user]\n\tname = " + values[0] + "\n\temail = " + values[1] + "\n\n[credential]\n\thelper = " + values[2] + "\n", nil
}

// quoteConfigValue quotes value for a Git config file. Git understands only
// the \" \\ \n \t and \b escapes inside a value, so everything else, including
// non-ASCII UTF-8 such as emoji joined with U+200D, is written as is. Control
// characters cannot be represented safely and are rejected.
func quoteConfigValue(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("Git config value is not valid UTF-8")
	}
	var builder strings.Builder
	builder.Grow(len(value) + 2)
	builder.WriteByte('"')
	for _, char := range value {
		switch {
		case char == '\\' || char == '"':
			builder.WriteByte('\\')
			builder.WriteRune(char)
		case unicode.IsControl(char):
			return "", errors.New("Git config value contains a control character")
		default:
			builder.WriteRune(char)
		}
	}
	builder.WriteByte('"')
	return builder.String(), nil
}

// printable rejects invalid UTF-8 and control characters (newlines, tabs,
// NUL, DEL and the C1 range). Format characters such as the zero-width
// joiner used in emoji sequences remain allowed.
func printable(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
