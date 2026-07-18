package codexcli

import (
	"regexp"
	"strconv"
	"strings"
)

const DefaultTargetVersion = "0.144.4"

var strictVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func ValidTargetVersion(value string) bool {
	return strictVersionPattern.MatchString(strings.TrimSpace(value))
}

func UpdateAvailable(reported, target string) bool {
	current, currentOK := parseReportedVersion(reported)
	desired, desiredOK := parseVersion(strings.TrimSpace(target))
	if !currentOK || !desiredOK {
		return false
	}
	for index := range current {
		if current[index] != desired[index] {
			return desired[index] > current[index]
		}
	}
	return false
}

func ReportedVersionMatches(reported, target string) bool {
	current, currentOK := parseReportedVersion(reported)
	desired, desiredOK := parseVersion(strings.TrimSpace(target))
	return currentOK && desiredOK && current == desired
}

func parseReportedVersion(value string) ([3]int, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "codex-cli ")
	value = strings.TrimPrefix(value, "v")
	return parseVersion(value)
}

func parseVersion(value string) ([3]int, bool) {
	var version [3]int
	if !strictVersionPattern.MatchString(value) {
		return version, false
	}
	for index, part := range strings.Split(value, ".") {
		parsed, err := strconv.Atoi(part)
		if err != nil {
			return version, false
		}
		version[index] = parsed
	}
	return version, true
}
