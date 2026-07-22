package buildinfo

import (
	"strconv"
	"strings"
)

var Version = "0.2.35"

const MinimumSelfUpdateVersion = "0.2.0"
const MinimumCodexUpdateVersion = "0.2.9"

func SupportsSelfUpdate(version string) bool {
	current, ok := parseVersion(version)
	if !ok {
		return false
	}
	minimum, _ := parseVersion(MinimumSelfUpdateVersion)
	for index := range current {
		if current[index] != minimum[index] {
			return current[index] > minimum[index]
		}
	}
	return true
}

func UpdateAvailable(current, target string) bool {
	if !SupportsSelfUpdate(current) {
		return false
	}
	currentVersion, currentOK := parseVersion(current)
	targetVersion, targetOK := parseVersion(target)
	if !currentOK || !targetOK {
		return false
	}
	for index := range currentVersion {
		if currentVersion[index] != targetVersion[index] {
			return targetVersion[index] > currentVersion[index]
		}
	}
	return false
}

func SupportsCodexUpdate(version string) bool {
	return atLeast(version, MinimumCodexUpdateVersion)
}

func atLeast(currentValue, minimumValue string) bool {
	current, currentOK := parseVersion(currentValue)
	minimum, minimumOK := parseVersion(minimumValue)
	if !currentOK || !minimumOK {
		return false
	}
	for index := range current {
		if current[index] != minimum[index] {
			return current[index] > minimum[index]
		}
	}
	return true
}

func parseVersion(value string) ([3]int, bool) {
	var version [3]int
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if cut := strings.IndexAny(value, "+-"); cut >= 0 {
		value = value[:cut]
	}
	parts := strings.Split(value, ".")
	if len(parts) != len(version) {
		return version, false
	}
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return version, false
		}
		version[index] = parsed
	}
	return version, true
}
