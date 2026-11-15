package agent

import (
	"errors"
	"log/slog"
	"os"
	"strings"
)

func codexEnvironment(config Config, log *slog.Logger) []string {
	keyPath := codexAPIKeyPath(config)
	raw, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) && keyPath != config.CodexAPIKeyFile && config.CodexAPIKeyFile != "" {
		keyPath = config.CodexAPIKeyFile
		raw, err = os.ReadFile(keyPath)
	}
	if err != nil {
		log.Warn("Codex API key is unavailable", "path", keyPath, "error", err)
		return nil
	}
	key := strings.TrimSpace(string(raw))
	if key == "" || strings.ContainsAny(key, "\r\n\x00") {
		log.Warn("Codex API key file is invalid", "path", keyPath)
		return nil
	}
	return []string{"WIO_CODEX_API_KEY=" + key}
}

func (c *Client) currentCodexPath() string {
	c.codexPathMu.RLock()
	defer c.codexPathMu.RUnlock()
	return c.codexPath
}

func (c *Client) setCodexPath(path string) {
	c.codexPathMu.Lock()
	c.codexPath = path
	c.codexPathMu.Unlock()
}
