package buildinfo

import (
	"strconv"
	"strings"
)

var Version = "0.2.86"

const MinimumGeminiGoalVersion = "0.2.84"

func SupportsGeminiGoal(agent, cli string) bool {
	return atLeast(agent, MinimumGeminiGoalVersion) && atLeast(cli, "0.66.0")
}

const MinimumGeminiSkillsVersion = "0.2.83"
const MinimumGeminiPlanVersion = "0.2.83"

func SupportsGeminiSkills(agent, cli string) bool {
	return atLeast(agent, MinimumGeminiSkillsVersion) && atLeast(cli, "0.66.0")
}

func SupportsGeminiPlan(agent, cli string) bool {
	return atLeast(agent, MinimumGeminiPlanVersion) && atLeast(cli, "0.66.0")
}

const MinimumGeminiSteerVersion = "0.2.79"

func SupportsGeminiSteering(agent, cli string) bool {
	return atLeast(agent, MinimumGeminiSteerVersion) && atLeast(cli, "0.66.0")
}

const MinimumGeminiInputVersion = "0.2.73"

func SupportsGeminiInput(version string) bool { return atLeast(version, MinimumGeminiInputVersion) }

const MinimumCLIManagementVersion = "0.2.61"

func SupportsCLIManagement(version string) bool {
	return atLeast(version, MinimumCLIManagementVersion)
}

const MinimumSelfUpdateVersion = "0.2.0"
const MinimumCodexUpdateVersion = "0.2.9"
const MinimumExactDeploymentRollbackVersion = "0.2.45"
const MinimumCodexSteerVersion = "0.2.51"
const MinimumWorkspaceFileWriteVersion = "0.2.56"
const MinimumMessageForkVersion = "0.2.59"
const MinimumGeminiVersion = "0.2.60"

func SupportsGemini(version string) bool {
	return atLeast(version, MinimumGeminiVersion)
}

func SupportsMessageFork(version string) bool {
	return atLeast(version, MinimumMessageForkVersion)
}

func SupportsWorkspaceFileWrite(version string) bool {
	return atLeast(version, MinimumWorkspaceFileWriteVersion)
}

func SupportsCodexSteer(version string) bool {
	return atLeast(version, MinimumCodexSteerVersion)
}

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

func SupportsExactDeploymentRollback(version string) bool {
	return atLeast(version, MinimumExactDeploymentRollbackVersion)
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

const MinimumGeminiCredentialsVersion = "0.2.63"

func SupportsGeminiCredentials(version string) bool {
	return atLeast(version, MinimumGeminiCredentialsVersion)
}

// ACP rewrites before 0.2.85 can load the original native conversation.
const MinimumGeminiRewriteVersion = "0.2.85"

func SupportsGeminiRewrite(version string) bool { return atLeast(version, MinimumGeminiRewriteVersion) }

const MinimumGeminiForkVersion = "0.2.68"

func SupportsGeminiFork(version string) bool { return atLeast(version, MinimumGeminiForkVersion) }

const MinimumGeminiThinkingVersion = "0.2.70"

func SupportsGeminiThinking(version string) bool {
	return atLeast(version, MinimumGeminiThinkingVersion)
}

const MinimumGeminiYoloVersion = "0.2.72"

func SupportsGeminiYolo(version string) bool {
	return atLeast(version, MinimumGeminiYoloVersion)
}
