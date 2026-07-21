package agent

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ControlURL         string   `json:"control_url"`
	ControlDialAddress string   `json:"control_dial_address,omitempty"`
	ServerID           string   `json:"server_id"`
	AgentToken         string   `json:"agent_token"`
	ScanRoots          []string `json:"scan_roots"`
	CloneRoot          string   `json:"clone_root"`
	StateDir           string   `json:"state_dir"`
	CodexPath          string   `json:"codex_path"`
	CodexAPIKeyFile    string   `json:"codex_api_key_file"`
	DockerPath         string   `json:"docker_path"`
	PrerequisiteSocket string   `json:"prerequisite_socket"`
	InsecureSkipVerify bool     `json:"insecure_skip_verify,omitempty"`
}

func LoadConfig(filename string) (Config, error) {
	raw, err := os.ReadFile(filename)
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return Config{}, err
	}
	config.defaults()
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func SaveConfig(filename string, config Config) error {
	config.defaults()
	if err := config.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		return err
	}
	temporary := filename + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filename)
}

func (c *Config) defaults() {
	c.ControlURL = strings.TrimRight(strings.TrimSpace(c.ControlURL), "/")
	c.ControlDialAddress = strings.TrimSpace(c.ControlDialAddress)
	if len(c.ScanRoots) == 0 {
		c.ScanRoots = []string{"/srv", "/opt", "/home"}
	}
	if c.CloneRoot == "" {
		c.CloneRoot = "/var/lib/wio-agent/projects"
	}
	if c.StateDir == "" {
		c.StateDir = "/var/lib/wio-agent"
	}
	if c.CodexPath == "" {
		c.CodexPath = "codex"
	}
	if c.CodexAPIKeyFile == "" {
		c.CodexAPIKeyFile = "/etc/wio-agent/codex.key"
	}
	if c.DockerPath == "" {
		c.DockerPath = "docker"
	}
	if c.PrerequisiteSocket == "" {
		c.PrerequisiteSocket = "/run/wio-prerequisites/helper.sock"
	}
}

func (c Config) Validate() error {
	if c.ControlURL == "" || c.ServerID == "" || c.AgentToken == "" {
		return errors.New("control_url, server_id, and agent_token are required")
	}
	if !strings.HasPrefix(c.ControlURL, "https://") && !strings.HasPrefix(c.ControlURL, "http://") {
		return errors.New("control_url must use https:// or http://")
	}
	if c.ControlDialAddress != "" {
		if _, _, err := net.SplitHostPort(c.ControlDialAddress); err != nil {
			return errors.New("control_dial_address must be host:port")
		}
	}
	if !filepath.IsAbs(c.CloneRoot) || !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.CodexAPIKeyFile) || !filepath.IsAbs(c.PrerequisiteSocket) {
		return errors.New("clone_root, state_dir, codex_api_key_file, and prerequisite_socket must be absolute")
	}
	for _, root := range c.ScanRoots {
		if !filepath.IsAbs(root) {
			return errors.New("scan roots must be absolute")
		}
	}
	return nil
}
