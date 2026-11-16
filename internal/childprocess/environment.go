package childprocess

import (
	"context"
	"errors"
	"os"
	"strings"
)

// Scope selects the service environment needed by a child command.
type Scope uint8

const (
	Runtime Scope = iota
	Git
	Codex
	Claude
	Gemini
	Deployment
	Installation
)

type scopeKey struct{}
type extraEnvironmentKey struct{}

// WithScope keeps the command's cancellation and extra variable names while
// selecting its inherited service configuration.
func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

// WithExtraEnvironment copies variable names, never their values. Environment
// reads the current values when each command is constructed.
func WithExtraEnvironment(ctx context.Context, names []string) context.Context {
	extra := make(map[string]bool, len(names))
	for _, name := range names {
		if validExtraEnvironmentName(name) {
			extra[strings.ToUpper(name)] = true
		}
	}
	return context.WithValue(ctx, extraEnvironmentKey{}, extra)
}

// ValidateExtraEnvironment rejects assignments, wildcard patterns and Wio's
// own settings. Invalid entries are not included in errors because an accidental
// assignment may itself contain a credential.
func ValidateExtraEnvironment(names []string) error {
	for _, name := range names {
		if !validExtraEnvironmentName(name) {
			return errors.New("extra_environment must contain variable names without assignments, wildcards, or the WIO_ prefix")
		}
	}
	return nil
}

func validExtraEnvironmentName(name string) bool {
	if name == "" || strings.HasPrefix(strings.ToUpper(name), "WIO_") {
		return false
	}
	for i, r := range name {
		if r != '_' && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Environment is the only inherited-environment constructor. Every command
// receives common runtime settings, its scope's namespaces and explicitly
// allowed names. Inherited WIO_* settings are always excluded. Overrides are
// supplied by the Agent, and still take precedence, including empty values.
func Environment(ctx context.Context, overrides ...string) []string {
	scope, _ := ctx.Value(scopeKey{}).(Scope)
	extra, _ := ctx.Value(extraEnvironmentKey{}).(map[string]bool)
	return mergeEnvironment(os.Environ(), overrides, scope, extra)
}

func mergeEnvironment(inherited, overrides []string, scope Scope, extra map[string]bool) []string {
	overridden := make(map[string]bool, len(overrides))
	for _, entry := range overrides {
		name, _, _ := strings.Cut(entry, "=")
		overridden[strings.ToUpper(name)] = true
	}
	environment := make([]string, 0, len(inherited)+len(overrides))
	for _, entry := range inherited {
		name, _, ok := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if ok && !strings.HasPrefix(name, "WIO_") && !overridden[name] && (extra[name] || allowedEnvironmentName(name, scope)) {
			environment = append(environment, entry)
		}
	}
	return append(environment, overrides...)
}

var runtimeEnvironment = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"USERPROFILE": true, "HOMEDRIVE": true, "HOMEPATH": true, "SYSTEMROOT": true,
	"WINDIR": true, "COMSPEC": true, "PATHEXT": true, "APPDATA": true, "LOCALAPPDATA": true,
	"TMPDIR": true, "TMP": true, "TEMP": true,
	"LANG": true, "LANGUAGE": true, "TZ": true, "TERM": true, "COLORTERM": true,
	"NO_COLOR": true, "FORCE_COLOR": true, "CLICOLOR": true, "CLICOLOR_FORCE": true,
	"CI": true, "PAGER": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "REQUESTS_CA_BUNDLE": true,
	"CURL_CA_BUNDLE": true, "NODE_EXTRA_CA_CERTS": true, "NODE_USE_SYSTEM_CA": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true,
	"XDG_STATE_HOME": true, "XDG_RUNTIME_DIR": true, "XDG_CONFIG_DIRS": true, "XDG_DATA_DIRS": true,
	"NODE_OPTIONS": true, "NODE_PATH": true, "NODE_ENV": true, "NODE_NO_WARNINGS": true, "NODE_ICU_DATA": true,
}

func allowedEnvironmentName(name string, scope Scope) bool {
	if runtimeEnvironment[name] || strings.HasPrefix(name, "LC_") {
		return true
	}
	switch scope {
	case Git, Codex, Claude, Gemini, Deployment:
		if hasEnvironmentPrefix(name, "GIT_", "GCM_") || name == "SSH_AUTH_SOCK" || name == "SSH_AGENT_PID" {
			return true
		}
	}
	switch scope {
	case Codex:
		return hasEnvironmentPrefix(name, "CODEX_", "OPENAI_")
	case Claude:
		if hasEnvironmentPrefix(name, "CLAUDE_", "ANTHROPIC_", "AWS_", "GOOGLE_", "GCLOUD_", "CLOUDSDK_", "AZURE_") {
			return true
		}
		switch name {
		case "IS_SANDBOX", "DISABLE_AUTOUPDATER", "DISABLE_TELEMETRY", "DISABLE_ERROR_REPORTING", "ENABLE_TOOL_SEARCH":
			return true
		}
	case Gemini:
		return hasEnvironmentPrefix(name, "GEMINI_", "GOOGLE_", "GCLOUD_", "CLOUDSDK_")
	case Deployment, Installation:
		return hasEnvironmentPrefix(name, "DOCKER_", "COMPOSE_", "NPM_CONFIG_")
	}
	return false
}

func hasEnvironmentPrefix(name string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
