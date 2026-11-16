package sessionisolation

import (
	"net/url"
	"strings"
)

type environment map[string]string

func commandEnvironment(entries []string) environment {
	out := environment{}
	for _, entry := range entries {
		if name, value, ok := strings.Cut(entry, "="); ok {
			out[strings.ToUpper(name)] = value
		}
	}
	return out
}

func (e environment) first(names ...string) string {
	for _, name := range names {
		if value := e[name]; value != "" {
			return value
		}
	}
	return ""
}

func (e environment) entries() []string {
	out := make([]string, 0, len(e))
	for name, value := range e {
		out = append(out, name+"="+value)
	}
	return out
}

var publicRuntime = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"LANG": true, "LANGUAGE": true, "TZ": true, "TERM": true, "COLORTERM": true,
	"NO_COLOR": true, "FORCE_COLOR": true, "CLICOLOR": true, "CLICOLOR_FORCE": true,
	"CI": true, "PAGER": true, "NODE_OPTIONS": true, "NODE_PATH": true, "NODE_ENV": true,
	"NODE_NO_WARNINGS": true, "NODE_ICU_DATA": true, "NODE_EXTRA_CA_CERTS": true, "NODE_USE_SYSTEM_CA": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "REQUESTS_CA_BUNDLE": true, "CURL_CA_BUNDLE": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
	"IS_SANDBOX": true, "DISABLE_AUTOUPDATER": true, "DISABLE_TELEMETRY": true,
	"DISABLE_ERROR_REPORTING": true, "ENABLE_TOOL_SEARCH": true,
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true, "GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
}

func publicEnvironment(original environment, provider string) environment {
	out := environment{}
	for name, value := range original {
		allowed := publicRuntime[name] || strings.HasPrefix(name, "LC_")
		if strings.HasPrefix(name, strings.ToUpper(provider)+"_") && !sensitiveName(name) {
			allowed = true
		}
		if strings.HasSuffix(name, "_PROXY") && name != "NO_PROXY" {
			if parsed, err := url.Parse(value); err != nil || parsed.User != nil {
				allowed = false
			}
		}
		if allowed {
			out[name] = value
		}
	}
	if provider == "gemini" {
		// These values are prepared by the trusted adapter for its inert model
		// aliases. They contain model choices, never account authentication.
		for _, name := range []string{"WIO_GEMINI_LOOP_MODEL", "WIO_GEMINI_THINKING_MODEL", "WIO_GEMINI_THINKING_LEVEL"} {
			if value := original[name]; value != "" {
				out[name] = value
			}
		}
	}
	// Git pushes and credential helpers remain trusted Agent operations.
	out["GIT_CONFIG_NOSYSTEM"], out["GIT_CONFIG_GLOBAL"] = "1", "/dev/null"
	out["GIT_CONFIG_COUNT"] = "1"
	out["GIT_CONFIG_KEY_0"], out["GIT_CONFIG_VALUE_0"] = "credential.helper", ""
	out["GIT_TERMINAL_PROMPT"] = "0"
	out["NO_PROXY"] = strings.TrimPrefix(original["NO_PROXY"]+",127.0.0.1,localhost", ",")
	return out
}

func sensitiveName(name string) bool {
	name = strings.ToLower(name)
	// Token budgets are public model settings, not authentication tokens.
	switch strings.ReplaceAll(name, "_", "") {
	case "maxtokens", "maxinputtokens", "maxoutputtokens", "modelmaxoutputtokens", "claudecodemaxoutputtokens":
		return false
	}
	for _, part := range []string{"token", "secret", "password", "credential", "api_key", "apikey", "auth", "headers", "bedrock", "vertex", "foundry"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

func isTrue(value string) bool {
	return value == "1" || strings.EqualFold(value, "true")
}
