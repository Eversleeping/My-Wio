package childprocess

import (
	"os"
	"strings"
)

// Environment builds a child environment without inheriting the Agent or
// control-plane WIO_* settings. Explicit command settings still take precedence
// and may include Wio's per-engine credentials or compatibility hooks.
func Environment(overrides ...string) []string {
	return mergeEnvironment(os.Environ(), overrides)
}

func mergeEnvironment(inherited, overrides []string) []string {
	overridden := make(map[string]bool, len(overrides))
	for _, entry := range overrides {
		name, _, _ := strings.Cut(entry, "=")
		overridden[strings.ToUpper(name)] = true
	}
	environment := make([]string, 0, len(inherited)+len(overrides))
	for _, entry := range inherited {
		name, _, _ := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if !strings.HasPrefix(name, "WIO_") && !overridden[name] {
			environment = append(environment, entry)
		}
	}
	return append(environment, overrides...)
}
