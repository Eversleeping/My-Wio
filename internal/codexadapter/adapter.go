package codexadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type EmitFunc func(protocol.StreamEvent) error

type Adapter struct {
	command     string
	environment []string
	log         *slog.Logger
	emit        EmitFunc

	startMu sync.Mutex
	writeMu sync.Mutex
	mu      sync.Mutex
	process *exec.Cmd
	stdin   io.WriteCloser
	cancel  context.CancelFunc
	nextID  atomic.Int64
	pending map[string]chan rpcMessage
	approve map[string]approvalRequest
	threads map[string]string
	turns   map[string]turnState
}

type turnState struct {
	CodexThread string
	TurnID      string
	Active      bool
}

type approvalRequest struct {
	ID     json.RawMessage
	Method string
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcRequestError struct {
	Method  string
	Code    int
	Message string
}

func (e *rpcRequestError) Error() string {
	return fmt.Sprintf("Codex %s: %s (%d)", e.Method, e.Message, e.Code)
}

type requestFunc func(context.Context, string, any) (json.RawMessage, error)

func New(command string, log *slog.Logger, emit EmitFunc) *Adapter {
	return NewWithEnvironment(command, nil, log, emit)
}

func NewWithEnvironment(command string, environment []string, log *slog.Logger, emit EmitFunc) *Adapter {
	if command == "" {
		command = "codex"
	}
	return &Adapter{
		command:     command,
		environment: append([]string(nil), environment...),
		log:         log,
		emit:        emit,
		pending:     make(map[string]chan rpcMessage),
		approve:     make(map[string]approvalRequest),
		threads:     make(map[string]string),
		turns:       make(map[string]turnState),
	}
}

func (a *Adapter) StartTurn(ctx context.Context, command protocol.StartTurnCommand) error {
	if err := a.ensureStarted(ctx); err != nil {
		return err
	}
	return a.startTurn(ctx, command, a.request)
}

func (a *Adapter) RewriteTurn(ctx context.Context, command protocol.RewriteTurnCommand) error {
	if err := a.ensureStarted(ctx); err != nil {
		return err
	}
	return a.rewriteTurn(ctx, command, a.request)
}

func (a *Adapter) ForkThread(ctx context.Context, command protocol.ForkThreadCommand) (protocol.ForkThreadResult, error) {
	if err := a.ensureStarted(ctx); err != nil {
		return protocol.ForkThreadResult{}, err
	}
	return a.forkThread(ctx, command, a.request)
}

func (a *Adapter) forkThread(ctx context.Context, command protocol.ForkThreadCommand, request requestFunc) (protocol.ForkThreadResult, error) {
	if command.CodexThread == "" {
		return protocol.ForkThreadResult{}, errors.New("Codex thread is not initialized")
	}
	params := threadResumeParams(protocol.StartTurnCommand{Workspace: command.Workspace, ApprovalMode: "on-request"}, command.CodexThread)
	result, err := request(ctx, "thread/fork", params)
	if err != nil {
		return protocol.ForkThreadResult{}, err
	}
	id, err := responseThreadID(result, "thread/fork")
	if err != nil {
		return protocol.ForkThreadResult{}, err
	}
	a.mu.Lock()
	a.threads[id] = command.TargetThreadID
	a.mu.Unlock()
	return protocol.ForkThreadResult{CodexThread: id}, nil
}

func (a *Adapter) CodexOperation(ctx context.Context, kind string, payload json.RawMessage) (protocol.CodexCapabilityResult, error) {
	if err := a.ensureStarted(ctx); err != nil {
		return protocol.CodexCapabilityResult{}, err
	}
	return a.codexOperation(ctx, kind, payload, a.request)
}

func (a *Adapter) codexOperation(ctx context.Context, kind string, payload json.RawMessage, request requestFunc) (protocol.CodexCapabilityResult, error) {
	var base protocol.CodexSnapshotCommand
	if err := json.Unmarshal(payload, &base); err != nil {
		return protocol.CodexCapabilityResult{}, err
	}
	method := ""
	var params any
	switch kind {
	case "codex.goal.get", "codex.goal.clear":
		if base.CodexThread == "" {
			return unsupported(base.CodexVersion, "Codex thread is not initialized"), nil
		}
		method = map[string]string{"codex.goal.get": "thread/goal/get", "codex.goal.clear": "thread/goal/clear"}[kind]
		params = map[string]string{"threadId": base.CodexThread}
	case "codex.goal.set":
		var command protocol.CodexGoalSetCommand
		if err := json.Unmarshal(payload, &command); err != nil {
			return protocol.CodexCapabilityResult{}, err
		}
		if command.CodexThread == "" {
			return unsupported(command.CodexVersion, "Codex thread is not initialized"), nil
		}
		method = "thread/goal/set"
		params = map[string]any{"threadId": command.CodexThread, "objective": command.Objective, "status": command.Status, "tokenBudget": command.TokenBudget}
	case "codex.skills.list":
		method, params = "skills/list", map[string]any{"cwds": []string{base.Workspace}, "forceReload": true}
	case "codex.mcp.list":
		method, params = "mcpServerStatus/list", map[string]any{"threadId": nullableString(base.CodexThread), "limit": 100, "detail": "toolsAndAuthOnly"}
	case "codex.status.snapshot":
		method, params = "account/rateLimits/read", map[string]any{}
	default:
		return protocol.CodexCapabilityResult{}, fmt.Errorf("unsupported Codex operation %q", kind)
	}
	raw, err := request(ctx, method, params)
	if err != nil {
		var rpcErr *rpcRequestError
		if errors.As(err, &rpcErr) && (rpcErr.Code == -32601 || strings.Contains(strings.ToLower(rpcErr.Message), "not found") || strings.Contains(strings.ToLower(rpcErr.Message), "unsupported")) {
			return unsupported(base.CodexVersion, "This Codex version does not support "+method), nil
		}
		return protocol.CodexCapabilityResult{}, err
	}
	clean, err := sanitizeCodexResult(kind, raw)
	if err != nil {
		return protocol.CodexCapabilityResult{}, fmt.Errorf("invalid %s response: %w", method, err)
	}
	return protocol.CodexCapabilityResult{Supported: true, CodexVersion: base.CodexVersion, Data: clean}, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func unsupported(version, reason string) protocol.CodexCapabilityResult {
	return protocol.CodexCapabilityResult{Supported: false, Reason: reason, CodexVersion: version}
}

func sanitizeCodexResult(kind string, raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	switch kind {
	case "codex.goal.get", "codex.goal.set", "codex.goal.clear":
		return sanitizeGoalResult(value)
	case "codex.skills.list":
		return sanitizeSkillsResult(value)
	case "codex.mcp.list":
		return sanitizeMCPResult(value)
	case "codex.status.snapshot":
		return sanitizeStatusResult(value)
	}
	allowed := map[string]bool{}
	switch kind {
	case "codex.goal.get", "codex.goal.set", "codex.goal.clear":
		for _, k := range []string{"goal", "threadId", "objective", "status", "tokenBudget", "tokensUsed", "timeUsedSeconds", "createdAt", "updatedAt"} {
			allowed[k] = true
		}
	case "codex.skills.list":
		for _, k := range []string{"data", "cwd", "errors", "skills", "name", "description", "path", "scope", "enabled", "interface", "displayName", "shortDescription"} {
			allowed[k] = true
		}
	case "codex.mcp.list":
		for _, k := range []string{"data", "nextCursor", "name", "authStatus", "auth_status", "serverInfo", "server_info", "version", "tools", "resourceCount", "resource_count", "resourceTemplateCount", "resource_template_count"} {
			allowed[k] = true
		}
	case "codex.status.snapshot":
		for _, k := range []string{"rateLimits", "rate_limits", "rateLimitResetCredits", "primary", "secondary", "credits", "individualLimit", "limitId", "limit_id", "limitName", "limit_name", "planType", "plan_type", "rateLimitReachedType", "usedPercent", "used_percent", "windowDurationMins", "window_duration_mins", "resetsAt", "resets_at", "hasCredits", "has_credits", "unlimited", "balance", "availableCount", "limit", "remainingPercent", "used"} {
			allowed[k] = true
		}
	}
	clean := sanitizeValue(value, allowed)
	return json.Marshal(clean)
}

func sanitizeGoalResult(value any) (json.RawMessage, error) {
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("expected object")
	}
	raw, exists := root["goal"]
	if !exists || raw == nil {
		return json.RawMessage(`null`), nil
	}
	goal, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("expected goal object")
	}
	out := protocol.CodexGoal{ThreadID: stringField(goal, "threadId"), Objective: stringField(goal, "objective"), Status: stringField(goal, "status"), TokensUsed: int64Field(goal, "tokensUsed"), TimeUsedSeconds: int64Field(goal, "timeUsedSeconds"), CreatedAt: int64Field(goal, "createdAt"), UpdatedAt: int64Field(goal, "updatedAt")}
	if budget, ok := numberField(goal, "tokenBudget"); ok {
		converted := int64(budget)
		out.TokenBudget = &converted
	}
	return json.Marshal(out)
}

func sanitizeSkillsResult(value any) (json.RawMessage, error) {
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("expected object")
	}
	groups, _ := root["data"].([]any)
	out := make([]protocol.CodexSkill, 0)
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			continue
		}
		skills, _ := group["skills"].([]any)
		for _, rawSkill := range skills {
			skill, ok := rawSkill.(map[string]any)
			if !ok {
				continue
			}
			item := protocol.CodexSkill{Name: stringField(skill, "name"), Description: stringField(skill, "description"), Path: stringField(skill, "path"), Scope: stringField(skill, "scope"), Enabled: boolFieldDefault(skill, "enabled", true)}
			if detail, ok := skill["interface"].(map[string]any); ok {
				item.DisplayName = stringField(detail, "displayName")
				item.ShortDescription = stringField(detail, "shortDescription")
			}
			if item.Name != "" {
				out = append(out, item)
			}
		}
	}
	return json.Marshal(out)
}

func sanitizeMCPResult(value any) (json.RawMessage, error) {
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("expected object")
	}
	items, _ := root["data"].([]any)
	clean := make([]protocol.CodexMCPServer, 0, len(items))
	for _, item := range items {
		server, ok := item.(map[string]any)
		if !ok {
			continue
		}
		entry := protocol.CodexMCPServer{Name: stringField(server, "name"), AuthStatus: stringField(server, "authStatus"), Tools: []string{}, ResourceCount: collectionLength(server["resources"]), ResourceTemplateCount: collectionLength(server["resourceTemplates"])}
		if info, ok := server["serverInfo"].(map[string]any); ok {
			entry.ServerName = stringField(info, "name")
			entry.ServerVersion = stringField(info, "version")
		}
		if tools, ok := server["tools"].(map[string]any); ok {
			names := make([]string, 0, len(tools))
			for name := range tools {
				names = append(names, name)
			}
			slices.Sort(names)
			entry.Tools = names
		}
		if entry.Name != "" {
			clean = append(clean, entry)
		}
	}
	return json.Marshal(clean)
}

func sanitizeStatusResult(value any) (json.RawMessage, error) {
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("expected object")
	}
	snapshot := protocol.CodexStatusSnapshot{RateLimits: []protocol.CodexRateLimit{}}
	appendSnapshot := func(raw any, suffix string) {
		limit, ok := raw.(map[string]any)
		if !ok {
			return
		}
		base := stringField(limit, "limitName")
		if base == "" {
			base = stringField(limit, "limitId")
		}
		for _, windowName := range []string{"primary", "secondary"} {
			window, ok := limit[windowName].(map[string]any)
			if !ok {
				continue
			}
			name := strings.TrimSpace(strings.Join([]string{base, suffix, windowName}, " "))
			used, hasUsed := numberField(window, "usedPercent")
			item := protocol.CodexRateLimit{Name: name}
			if hasUsed {
				converted := int64(used)
				item.UsedPercent = &converted
			}
			if resets, ok := numberField(window, "resetsAt"); ok {
				item.ResetsAt = time.Unix(int64(resets), 0).UTC().Format(time.RFC3339)
			}
			snapshot.RateLimits = append(snapshot.RateLimits, item)
		}
	}
	if buckets, ok := root["rateLimitsByLimitId"].(map[string]any); ok && len(buckets) > 0 {
		keys := make([]string, 0, len(buckets))
		for key := range buckets {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			appendSnapshot(buckets[key], "")
		}
	} else {
		appendSnapshot(root["rateLimits"], "")
	}
	return json.Marshal(snapshot)
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	if len(value) > 4096 {
		return value[:4096]
	}
	return value
}
func numberField(object map[string]any, key string) (float64, bool) {
	value, ok := object[key].(float64)
	return value, ok
}
func int64Field(object map[string]any, key string) int64 {
	value, _ := numberField(object, key)
	return int64(value)
}
func boolFieldDefault(object map[string]any, key string, fallback bool) bool {
	value, ok := object[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

func collectionLength(value any) int {
	if values, ok := value.([]any); ok {
		return len(values)
	}
	return 0
}

func sanitizeValue(value any, allowed map[string]bool) any {
	switch typed := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, child := range typed {
			if allowed[key] {
				out[key] = sanitizeValue(child, allowed)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = sanitizeValue(typed[i], allowed)
		}
		return out
	case string:
		if len(typed) > 4096 {
			return typed[:4096]
		}
		return typed
	case float64, bool, nil:
		return typed
	default:
		return nil
	}
}

func (a *Adapter) rewriteTurn(ctx context.Context, command protocol.RewriteTurnCommand, request requestFunc) error {
	originalThread, err := a.prepareCodexThread(ctx, command.Start, request)
	if err != nil {
		return err
	}
	result, err := request(ctx, "thread/fork", threadResumeParams(command.Start, originalThread))
	if err != nil {
		return err
	}
	codexThread, err := responseThreadID(result, "thread/fork")
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.threads[codexThread] = command.Start.ThreadID
	a.mu.Unlock()
	if command.NumTurns > 0 {
		if _, err := request(ctx, "thread/rollback", map[string]any{"threadId": codexThread, "numTurns": command.NumTurns}); err != nil {
			return err
		}
	}
	return a.startPreparedTurn(ctx, command.Start, codexThread, map[string]any{
		"edit_event_id":        command.EditEventID,
		"replacement_event_id": command.ReplacementEventID,
		"replacement_payload":  command.ReplacementPayload,
		"cutoff_sequence":      command.CutoffSequence,
	}, request)
}

func (a *Adapter) startTurn(ctx context.Context, command protocol.StartTurnCommand, request requestFunc) error {
	codexThread, err := a.prepareCodexThread(ctx, command, request)
	if err != nil {
		return err
	}
	return a.startPreparedTurn(ctx, command, codexThread, nil, request)
}

func (a *Adapter) startPreparedTurn(ctx context.Context, command protocol.StartTurnCommand, codexThread string, acceptedMetadata map[string]any, request requestFunc) error {
	a.mu.Lock()
	a.threads[codexThread] = command.ThreadID
	a.turns[command.ThreadID] = turnState{CodexThread: codexThread, Active: true}
	a.mu.Unlock()
	params := turnStartParams(command, codexThread)
	result, err := request(ctx, "turn/start", params)
	if err != nil {
		a.mu.Lock()
		state := a.turns[command.ThreadID]
		state.Active = false
		a.turns[command.ThreadID] = state
		a.mu.Unlock()
		return err
	}
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		a.mu.Lock()
		state := a.turns[command.ThreadID]
		state.Active = false
		a.turns[command.ThreadID] = state
		a.mu.Unlock()
		return err
	}
	if response.Turn.ID == "" {
		a.mu.Lock()
		state := a.turns[command.ThreadID]
		state.Active = false
		a.turns[command.ThreadID] = state
		a.mu.Unlock()
		return errors.New("turn/start returned no turn id")
	}
	a.mu.Lock()
	a.turns[command.ThreadID] = turnState{CodexThread: codexThread, TurnID: response.Turn.ID, Active: true}
	if command.CodexThread != "" && command.CodexThread != codexThread {
		delete(a.threads, command.CodexThread)
	}
	a.mu.Unlock()
	accepted := map[string]any{"codex_thread_id": codexThread, "turn_id": response.Turn.ID}
	for key, value := range acceptedMetadata {
		accepted[key] = value
	}
	return a.emitEvent(command.ThreadID, "turn.accepted", accepted)
}

func (a *Adapter) prepareCodexThread(ctx context.Context, command protocol.StartTurnCommand, request requestFunc) (string, error) {
	codexThread := command.CodexThread
	if codexThread == "" {
		return a.startCodexThread(ctx, command, request)
	}
	a.mu.Lock()
	active := a.threads[codexThread] != ""
	a.mu.Unlock()
	if active {
		return codexThread, nil
	}
	result, err := request(ctx, "thread/resume", threadResumeParams(command, codexThread))
	if err != nil {
		if !isThreadNotFound(err) {
			return "", err
		}
		return a.startCodexThread(ctx, command, request)
	}
	resumed, err := responseThreadID(result, "thread/resume")
	if err != nil {
		return "", err
	}
	return resumed, nil
}

func (a *Adapter) startCodexThread(ctx context.Context, command protocol.StartTurnCommand, request requestFunc) (string, error) {
	result, err := request(ctx, "thread/start", threadStartParams(command))
	if err != nil {
		return "", err
	}
	codexThread, err := responseThreadID(result, "thread/start")
	if err != nil {
		return "", err
	}
	return codexThread, nil
}

func responseThreadID(result json.RawMessage, method string) (string, error) {
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return "", err
	}
	if response.Thread.ID == "" {
		return "", fmt.Errorf("%s returned no thread id", method)
	}
	return response.Thread.ID, nil
}

func threadStartParams(command protocol.StartTurnCommand) map[string]any {
	params := map[string]any{
		"cwd":            command.Workspace,
		"approvalPolicy": approvalPolicy(command.ApprovalMode),
		"sandbox":        "workspace-write",
	}
	if command.Model != "" {
		params["model"] = command.Model
	}
	return params
}

func threadResumeParams(command protocol.StartTurnCommand, codexThread string) map[string]any {
	params := threadStartParams(command)
	params["threadId"] = codexThread
	return params
}

func turnStartParams(command protocol.StartTurnCommand, codexThread string) map[string]any {
	input := []map[string]string{}
	if command.Prompt != "" {
		input = append(input, map[string]string{"type": "text", "text": command.Prompt})
	}
	for _, image := range command.Images {
		input = append(input, map[string]string{"type": "image", "url": image.DataURL})
	}
	params := map[string]any{
		"threadId":       codexThread,
		"input":          input,
		"approvalPolicy": approvalPolicy(command.ApprovalMode),
		"cwd":            command.Workspace,
	}
	if command.Model != "" {
		params["model"] = command.Model
	}
	if command.ReasoningEffort != "" {
		params["effort"] = command.ReasoningEffort
	}
	return params
}

func (a *Adapter) Interrupt(ctx context.Context, command protocol.InterruptTurnCommand) error {
	if err := a.ensureStarted(ctx); err != nil {
		return err
	}
	return a.interrupt(ctx, command, a.request)
}

func (a *Adapter) interrupt(ctx context.Context, command protocol.InterruptTurnCommand, request requestFunc) error {
	a.mu.Lock()
	state := a.turns[command.ThreadID]
	a.mu.Unlock()
	if command.CodexThread != "" {
		state.CodexThread = command.CodexThread
	}
	if command.TurnID != "" {
		state.TurnID = command.TurnID
	}
	if state.CodexThread == "" || state.TurnID == "" {
		return errors.New("no active turn is known for this session")
	}
	_, err := request(ctx, "turn/interrupt", map[string]string{"threadId": state.CodexThread, "turnId": state.TurnID})
	return err
}

func (a *Adapter) Decide(command protocol.ApprovalDecisionCommand) error {
	a.mu.Lock()
	request, ok := a.approve[command.RequestID]
	a.mu.Unlock()
	if !ok {
		return errors.New("approval request is no longer pending")
	}
	decision := approvalDecision(request.Method, command.Decision)
	if err := a.write(map[string]any{"id": request.ID, "result": map[string]any{"decision": decision}}); err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.approve, command.RequestID)
	a.mu.Unlock()
	return nil
}

func (a *Adapter) Close() error {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	a.stopLocked(errors.New("Codex app-server closed"))
	return nil
}

func (a *Adapter) ReconfigureEnvironment(environment []string, update func() error) error {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	a.mu.Lock()
	for _, turn := range a.turns {
		if turn.Active {
			a.mu.Unlock()
			return errors.New("Codex has an active turn; retry the credential update after it completes")
		}
	}
	a.mu.Unlock()
	if err := update(); err != nil {
		return err
	}
	a.environment = append([]string(nil), environment...)
	a.stopLocked(errors.New("Codex credentials changed"))
	return nil
}

func (a *Adapter) ReconfigureCommand(command string, update func() error) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("Codex command is required")
	}
	a.startMu.Lock()
	defer a.startMu.Unlock()
	a.mu.Lock()
	for _, turn := range a.turns {
		if turn.Active {
			a.mu.Unlock()
			return errors.New("Codex has an active turn; retry the CLI update after it completes")
		}
	}
	a.mu.Unlock()
	if err := update(); err != nil {
		return err
	}
	a.command = command
	a.stopLocked(errors.New("Codex CLI changed"))
	return nil
}

func (a *Adapter) stopLocked(reason error) {
	if a.cancel != nil {
		a.cancel()
	}
	if a.stdin != nil {
		_ = a.stdin.Close()
	}
	if a.process != nil && a.process.Process != nil {
		_ = a.process.Process.Kill()
	}
	a.reset(reason)
}

func (a *Adapter) ensureStarted(ctx context.Context) error {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	a.mu.Lock()
	running := a.process != nil
	a.mu.Unlock()
	if running {
		return nil
	}
	processContext, cancel := context.WithCancel(context.Background())
	process := exec.CommandContext(processContext, a.command, "app-server", "--listen", "stdio://")
	process.Env = append(os.Environ(), a.environment...)
	stdout, err := process.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		cancel()
		return err
	}
	stdin, err := process.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := process.Start(); err != nil {
		cancel()
		return fmt.Errorf("start codex app-server: %w", err)
	}
	a.mu.Lock()
	a.process = process
	a.stdin = stdin
	a.cancel = cancel
	a.mu.Unlock()
	go a.readLoop(stdout)
	go a.stderrLoop(stderr)
	go func() {
		err := process.Wait()
		a.log.Warn("Codex app-server exited", "error", err)
		a.resetProcess(process, fmt.Errorf("Codex app-server exited: %w", err))
	}()
	initializeContext, initializeCancel := context.WithTimeout(ctx, 20*time.Second)
	defer initializeCancel()
	_, err = a.request(initializeContext, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "wio", "title": "Wio", "version": "0.1.0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	})
	if err != nil {
		cancel()
		_ = stdin.Close()
		if process.Process != nil {
			_ = process.Process.Kill()
		}
		a.reset(err)
		return fmt.Errorf("initialize Codex app-server: %w", err)
	}
	return a.write(map[string]any{"method": "initialized", "params": map[string]any{}})
}

func (a *Adapter) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := a.nextID.Add(1)
	key := fmt.Sprintf("%d", id)
	response := make(chan rpcMessage, 1)
	a.mu.Lock()
	a.pending[key] = response
	a.mu.Unlock()
	if err := a.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		a.mu.Lock()
		delete(a.pending, key)
		a.mu.Unlock()
		return nil, err
	}
	select {
	case message, ok := <-response:
		if !ok {
			return nil, errors.New("Codex app-server disconnected")
		}
		if message.Error != nil {
			return nil, &rpcRequestError{Method: method, Message: message.Error.Message, Code: message.Error.Code}
		}
		return message.Result, nil
	case <-ctx.Done():
		a.mu.Lock()
		delete(a.pending, key)
		a.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (a *Adapter) write(value any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.mu.Lock()
	stdin := a.stdin
	a.mu.Unlock()
	if stdin == nil {
		return errors.New("Codex app-server is not running")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = stdin.Write(raw)
	return err
}

func (a *Adapter) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			a.log.Warn("invalid Codex app-server message", "error", err)
			continue
		}
		if len(message.ID) > 0 && message.Method == "" {
			a.handleResponse(message)
			continue
		}
		if len(message.ID) > 0 {
			a.handleServerRequest(message)
			continue
		}
		if message.Method != "" {
			a.handleNotification(message)
		}
	}
	if err := scanner.Err(); err != nil {
		a.log.Warn("reading Codex app-server output failed", "error", err)
	}
}

func (a *Adapter) stderrLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		a.log.Debug("Codex app-server", "message", scanner.Text())
	}
}

func (a *Adapter) handleResponse(message rpcMessage) {
	key := idKey(message.ID)
	a.mu.Lock()
	response := a.pending[key]
	delete(a.pending, key)
	a.mu.Unlock()
	if response != nil {
		response <- message
		close(response)
	}
}

func (a *Adapter) handleServerRequest(message rpcMessage) {
	if !isApproval(message.Method) {
		_ = a.write(map[string]any{"id": message.ID, "error": map[string]any{"code": -32601, "message": "Wio does not support this server request"}})
		return
	}
	requestID := idKey(message.ID)
	codexThread := findString(message.Params, "threadId", "conversationId")
	a.mu.Lock()
	wioThread := a.threads[codexThread]
	a.approve[requestID] = approvalRequest{ID: append(json.RawMessage(nil), message.ID...), Method: message.Method}
	a.mu.Unlock()
	if wioThread == "" {
		a.mu.Lock()
		delete(a.approve, requestID)
		a.mu.Unlock()
		_ = a.write(map[string]any{"id": message.ID, "error": map[string]any{"code": -32000, "message": "could not associate approval with a Wio session"}})
		return
	}
	_ = a.emitEvent(wioThread, "approval.requested", map[string]any{"request_id": requestID, "kind": message.Method, "detail": json.RawMessage(message.Params)})
}

func (a *Adapter) handleNotification(message rpcMessage) {
	codexThread := findString(message.Params, "threadId", "conversationId")
	turnID := notificationTurnID(message.Params)
	ignore := false
	a.mu.Lock()
	wioThread := a.threads[codexThread]
	if wioThread != "" && message.Method == "turn/started" && turnID != "" {
		state := a.turns[wioThread]
		state.CodexThread = codexThread
		state.TurnID = turnID
		state.Active = true
		a.turns[wioThread] = state
	}
	if wioThread != "" && terminalTurnNotification(message.Method) {
		state := a.turns[wioThread]
		if turnID != "" && state.TurnID != "" && state.TurnID != turnID {
			ignore = true
		} else {
			state.Active = false
			a.turns[wioThread] = state
		}
	}
	a.mu.Unlock()
	if wioThread == "" || ignore {
		return
	}
	if !persistNotification(message.Method) {
		return
	}
	kind := "codex." + strings.ReplaceAll(message.Method, "/", ".")
	_ = a.emit(protocol.StreamEvent{StreamID: wioThread, Kind: kind, OccurredAt: time.Now().UTC(), Payload: append(json.RawMessage(nil), message.Params...)})
}

func persistNotification(method string) bool {
	return !strings.HasSuffix(strings.ToLower(method), "delta")
}

func terminalTurnNotification(method string) bool {
	switch method {
	case "turn/completed", "turn/failed", "turn/cancelled":
		return true
	default:
		return false
	}
}

func (a *Adapter) emitEvent(streamID, kind string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return a.emit(protocol.StreamEvent{StreamID: streamID, Kind: kind, OccurredAt: time.Now().UTC(), Payload: raw})
}

func (a *Adapter) reset(reason error) {
	a.mu.Lock()
	failed := a.activeTurnsLocked()
	a.process = nil
	a.stdin = nil
	a.cancel = nil
	for key, response := range a.pending {
		delete(a.pending, key)
		close(response)
	}
	a.approve = make(map[string]approvalRequest)
	a.threads = make(map[string]string)
	a.turns = make(map[string]turnState)
	a.mu.Unlock()
	a.emitDisconnectedTurns(failed, reason)
	if reason != nil {
		a.log.Debug("Codex adapter reset", "reason", reason)
	}
}

func (a *Adapter) resetProcess(process *exec.Cmd, reason error) {
	a.mu.Lock()
	if a.process != process {
		a.mu.Unlock()
		return
	}
	failed := a.activeTurnsLocked()
	a.process = nil
	a.stdin = nil
	a.cancel = nil
	for key, response := range a.pending {
		delete(a.pending, key)
		close(response)
	}
	a.approve = make(map[string]approvalRequest)
	a.threads = make(map[string]string)
	a.turns = make(map[string]turnState)
	a.mu.Unlock()
	a.emitDisconnectedTurns(failed, reason)
	if reason != nil {
		a.log.Debug("Codex adapter reset", "reason", reason)
	}
}

func (a *Adapter) activeTurnsLocked() map[string]turnState {
	active := make(map[string]turnState)
	for threadID, state := range a.turns {
		if state.Active {
			active[threadID] = state
		}
	}
	return active
}

func (a *Adapter) emitDisconnectedTurns(turns map[string]turnState, reason error) {
	if reason == nil {
		return
	}
	for threadID, state := range turns {
		_ = a.emitEvent(threadID, "codex.turn.failed", map[string]any{
			"threadId": state.CodexThread,
			"turnId":   state.TurnID,
			"error":    map[string]string{"message": reason.Error()},
		})
	}
}

func isThreadNotFound(err error) bool {
	var requestError *rpcRequestError
	return errors.As(err, &requestError) && strings.Contains(strings.ToLower(requestError.Message), "thread not found")
}

func approvalPolicy(value string) string {
	switch value {
	case "never", "untrusted", "on-failure", "on-request":
		return value
	default:
		return "on-request"
	}
}

func approvalDecision(method, decision string) string {
	approved := decision == "approved"
	if strings.HasPrefix(method, "item/") {
		if approved {
			return "accept"
		}
		return "decline"
	}
	if approved {
		return "approved"
	}
	return "denied"
}

func isApproval(method string) bool {
	lower := strings.ToLower(method)
	return strings.Contains(lower, "approval") || strings.Contains(lower, "requestapproval")
}

func idKey(raw json.RawMessage) string {
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return string(raw)
}

func findString(raw json.RawMessage, keys ...string) string {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, key := range keys {
		if text, ok := value[key].(string); ok {
			return text
		}
	}
	return ""
}

func notificationTurnID(raw json.RawMessage) string {
	var value struct {
		TurnID string `json:"turnId"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	if value.TurnID != "" {
		return value.TurnID
	}
	return value.Turn.ID
}
