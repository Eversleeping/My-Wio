package agent

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Gemini 0.65 disables global Git config and appends an empty generic helper
// through GIT_CONFIG_COUNT. Git reads GIT_CONFIG_PARAMETERS afterwards, allowing
// only Wio's explicitly bound HTTPS endpoints to use the protected token file.
// No token is passed in the environment, command arguments or model context.
func (c *Client) geminiGitEnvironment(ctx context.Context) ([]string, error) {
	path, err := filepath.Abs(filepath.Join(c.config.StateDir, ".git-credentials"))
	if err != nil {
		return nil, errors.New("could not resolve Gemini Git credential path")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, errors.New("could not read Gemini Git credential configuration")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("could not read Gemini Git credential configuration")
	}
	var parameters []string
	add := func(key, value string) {
		// Git's native config-parameter encoding, not command-line shell text.
		parameters = append(parameters, "'"+strings.ReplaceAll(key+"="+value, "'", "'\\''")+"'")
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User == nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("invalid Gemini Git credential configuration")
		}
		u.User = nil
		scope := strings.TrimRight(u.String(), "/")
		if !seen[scope] {
			add("credential."+scope+".helper", "store --file="+strconv.Quote(filepath.ToSlash(path)))
			seen[scope] = true
		}
	}
	if len(parameters) == 0 {
		return nil, nil
	}
	// Read only commit identity from Wio's generated file. Do not restore an
	// entire global config, includes, hooks, aliases or arbitrary helpers.
	config := filepath.Join(c.config.StateDir, ".gitconfig")
	if info, err := os.Lstat(config); err == nil && info.Mode().IsRegular() {
		for _, key := range []string{"user.name", "user.email"} {
			cmd := exec.CommandContext(ctx, "git", "config", "--file", config, "--no-includes", "--get", key)
			raw, err := cmd.Output()
			if err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) && exit.ExitCode() == 1 {
					continue
				}
				return nil, errors.New("could not read Gemini Git commit identity")
			}
			value := strings.TrimSpace(string(raw))
			if value != "" && !strings.ContainsAny(value, "\r\n\x00") {
				add(key, value)
			}
		}
	}
	return []string{"GIT_CONFIG_PARAMETERS=" + strings.Join(parameters, " ")}, nil
}
