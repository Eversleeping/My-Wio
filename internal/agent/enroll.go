package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

type EnrollmentOptions struct {
	ControlURL         string
	ControlDialAddress string
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
	payload, _ := json.Marshal(map[string]string{"enrollment_token": options.EnrollmentToken, "hostname": hostname})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, options.ControlURL+"/api/agent/enroll", bytes.NewReader(payload))
	if err != nil {
		return Config{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: options.InsecureSkipVerify}}
	if options.ControlDialAddress != "" {
		transport.DialContext = controlDialer(options.ControlDialAddress)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
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
	config := Config{ControlURL: options.ControlURL, ControlDialAddress: options.ControlDialAddress, ServerID: result.ServerID, AgentToken: result.AgentToken, ScanRoots: result.ScanRoots, CloneRoot: options.CloneRoot, StateDir: options.StateDir, CodexPath: options.CodexPath, DockerPath: options.DockerPath, InsecureSkipVerify: options.InsecureSkipVerify}
	config.defaults()
	if err := SaveConfig(options.ConfigPath, config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func controlDialer(address string) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", address)
	}
}
