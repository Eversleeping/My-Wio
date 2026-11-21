// Package enrollment implements the target-machine registration session. Only
// the device key is retained locally; API and Git secrets stay out of logs.
package enrollment

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/installer"
)

var registrationIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func ValidID(id string) bool { return registrationIDPattern.MatchString(id) }

type Client struct {
	ControlURL string
	ID         string
	DeviceKey  string
	HTTP       *http.Client
}
type Registration struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	ServerID string `json:"server_id"`
}
type Plan struct {
	Registration Registration             `json:"registration"`
	Request      installer.InstallRequest `json:"request"`
}
type responseError struct{ Status int }

func (e responseError) Error() string {
	return fmt.Sprintf("installation request rejected (HTTP %d)", e.Status)
}

func NewClient(controlURL, id, device string) (*Client, error) {
	u, err := url.Parse(controlURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !ValidID(id) {
		return nil, errors.New("a valid control URL and registration ID are required")
	}
	return &Client{ControlURL: strings.TrimSuffix(controlURL, "/"), ID: id, DeviceKey: device, HTTP: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("installation redirects are not allowed")
	}}}, nil
}
func (c *Client) request(ctx context.Context, method, suffix string, input, output any) error {
	var raw []byte
	var err error
	if input != nil {
		raw, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.ControlURL+"/api/agent/registrations/"+c.ID+suffix, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.DeviceKey)
		response, err := c.HTTP.Do(req)
		if err == nil {
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if output != nil {
					err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output)
				} else {
					_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				}
				response.Body.Close()
				if err == nil {
					return nil
				}
			} else {
				status := response.StatusCode
				response.Body.Close()
				err = responseError{status}
				if status < 500 && status != 429 {
					return err
				}
			}
		}
		if attempt == 2 {
			return errors.New("installation request failed; rerun the same command to resume")
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("installation request failed")
}
func (c *Client) Session(ctx context.Context) (Plan, error) {
	var plan Plan
	err := c.request(ctx, "GET", "/session", nil, &plan)
	return plan, err
}
func (c *Client) Claim(ctx context.Context, code string) (Plan, error) {
	var plan Plan
	err := c.request(ctx, "POST", "/claim", map[string]string{"code": code, "device_key": c.DeviceKey, "os": runtime.GOOS, "architecture": runtime.GOARCH}, &plan)
	return plan, err
}
func (c *Client) Progress(ctx context.Context, event installer.Event) error {
	return c.request(ctx, "POST", "/progress", event, nil)
}

func deviceKey(directory string) (string, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", errors.New("installation state directory must be private")
	}
	file := filepath.Join(directory, "device.key")
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	encoded := hex.EncodeToString(key)
	temporary, err := os.CreateTemp(directory, ".device-key-")
	if err != nil {
		return "", err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.WriteString(encoded); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	// Publish a complete key atomically; a crash cannot leave an empty device key.
	err = os.Link(temporaryName, file)
	if err == nil {
		return encoded, nil
	}
	if !os.IsExist(err) {
		return "", err
	}
	info, err = os.Lstat(file)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", errors.New("installation device key must be a private regular file")
	}
	stored, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	if len(stored) != 64 {
		return "", errors.New("invalid local installation device key")
	}
	if _, err := hex.DecodeString(string(stored)); err != nil {
		return "", errors.New("invalid local installation device key")
	}
	return string(stored), nil
}

// Run remembers device ownership before claiming, so a lost HTTP response is
// recoverable. An installed identity is never overwritten by a new registration.
func Run(ctx context.Context, controlURL, id, stateRoot string, readCode func() (string, error), output io.Writer) error {
	if !ValidID(id) {
		return errors.New("invalid registration ID")
	}
	client, err := NewClient(controlURL, id, "")
	if err != nil {
		return err
	}
	directory := filepath.Join(stateRoot, id)
	key, err := deviceKey(directory)
	if err != nil {
		return err
	}
	if err := bindControlURL(directory, client.ControlURL); err != nil {
		return err
	}
	client.DeviceKey = key
	plan, err := client.Session(ctx)
	if err != nil {
		var rejected responseError
		if !errors.As(err, &rejected) || rejected.Status != 401 {
			return err
		}
		code, err := readCode()
		if err != nil {
			return err
		}
		plan, err = client.Claim(ctx, strings.TrimSpace(code))
		if err != nil {
			return err
		}
	}
	if plan.Registration.State == "complete" {
		fmt.Fprintln(output, "Agent is already registered and online.")
		return nil
	}
	if raw, err := os.ReadFile("/etc/wio-agent/config.json"); err == nil {
		var installed struct {
			ServerID string `json:"server_id"`
		}
		if json.Unmarshal(raw, &installed) != nil || installed.ServerID == "" || installed.ServerID != plan.Registration.ServerID {
			return errors.New("this machine already has another Agent identity; use repair registration")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	resultFile := filepath.Join(directory, "result.json")
	var result installer.InstallResult
	cached, readErr := os.ReadFile(resultFile)
	cachedOK := readErr == nil && json.Unmarshal(cached, &result) == nil && result.ServerID != "" && !serviceFailed(result)
	if !cachedOK {
		request := plan.Request
		if request.RegistrationID != id || request.ControlURL != client.ControlURL || request.EnrollmentToken != key {
			return errors.New("installation plan identity mismatch")
		}
		request.OnInstalled = func(result installer.InstallResult) error {
			if err := saveResult(resultFile, result); err != nil {
				return err
			}
			_ = client.Progress(ctx, installer.Event{Type: "complete", Result: &result})
			return nil
		}
		request.Progress = func(progress installer.InstallProgress) {
			fmt.Fprintln(output, progress.Step)
			_ = client.Progress(ctx, installer.Event{Type: "progress", Step: progress.Step, Current: progress.Current, Total: progress.Total})
		}
		result, err = installer.InstallLocal(ctx, request)
		if result.ServerID != "" {
			if writeErr := saveResult(resultFile, result); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if errors.Is(err, installer.ErrHeartbeatPending) {
				_ = client.Progress(ctx, installer.Event{Type: "complete", Result: &result})
				return errors.New("Agent installed but its heartbeat is pending; check web progress or rerun the same command")
			}
			_ = client.Progress(ctx, installer.Event{Type: "error", Code: "installation_failed"})
			return errors.New("installation failed; check the web progress and rerun the same command to retry")
		}
	}
	if err := client.Progress(ctx, installer.Event{Type: "complete", Result: &result}); err != nil {
		return err
	}
	if serviceFailed(result) {
		return errors.New("Agent service did not start; resolve the service error and rerun the same command")
	}
	fmt.Fprintln(output, "Waiting for the Agent heartbeat...")
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("Agent heartbeat not received yet; check the web progress or rerun the command")
		case <-ticker.C:
			current, err := client.Session(ctx)
			if err != nil {
				return err
			}
			if current.Registration.State == "complete" {
				fmt.Fprintln(output, "Agent registered, online, and configured to start on boot.")
				for _, warning := range result.Warnings {
					fmt.Fprintln(output, "Warning:", warning)
				}
				return nil
			}
		}
	}
}
func serviceFailed(result installer.InstallResult) bool {
	for _, w := range result.Warnings {
		if w == "service_start_failed" || w == "service_not_active" {
			return true
		}
	}
	return false
}

func bindControlURL(directory, controlURL string) error {
	file := filepath.Join(directory, "control-url")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, err = f.WriteString(controlURL)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	if !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("installation origin must be a private regular file")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if string(raw) != controlURL {
		return errors.New("this registration is bound to a different control URL")
	}
	return nil
}

func saveResult(file string, result installer.InstallResult) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(file), ".result-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), file)
}
