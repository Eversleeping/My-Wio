package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

// Operation dispatch has one signature. The optional execute entry preserves
// the older error-only entry point's accepted kinds and interruption lifetime.
type operationFunc func(*Client, context.Context, *protocol.ControlEnvelope) (string, json.RawMessage, bool, error)
type commandFunc func(*Client, context.Context, *protocol.ControlEnvelope) error
type operationHandler struct {
	run     operationFunc
	execute commandFunc
}

func commandOperation(execute commandFunc) operationHandler {
	return operationHandler{execute: execute, run: func(c *Client, ctx context.Context, envelope *protocol.ControlEnvelope) (string, json.RawMessage, bool, error) {
		return "", nil, false, execute(c, ctx, envelope)
	}}
}
func sessionCommand(engine string, run func(sessionRunner, context.Context, *protocol.ControlEnvelope) error) operationHandler {
	return commandOperation(func(c *Client, ctx context.Context, envelope *protocol.ControlEnvelope) error {
		return run(c.sessionRunner(engine), ctx, envelope)
	})
}
func interruptWithoutData(runner sessionRunner, ctx context.Context, envelope *protocol.ControlEnvelope) error {
	_, err := runner.Interrupt(ctx, envelope)
	return err
}
func sessionInterruption(engine string, execute commandFunc) operationHandler {
	return operationHandler{execute: execute, run: func(c *Client, ctx context.Context, envelope *protocol.ControlEnvelope) (string, json.RawMessage, bool, error) {
		return runSessionInterrupt(ctx, c.sessionRunner(engine), envelope)
	}}
}

var operationHandlers = map[string]operationHandler{
	protocol.OpAgentUpdate:                {run: (*Client).runAgentUpdateOperation},
	protocol.OpCLIInstall:                 commandOperation((*Client).executeCLIInstall),
	protocol.OpClaudeCredentialsConfigure: commandOperation((*Client).executeClaudeCredentialsConfigure),
	protocol.OpClaudeQuestionAnswer:       sessionCommand(session.Claude, sessionRunner.Answer),
	protocol.OpClaudeSkillsList:           {run: (*Client).runClaudeSkillsListOperation},
	protocol.OpClaudeThreadFork:           {run: (*Client).runClaudeThreadForkOperation},
	protocol.OpClaudeTurnInterrupt:        sessionInterruption(session.Claude, nil),
	protocol.OpClaudeTurnStart:            sessionCommand(session.Claude, sessionRunner.Start),
	protocol.OpClaudeTurnSteer:            {run: sessionCommand(session.Claude, sessionRunner.Steer).run},
	protocol.OpCodexApproval:              sessionCommand(session.Codex, sessionRunner.Answer),
	protocol.OpCodexCredentialsConfigure:  commandOperation((*Client).executeCredentialsConfigure),
	protocol.OpCodexGoalClear:             {run: (*Client).runCodexGoalPrefixOperation},
	protocol.OpCodexGoalGet:               {run: (*Client).runCodexGoalPrefixOperation},
	protocol.OpCodexGoalSet:               {run: (*Client).runCodexGoalPrefixOperation},
	protocol.OpCodexMCPList:               {run: (*Client).runCodexGoalPrefixOperation},
	protocol.OpCodexModelsList:            {run: (*Client).runCodexGoalPrefixOperation},
	protocol.OpCodexSkillsList:            {run: (*Client).runCodexGoalPrefixOperation},
	protocol.OpCodexStatusSnapshot:        {run: (*Client).runCodexGoalPrefixOperation},
	protocol.OpCodexThreadCompact:         {run: (*Client).runCodexGoalPrefixOperation},
	protocol.OpCodexThreadFork:            {run: (*Client).runCodexThreadForkOperation},
	protocol.OpCodexTurnInterrupt:         sessionCommand(session.Codex, interruptWithoutData),
	protocol.OpCodexTurnRewrite:           sessionCommand(session.Codex, sessionRunner.Start),
	protocol.OpCodexTurnStart:             sessionCommand(session.Codex, sessionRunner.Start),
	protocol.OpCodexTurnSteer:             sessionCommand(session.Codex, sessionRunner.Steer),
	protocol.OpCodexUpdate:                commandOperation((*Client).executeCodexUpdate),
	protocol.OpCredentialsConfigure:       commandOperation((*Client).executeCredentialsConfigure),
	protocol.OpDeployContainer:            {run: (*Client).runDeployContainerOperation},
	protocol.OpDeployRollback:             commandOperation((*Client).executeDeployRollback),
	protocol.OpDeployStart:                commandOperation((*Client).executeDeployStart),
	protocol.OpGeminiCredentialsConfigure: commandOperation((*Client).executeGeminiCredentialsConfigure),
	protocol.OpGeminiQuestionAnswer:       sessionCommand(session.Gemini, sessionRunner.Answer),
	protocol.OpGeminiSkillsList:           {run: (*Client).runGeminiSkillsListOperation},
	protocol.OpGeminiSkillsManage:         {run: (*Client).runGeminiSkillsManageOperation},
	protocol.OpGeminiThreadFork:           {run: (*Client).runGeminiThreadForkOperation},
	protocol.OpGeminiTurnInterrupt:        sessionInterruption(session.Gemini, (*Client).executeGeminiTurnInterrupt),
	protocol.OpGeminiTurnRewrite:          sessionCommand(session.Gemini, sessionRunner.Start),
	protocol.OpGeminiTurnStart:            sessionCommand(session.Gemini, sessionRunner.Start),
	protocol.OpGeminiTurnSteer:            sessionCommand(session.Gemini, sessionRunner.Steer),
	protocol.OpGitCredentialsConfigure:    commandOperation((*Client).executeCredentialsConfigure),
	protocol.OpGitImport:                  commandOperation((*Client).executeGitImport),
	protocol.OpGitProjectCreate:           {run: (*Client).runGitProjectCreateOperation},
	protocol.OpGitProjectDelete:           {run: (*Client).runGitProjectDeleteOperation},
	protocol.OpGitWorkspaceClone:          {run: (*Client).runGitWorkspaceCloneOperation},
	protocol.OpGitWorkspaceInspect:        {run: (*Client).runGitWorkspaceInspectOperation},
	protocol.OpGitWorkspaceLifecycle:      {run: (*Client).runGitWorkspaceLifecycleOperation},
	protocol.OpGitWorkspaceWrite:          {run: (*Client).runGitWorkspaceWriteOperation},
	protocol.OpGitWorktreeCleanup:         {run: (*Client).runGitWorktreeCleanupOperation},
	protocol.OpGitWorktreeCreate:          {run: (*Client).runGitWorktreeCreateOperation},
	protocol.OpInventoryScan:              commandOperation((*Client).executeInventoryScan),
	protocol.OpOperationInterrupt:         {run: (*Client).runOperationInterruptOperation},
	protocol.OpWorkspaceChanges:           {run: (*Client).runWorkspaceChangesOperation},
	protocol.OpWorkspaceDiffPreview:       {run: (*Client).runWorkspaceDiffPreviewOperation},
	protocol.OpWorkspaceFilePreview:       {run: (*Client).runWorkspaceFilePreviewOperation},
	protocol.OpWorkspaceFileWrite:         {run: (*Client).runWorkspaceFileWriteOperation},
	protocol.OpWorkspaceFiles:             {run: (*Client).runWorkspaceFilesOperation},
	protocol.OpWorkspaceImagePreview:      {run: (*Client).runWorkspaceFilePreviewOperation},
}

type operationPrefixHandler struct {
	prefix string
	run    operationFunc
}

// These two prefix families predate the exact-kind registry. Unknown suffixes
// must still reach the native validation/startup path before returning errors.
var operationPrefixHandlers = []operationPrefixHandler{
	{protocol.OpCodexGoalPrefix, (*Client).runCodexGoalPrefixOperation},
	{protocol.OpDeployContainerPrefix, (*Client).runDeployContainerOperation},
}

func operationHandlerFor(kind string) (operationFunc, bool) {
	if handler, ok := operationHandlers[kind]; ok {
		return handler.run, true
	}
	for _, handler := range operationPrefixHandlers {
		if strings.HasPrefix(kind, handler.prefix) {
			return handler.run, true
		}
	}
	return nil, false
}
