package geminiadapter

import (
	"bytes"
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tailscale/hujson"
	"github.com/wio-platform/wio/internal/protocol"
)

//go:embed steering-hook.cjs
var steeringHook []byte

// Outside Wio this hook is inert. Keeping the command in user settings lets
// unprivileged Agents use the official hook system without overriding any
// administrator policy. Its script and credentials exist only for one turn.
const steeringHookCommand = `node -e "if(process.env.WIO_GEMINI_STEER_SCRIPT){require(process.env.WIO_GEMINI_STEER_SCRIPT)}else{process.stdout.write('{}')}"`

// steeringConfigFile sits next to the per-turn hook script and holds the
// queue endpoint and its bearer credential. Gemini's shell tools inherit the
// CLI environment, so the credential must not travel in an environment
// variable; only the path to the script does.
const steeringConfigFile = "steering.json"

func ensureSteeringHook(env []string) error {
	thinkingSettingsMu.Lock()
	defer thinkingSettingsMu.Unlock()
	home, _ := os.UserHomeDir()
	for _, entry := range env {
		if v, ok := strings.CutPrefix(entry, "HOME="); ok {
			home = v
		}
	}
	for _, entry := range env {
		if v, ok := strings.CutPrefix(entry, "GEMINI_CLI_HOME="); ok && v != "" {
			home = v
		}
	}
	if home == "" {
		return errors.New("Gemini home directory unavailable")
	}
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe Gemini account settings directory")
	}
	path := filepath.Join(dir, "settings.json")
	settings, original, mode, err := readThinkingSettings(path)
	if err != nil {
		return err
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if settings["hooks"] != nil && !ok {
		return errors.New("invalid Gemini hooks")
	}
	before, ok := hooks["BeforeModel"].([]any)
	if hooks["BeforeModel"] != nil && !ok {
		return errors.New("invalid Gemini BeforeModel hooks")
	}
	for _, definition := range before {
		def, _ := definition.(map[string]any)
		list, _ := def["hooks"].([]any)
		for _, value := range list {
			hook, _ := value.(map[string]any)
			if hook["name"] == "wio-steering" {
				if hook["type"] != "command" || hook["command"] != steeringHookCommand {
					return errors.New("existing Wio steering hook conflicts with this Agent")
				}
				return nil
			}
		}
	}
	patches := []map[string]any{}
	if hooks == nil {
		patches = append(patches, map[string]any{"op": "add", "path": "/hooks", "value": map[string]any{}})
	}
	if before == nil {
		patches = append(patches, map[string]any{"op": "add", "path": "/hooks/BeforeModel", "value": []any{}})
	}
	patches = append(patches, map[string]any{"op": "add", "path": "/hooks/BeforeModel/-", "value": map[string]any{"hooks": []any{map[string]any{"name": "wio-steering", "type": "command", "command": steeringHookCommand, "timeout": 5000}}}})
	existed := original != nil
	documentRaw := original
	if documentRaw == nil {
		documentRaw = []byte("{}\n")
	}
	document, err := hujson.Parse(append([]byte(nil), documentRaw...))
	if err != nil {
		return err
	}
	patch, _ := json.Marshal(patches)
	if err = document.Patch(patch); err != nil {
		return err
	}
	document.Format()
	current, readErr := os.ReadFile(path)
	if (existed && (readErr != nil || !bytes.Equal(current, original))) || (!existed && !os.IsNotExist(readErr)) {
		return errors.New("Gemini settings changed; retry after saving configuration")
	}
	return writeThinkingSettings(path, document.Pack(), mode)
}

type steeringHint struct {
	ID, Text string
	Images   []protocol.TurnImage
	Applied  bool
}
type steeringTurn struct {
	mu     sync.Mutex
	ctx    context.Context
	thread string
	ready  bool
	closed bool
	hints  []steeringHint
}

func (a *Adapter) Steer(command protocol.GeminiSteerCommand) error {
	a.mu.Lock()
	turn := a.steering[command.OperationID]
	a.mu.Unlock()
	if turn == nil {
		return errors.New("Gemini turn cannot receive steering instructions")
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if turn.closed || turn.ctx.Err() != nil || turn.thread != command.ThreadID || !turn.ready {
		return errors.New("Gemini steering is unavailable for this turn")
	}
	if command.RequestID == "" || (strings.TrimSpace(command.Prompt) == "" && len(command.Images) == 0) || len(command.Prompt) > 8192 {
		return errors.New("invalid Gemini steering instruction")
	}
	if err := protocol.ValidateGeminiImages(command.Images); err != nil {
		return err
	}
	textBytes := len(command.Prompt)
	images := append([]protocol.TurnImage(nil), command.Images...)
	for _, hint := range turn.hints {
		if hint.ID == command.RequestID {
			if hint.Text != command.Prompt || !slices.Equal(hint.Images, command.Images) {
				return errors.New("Gemini steering request already has different content")
			}
			return nil
		}
		textBytes += len(hint.Text)
		images = append(images, hint.Images...)
	}
	if len(turn.hints) >= 32 || textBytes > 64<<10 {
		return errors.New("too many Gemini steering instructions in this turn")
	}
	if err := protocol.ValidateGeminiImages(images); err != nil {
		return errors.New("the running task has reached its image limit; wait for it to finish")
	}
	turn.hints = append(turn.hints, steeringHint{ID: command.RequestID, Text: command.Prompt, Images: slices.Clone(command.Images)})
	return nil
}

// BeforeModel's stable interface is text-only. The model retrieves queued
// images through this local MCP tool, which records them in native history.
func (a *Adapter) readSteeringImages(op, thread, requestID string) ([]any, error) {
	a.mu.Lock()
	turn := a.steering[op]
	a.mu.Unlock()
	if turn == nil {
		return nil, errors.New("the image update belongs to an expired task")
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if turn.closed || turn.ctx.Err() != nil || turn.thread != thread {
		return nil, errors.New("the image update belongs to an expired task")
	}
	for i := range turn.hints {
		hint := &turn.hints[i]
		if hint.ID != requestID || len(hint.Images) == 0 {
			continue
		}
		content := []any{map[string]string{"type": "text", "text": "User update supplied during this task: " + hint.Text}}
		for _, image := range hint.Images {
			prefix, data, _ := strings.Cut(image.DataURL, ",")
			content = append(content, map[string]string{"type": "image", "mimeType": strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64"), "data": data})
		}
		if !hint.Applied {
			raw, _ := json.Marshal(map[string]any{"operation_id": op, "request_id": hint.ID})
			if err := a.emit(protocol.StreamEvent{EventID: hint.ID + ":applied", StreamID: thread, Kind: "gemini.steer.applied", Payload: raw}); err != nil {
				return nil, err
			}
			hint.Applied = true
		}
		return content, nil
	}
	return nil, errors.New("no image update with this request_id exists in this task")
}

// Keep every applied hint in subsequent model requests: BeforeModel edits do
// not change Gemini's native transcript. The next Wio turn carries the applied
// updates into its prompt, where the CLI records them in native history.
func (a *Adapter) startSteering(ctx context.Context, op, thread string, env []string) ([]string, func(), error) {
	dir, err := os.MkdirTemp("", "wio-gemini-steering-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err = ensureSteeringHook(env); err != nil {
		cleanup()
		return nil, nil, err
	}
	script := filepath.Join(dir, "steering-hook.cjs")
	if err = os.WriteFile(script, steeringHook, 0600); err != nil {
		cleanup()
		return nil, nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	token := uuid.NewString() + uuid.NewString()
	config, _ := json.Marshal(map[string]string{"url": "http://" + listener.Addr().String(), "token": token})
	// MkdirTemp created the directory 0700; the file is private to the Agent
	// account as well and is removed with the directory when the turn ends.
	if err = os.WriteFile(filepath.Join(dir, steeringConfigFile), config, 0600); err != nil {
		_ = listener.Close()
		cleanup()
		return nil, nil, err
	}
	turn := &steeringTurn{ctx: ctx, thread: thread}
	emit := func(kind, id string, payload map[string]any) error {
		payload["operation_id"] = op
		raw, _ := json.Marshal(payload)
		return a.emit(protocol.StreamEvent{EventID: id, StreamID: thread, Kind: "gemini.steer." + kind, Payload: raw})
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		turn.mu.Lock()
		defer turn.mu.Unlock()
		if turn.closed || ctx.Err() != nil {
			http.Error(w, "turn ended", 409)
			return
		}
		if !turn.ready {
			if err := emit("ready", op+":steer-ready", map[string]any{}); err != nil {
				http.Error(w, "event unavailable", 503)
				return
			}
			turn.ready = true
		}
		hints := []string{}
		for i := range turn.hints {
			hint := &turn.hints[i]
			if !hint.Applied && len(hint.Images) == 0 {
				if err := emit("applied", hint.ID+":applied", map[string]any{"request_id": hint.ID}); err != nil {
					http.Error(w, "event unavailable", 503)
					return
				}
				hint.Applied = true
			}
			text := hint.Text
			if len(hint.Images) > 0 && !hint.Applied {
				text += "\nThe user also attached images. Before continuing, call the wio_read_images tool with request_id " + hint.ID + " to receive the actual images. Do not claim to have seen them before reading the tool result."
			}
			if text != "" {
				hints = append(hints, text)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"hints": hints})
	})}
	a.mu.Lock()
	if a.steering == nil {
		a.steering = map[string]*steeringTurn{}
	}
	a.steering[op] = turn
	a.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
	close := func() {
		turn.mu.Lock()
		turn.closed = true
		turn.mu.Unlock()
		a.mu.Lock()
		delete(a.steering, op)
		a.mu.Unlock()
		_ = server.Close()
		cleanup()
	}
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "WIO_GEMINI_STEER_") {
			result = append(result, entry)
		}
	}
	result = append(result, "WIO_GEMINI_STEER_SCRIPT="+script)
	return result, close, nil
}
