package installer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wio-platform/wio/internal/hostplatform"
	"io"
	"net/http"
	"os"
	"time"
)

var ErrHeartbeatPending = errors.New("Agent heartbeat has not been received")

func verifyInstalledAgent(ctx context.Context, controlURL, serverID string) error {
	raw, err := os.ReadFile(hostplatform.Current().Config)
	if err != nil {
		return ErrHeartbeatPending
	}
	var config struct {
		ServerID   string `json:"server_id"`
		AgentToken string `json:"agent_token"`
	}
	if json.Unmarshal(raw, &config) != nil || config.ServerID != serverID || config.AgentToken == "" {
		return ErrHeartbeatPending
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected redirect") }}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, "GET", controlURL+"/api/agent/install-status", nil)
		if err != nil {
			return ErrHeartbeatPending
		}
		request.Header.Set("Authorization", "Bearer "+config.AgentToken)
		response, err := client.Do(request)
		if err == nil {
			var state struct {
				Online   bool   `json:"online"`
				ServerID string `json:"server_id"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&state)
			response.Body.Close()
			if response.StatusCode == 200 && decodeErr == nil && state.Online && state.ServerID == serverID {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ErrHeartbeatPending
		case <-ticker.C:
		}
	}
}
