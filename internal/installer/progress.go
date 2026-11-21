package installer

var progressSteps = map[string]bool{
	"waiting_agent":                      true,
	"configuring_npm":                    true,
	"preparing_account":                  true,
	"checking_cli_versions":              true,
	"verifying_dependencies":             true,
	"installing_gemini":                  true,
	"uploading_prerequisite_helper":      true,
	"configuring_codex":                  true,
	"installing_git":                     true,
	"uploading_agent":                    true,
	"enrolling_agent":                    true,
	"inspecting":                         true,
	"configuring_git":                    true,
	"installing_claude":                  true,
	"installing_playwright":              true,
	"installing_docker":                  true,
	"installing_node":                    true,
	"uploading_service":                  true,
	"connecting":                         true,
	"installing_deployment_dependencies": true,
	"installing_codex":                   true,
	"starting_service":                   true,
}

func ValidProgressStep(step string) bool { return progressSteps[step] }

var installWarnings = map[string]bool{
	"claude_install_failed":                   true,
	"claude_install_requires_npm":             true,
	"codex_install_failed":                    true,
	"codex_install_requires_npm":              true,
	"codex_unavailable":                       true,
	"config_permission_failed":                true,
	"deployment_prerequisites_install_failed": true,
	"docker_compose_unavailable":              true,
	"docker_unavailable":                      true,
	"gemini_install_failed":                   true,
	"gemini_install_requires_npm":             true,
	"git_unavailable":                         true,
	"node_install_failed":                     true,
	"npm_registry_configuration_failed":       true,
	"playwright_chromium_install_failed":      true,
	"playwright_dependencies_install_failed":  true,
	"playwright_install_requires_npm":         true,
	"playwright_package_install_failed":       true,
	"playwright_verification_failed":          true,
	"service_not_active":                      true,
	"service_start_failed":                    true,
}

func ValidWarnings(warnings []string) bool {
	if len(warnings) > 64 {
		return false
	}
	for _, warning := range warnings {
		if !installWarnings[warning] {
			return false
		}
	}
	return true
}
