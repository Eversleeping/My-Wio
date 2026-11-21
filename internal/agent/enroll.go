package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

type EnrollmentOptions struct {
	PreviousConfig     *Config
	RegistrationID     string
	AllowSudo          bool
	ControlURL         string
	EnrollmentToken    string
	ConfigPath         string
	CloneRoot          string
	StateDir           string
	CodexPath          string
	DockerPath         string
	InsecureSkipVerify bool
}

func Enroll(ctx context.Context, options EnrollmentOptions) (Config, error) {
	parsed, err := url.Parse(options.ControlURL)
	if err != nil || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" {
		return Config{}, errors.New("control URL must contain only scheme and host")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return Config{}, errors.New("control URL must use HTTPS or HTTP")
	}
	hostname, err := os.Hostname()
	if err != nil {
		return Config{}, err
	}
	fields := map[string]string{"hostname": hostname}
	if options.RegistrationID == "" {
		fields["enrollment_token"] = options.EnrollmentToken
	}
	payload, _ := json.Marshal(fields)
	endpoint := options.ControlURL + "/api/agent/enroll"
	if options.RegistrationID != "" {
		endpoint = options.ControlURL + "/api/agent/registrations/" + url.PathEscape(options.RegistrationID) + "/enroll"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Config{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if options.RegistrationID != "" {
		request.Header.Set("Authorization", "Bearer "+options.EnrollmentToken)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: options.InsecureSkipVerify}}}
	response, err := client.Do(request)
	if err != nil {
		return Config{}, err
	}
	defer response.Body.Close()
	var result struct {
		ServerID   string   `json:"server_id"`
		AgentToken string   `json:"agent_token"`
		ScanRoots  []string `json:"scan_roots"`
		Error      string   `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return Config{}, err
	}
	if response.StatusCode != http.StatusCreated {
		return Config{}, fmt.Errorf("enrollment failed: %s", result.Error)
	}
	config := Config{ControlURL: options.ControlURL, ServerID: result.ServerID, AgentToken: result.AgentToken, ScanRoots: result.ScanRoots, CloneRoot: options.CloneRoot, StateDir: options.StateDir, CodexPath: options.CodexPath, DockerPath: options.DockerPath, InsecureSkipVerify: options.InsecureSkipVerify}
	if options.PreviousConfig != nil {
		previous := *options.PreviousConfig
		if previous.ServerID != config.ServerID {
			return Config{}, errors.New("repair response belongs to another server")
		}
		previous.ControlURL, previous.AgentToken = config.ControlURL, config.AgentToken
		config = previous
	} else {
		config.AllowSudo = options.AllowSudo
	}
	config.defaults()
	if err := SaveConfig(options.ConfigPath, config); err != nil {
		return Config{}, err
	}
	return config, nil
}
