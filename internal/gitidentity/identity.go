package gitidentity

import (
	"errors"
	"net/mail"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	nameLimit  = 256
	emailLimit = 320
)

func Normalize(name, email string) (string, string, error) {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if name == "" || utf8.RuneCountInString(name) > nameLimit || strings.ContainsAny(name, "\r\n\x00") {
		return "", "", errors.New("Git commit name is invalid")
	}
	if email == "" || len(email) > emailLimit || strings.ContainsAny(email, "\r\n\x00") {
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
	if !filepath.IsAbs(credentialFile) && !path.IsAbs(credentialFile) || strings.ContainsAny(credentialFile, "\r\n\x00") {
		return "", errors.New("Git credential file path is invalid")
	}
	return "[user]\n\tname = " + strconv.Quote(name) + "\n\temail = " + strconv.Quote(email) + "\n\n[credential]\n\thelper = store --file=" + strconv.Quote(credentialFile) + "\n", nil
}
