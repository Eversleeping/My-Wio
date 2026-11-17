package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/runtimeaccess"
)

const runtimeAccessRestart = "runtime-access-restart"
const runtimeServiceRestart = "runtime-service-restart"

func (c *Client) callRuntimeAccess(ctx context.Context, r runtimeaccess.Request) (protocol.AgentAccessSettings, error) {
	if c.runtimeAccessCall != nil {
		return c.runtimeAccessCall(ctx, r)
	}
	if c.config.configPath != "/etc/wio-agent/config.json" {
		return protocol.AgentAccessSettings{}, errors.New("runtime settings are managed by this Agent's deployment")
	}

	result, err := runtimeaccess.Call(ctx, c.config.PrerequisiteSocket, r)
	if err == nil {
		return result, nil
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) && !strings.Contains(err.Error(), "unsupported prerequisite helper action") {
		return result, err
	}
	return c.dockerRuntimeAccess(ctx, r)
}
func (c *Client) dockerRuntimeAccess(ctx context.Context, r runtimeaccess.Request) (protocol.AgentAccessSettings, error) {
	// Only a local rootful Docker daemon can administer this server. Ignore
	// DOCKER_HOST and contexts that could point at a different machine.
	docker := c.config.DockerPath
	if docker == "" {
		docker = "docker"
	}
	invoke := func(args ...string) *exec.Cmd {
		command := childprocess.CommandContext(ctx, docker, append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
		command.Env = childprocess.Environment(childprocess.WithExtraEnvironment(ctx, nil), "DOCKER_CONTEXT=", "DOCKER_HOST=unix:///var/run/docker.sock")
		return command
	}
	executable, err := os.Executable()
	if err != nil {
		return protocol.AgentAccessSettings{}, err
	}
	f, err := os.Open(executable)
	if err != nil {
		return protocol.AgentAccessSettings{}, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return protocol.AgentAccessSettings{}, err
	}
	tag := "wio-runtime-helper:" + hex.EncodeToString(hash.Sum(nil))
	image, err := invoke("image", "inspect", tag, "--format", "{{.Id}}").Output()
	if err != nil {
		if _, err = f.Seek(0, 0); err != nil {
			return protocol.AgentAccessSettings{}, err
		}
		archive, err := os.CreateTemp(c.config.StateDir, ".runtime-helper-*.tar")
		if err != nil {
			return protocol.AgentAccessSettings{}, err
		}
		defer os.Remove(archive.Name())
		defer archive.Close()
		info, err := f.Stat()
		if err != nil {
			return protocol.AgentAccessSettings{}, err
		}
		writer := tar.NewWriter(archive)
		if err = writer.WriteHeader(&tar.Header{Name: "wio-agent", Mode: 0555, Size: info.Size()}); err != nil {
			return protocol.AgentAccessSettings{}, err
		}
		if _, err = io.Copy(writer, f); err != nil {
			return protocol.AgentAccessSettings{}, err
		}
		if err = writer.Close(); err != nil {
			return protocol.AgentAccessSettings{}, err
		}
		if _, err = archive.Seek(0, 0); err != nil {
			return protocol.AgentAccessSettings{}, err
		}
		command := invoke("import", "--change", `ENTRYPOINT ["/wio-agent"]`, "--change", "LABEL io.wio.runtime-helper=true", "-", tag)
		command.Stdin = archive
		image, err = command.Output()
		if err != nil {
			return protocol.AgentAccessSettings{}, errors.New("system settings helper is unavailable; this Agent needs a local system helper or Docker management access")
		}
	}
	imageID := strings.TrimSpace(string(image))
	digest, decodeErr := hex.DecodeString(strings.TrimPrefix(imageID, "sha256:"))
	if decodeErr != nil || len(digest) != 32 {
		return protocol.AgentAccessSettings{}, errors.New("invalid local helper image")
	}
	mount := "type=bind,src=/,dst=/host"
	if r.Action == "read" {
		mount += ",readonly"
	}
	// systemd authenticates the caller using its process identity. Share the
	// host PID namespace to match the host /proc mounted by the chroot; a
	// container PID namespace makes sd-bus authentication fail with ENODATA.
	command := invoke("run", "--pull", "never", "--rm", "--sig-proxy=false", "--read-only", "--network", "none", "--pid", "host", "--user", "0:0", "--cap-drop", "ALL", "--cap-add", "SYS_CHROOT", "--cap-add", "DAC_OVERRIDE", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--security-opt", "no-new-privileges=true", "--pids-limit", "32", "--mount", mount, "-i", imageID, "runtime-access-helper", "--host-root")
	input, _ := json.Marshal(r)
	command.Stdin = bytes.NewReader(input)
	output, err := command.Output()
	if err != nil {
		return protocol.AgentAccessSettings{}, errors.New("could not run the local system settings helper")
	}
	var reply runtimeaccess.Reply
	if json.Unmarshal(output, &reply) != nil {
		return reply.Settings, errors.New("invalid system settings reply")
	}
	if reply.Error != "" {
		return reply.Settings, errors.New(reply.Error)
	}
	return reply.Settings, nil
}
func (c *Client) runRuntimeAccess(ctx context.Context, envelope *protocol.ControlEnvelope) (restart string, data json.RawMessage, refresh bool, err error) {
	var command protocol.AgentAccessCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return
	}
	if command.ServerID == "" || command.ServerID != c.config.ServerID {
		err = errors.New("runtime settings server identity differs")
		return
	}
	request := runtimeaccess.Request{Action: "read", ServerID: command.ServerID}
	if envelope.Kind == protocol.OpAgentAccessUpdate || envelope.Kind == protocol.OpAgentRestart {
		if envelope.Kind == protocol.OpAgentAccessUpdate && (command.Settings == nil || command.Settings.AllowSudo && command.Settings.SessionIsolation) || envelope.Kind == protocol.OpAgentRestart && command.Settings != nil {
			err = errors.New("invalid runtime settings")
			return
		}
		c.seenMu.Lock()
		if c.codex != nil && c.codex.HasActiveTurn() {
			err = errors.New("finish active Codex turns before changing runtime settings")
		} else if c.updatePending {
			err = errors.New("another Agent maintenance operation is in progress")
		} else {
			err = c.agentUpdateBlocker()
			if err == nil {
				c.updatePending = true
			}
		}
		c.seenMu.Unlock()
		if err != nil {
			return
		}
		defer func() {
			if err != nil || restart == "" {
				c.releaseAgentUpdate()
			}
		}()
		if envelope.Kind == protocol.OpAgentAccessUpdate {
			if command.Settings.SessionIsolation {
				if err = c.isolation.CheckSupport(ctx); err != nil {
					err = fmt.Errorf("cannot enable session isolation: %w", err)
					return
				}
			}
			request.Action, request.Settings = "apply", command.Settings
		}
	} else if command.Settings != nil {
		err = errors.New("read operation cannot change runtime settings")
		return
	}
	settings, callErr := c.callRuntimeAccess(ctx, request)
	if callErr != nil {
		err = callErr
		return
	}
	isolation := c.config.SessionIsolation != nil && c.config.SessionIsolation.Enabled
	settings.Applied = settings.AllowSudo == c.config.AllowSudo && settings.SessionIsolation == isolation
	settings.InstanceID = c.runtimeInstanceID()
	settings.IsolationSupport = &protocol.AgentIsolationSupport{Supported: true}
	if supportErr := c.isolation.CheckSupport(ctx); supportErr != nil {
		settings.IsolationSupport.Supported = false
		settings.IsolationSupport.Reason = supportErr.Error()
	}
	data, err = json.Marshal(settings)
	if envelope.Kind == protocol.OpAgentRestart {
		restart = runtimeServiceRestart + ":" + settings.Revision
	} else if settings.RestartRequired && envelope.Kind == protocol.OpAgentAccessUpdate {
		restart = runtimeAccessRestart + ":" + settings.Revision
	}
	return
}
func (c *Client) restartRuntimeAccess(ctx context.Context, path string) error {
	revision := strings.TrimPrefix(path, runtimeAccessRestart+":")
	force := strings.HasPrefix(path, runtimeServiceRestart+":")
	if force {
		revision = strings.TrimPrefix(path, runtimeServiceRestart+":")
	}
	_, err := c.callRuntimeAccess(ctx, runtimeaccess.Request{Action: "restart", ServerID: c.config.ServerID, Revision: revision, Force: force})
	if err != nil {
		return fmt.Errorf("could not restart Agent after changing runtime settings: %w", err)
	}
	return nil
}
