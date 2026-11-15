package httpapi

import "github.com/go-chi/chi/v5"

func (api *API) registerRoutes(router chi.Router) {
	router.Route("/api", func(r chi.Router) {
		api.registerPublicRoutes(r)
		r.Group(func(private chi.Router) {
			private.Use(api.authenticate, api.requireCSRF)
			api.registerSessionRoutes(private)
			api.registerSummaryRoutes(private)
			api.registerCollaborationRoutes(private)
			api.registerClaudeRoutes(private)
			api.registerGeminiRoutes(private)
			api.registerServerRoutes(private)
			api.registerProjectRoutes(private)
			api.registerWorkspaceRoutes(private)
			api.registerCodexRoutes(private)
			api.registerScheduledTaskRoutes(private)
			api.registerApprovalRoutes(private)
			api.registerSecretSetRoutes(private)
			api.registerCredentialProfileRoutes(private)
			api.registerSettingsRoutes(private)
			api.registerDeploymentRoutes(private)
			api.registerOperationRoutes(private)
			api.registerAlertRoutes(private)
			api.registerRealtimeRoutes(private)
		})
	})
}

func (api *API) registerPublicRoutes(r chi.Router) {
	r.Get("/health", api.health)
	r.Get("/setup/status", api.setupStatus)
	r.Post("/setup", api.setup)
	r.Post("/auth/login", api.loginHandler)
	r.Post("/agent/enroll", api.enrollAgent)
	r.Get("/agent/update-package/{architecture}", api.downloadAgentUpdate)
}

func (api *API) registerSessionRoutes(private chi.Router) {
	private.Get("/auth/session", api.session)
	private.Post("/auth/logout", api.logout)
}

func (api *API) registerSummaryRoutes(private chi.Router) {
	private.Get("/summary", api.summary)
}

func (api *API) registerCollaborationRoutes(private chi.Router) {
	private.Get("/sessions/{engine}/{threadID}/collaboration", api.sessionCollaboration)
	private.Put("/sessions/{engine}/{threadID}/collaboration", api.updateSessionCollaboration)
	private.Post("/sessions/{engine}/{threadID}/collaboration/read", api.markSessionCollaborationRead)
}

func (api *API) registerClaudeRoutes(private chi.Router) {
	private.Get("/claude/threads", api.claudeThreads)
	private.Post("/claude/threads", api.createClaudeThread)
	private.Get("/claude/threads/{threadID}", api.claudeThread)
	private.Get("/claude/threads/{threadID}/events", api.claudeEvents)
	private.Get("/claude/threads/{threadID}/usage", api.claudeUsage)
	private.Post("/claude/threads/{threadID}/turns", api.startClaudeTurn)
	private.Post("/claude/threads/{threadID}/events/{eventID}/rewrite", api.startClaudeTurn)
	private.Post("/claude/threads/{threadID}/fork", api.forkClaudeThread)
	private.Post("/claude/threads/{threadID}/fork-worktree", api.forkClaudeWorktree)
	private.Post("/projects/{projectID}/claude/threads/archive", api.archiveClaudeProject)
	private.Post("/claude/threads/{threadID}/interrupt", api.interruptClaudeTurn)
	private.Post("/claude/threads/{threadID}/steer", api.steerClaudeTurn)
	private.Post("/claude/threads/{threadID}/questions/{requestID}/answer", api.answerClaudeQuestion)
	private.Post("/claude/threads/{threadID}/confirm-stopped", api.confirmClaudeStopped)
	private.Patch("/claude/threads/{threadID}", api.updateClaudeThread)
	private.Delete("/claude/threads/{threadID}", api.deleteClaudeThread)
	private.Get("/workspaces/{workspaceID}/claude/models", api.workspaceClaudeModels)
	private.Post("/workspaces/{workspaceID}/claude/skills", api.refreshWorkspaceClaudeSkills)
	private.Get("/workspaces/{workspaceID}/claude/skills/{operationID}", api.workspaceClaudeSkillsResult)
}

func (api *API) registerGeminiRoutes(private chi.Router) {
	private.Post("/gemini/threads/{threadID}/fork", api.forkGeminiThread)
	private.Post("/gemini/threads/{threadID}/fork-worktree", api.forkGeminiWorktree)
	private.Post("/projects/{projectID}/gemini/threads/archive", api.archiveGeminiProject)
	private.Get("/gemini/threads", api.geminiThreads)
	private.Post("/gemini/threads", api.createGeminiThread)
	private.Get("/gemini/threads/{threadID}", api.geminiThread)
	private.Get("/gemini/threads/{threadID}/events", api.geminiEvents)
	private.Get("/gemini/threads/{threadID}/usage", api.geminiUsage)
	private.Get("/gemini/threads/{threadID}/goal", api.geminiGoal)
	private.Post("/gemini/threads/{threadID}/goals/{goalID}/{action}", api.controlGeminiGoal)
	private.Post("/gemini/threads/{threadID}/turns", api.startGeminiTurn)
	private.Post("/gemini/threads/{threadID}/events/{eventID}/rewrite", api.startGeminiTurn)
	private.Post("/gemini/threads/{threadID}/questions/{requestID}/answer", api.answerGeminiQuestion)
	private.Post("/gemini/threads/{threadID}/interrupt", api.interruptGeminiTurn)
	private.Post("/gemini/threads/{threadID}/confirm-stopped", api.confirmGeminiStopped)
	private.Post("/gemini/threads/{threadID}/steer", api.steerGeminiTurn)
	private.Patch("/gemini/threads/{threadID}", api.updateGeminiThread)
	private.Delete("/gemini/threads/{threadID}", api.deleteGeminiThread)
	private.Get("/workspaces/{workspaceID}/gemini/models", api.workspaceGeminiModels)
	private.Post("/workspaces/{workspaceID}/gemini/skills", api.refreshWorkspaceGeminiSkills)
	private.Post("/workspaces/{workspaceID}/gemini/skills/manage", api.manageWorkspaceGeminiSkill)
	private.Get("/workspaces/{workspaceID}/gemini/skills/{operationID}", api.workspaceGeminiSkillsResult)
}

func (api *API) registerServerRoutes(private chi.Router) {
	private.Get("/servers", api.servers)
	private.Patch("/servers/{serverID}", api.updateServer)
	private.Post("/servers/{serverID}/credential-profiles", api.updateServerCredentialProfiles)
	private.Post("/servers/{serverID}/agent-update", api.updateAgent)
	private.Post("/servers/{serverID}/codex-update", api.updateCodexCLI)
	private.Get("/servers/{serverID}/ai-clis", api.serverCLIs)
	private.Get("/ai-clis/{kind}/versions", api.cliVersions)
	private.Post("/servers/{serverID}/ai-clis/{kind}/install", api.installServerCLI)
	private.Post("/servers/enrollments", api.createEnrollment)
	private.Post("/servers/ssh/probe", api.probeServerSSH)
	private.Post("/servers/ssh/bootstrap", api.bootstrapServerSSH)
	private.Post("/servers/ssh/bootstrap-stream", api.streamBootstrapServerSSH)
	private.Post("/servers/{serverID}/ssh/repair", api.repairServerSSH)
	private.Post("/servers/{serverID}/ssh/repair-stream", api.streamRepairServerSSH)
	private.Post("/servers/{serverID}/ssh/uninstall", api.uninstallServerSSH)
	private.Delete("/servers/{serverID}", api.revokeServer)
	private.Get("/servers/{serverID}/metrics", api.metrics)
	private.Get("/servers/{serverID}/operation-metrics", api.operationMetrics)
	private.Post("/servers/{serverID}/gemini-credential-profile", api.updateServerGeminiCredentialProfile)
	private.Post("/servers/{serverID}/claude-credential-profile", api.updateServerClaudeCredentialProfile)
}

func (api *API) registerProjectRoutes(private chi.Router) {
	private.Get("/projects", api.projects)
	private.Post("/projects", api.createProject)
	private.Get("/projects/hidden", api.hiddenServerProjects)
	private.Get("/projects/{projectID}", api.projectDetail)
	private.Patch("/projects/{projectID}", api.updateProject)
	private.Get("/projects/{projectID}/operations", api.projectOperations)
	private.Post("/projects/import", api.importProject)
	private.Post("/projects/discover", api.discoverProjects)
	private.Post("/projects/{projectID}/retry-import", api.retryProjectImport)
	private.Post("/projects/{projectID}/retry-create", api.retryProjectCreation)
	private.Post("/projects/{projectID}/deletion-plan", api.projectDeletionPlan)
	private.Delete("/projects/{projectID}", api.deleteProject)
	private.Post("/projects/{projectID}/threads/archive", api.archiveProjectThreads)
}

func (api *API) registerWorkspaceRoutes(private chi.Router) {
	private.Get("/workspaces", api.workspaces)
	private.Patch("/workspaces/{workspaceID}", api.updateWorkspace)
	private.Post("/workspaces/{workspaceID}/move", api.moveWorkspace)
	private.Post("/workspaces/{workspaceID}/copy", api.copyWorkspace)
	private.Post("/workspaces/{workspaceID}/deletion-plan", api.workspaceDeletionPlan)
	private.Delete("/workspaces/{workspaceID}", api.deleteWorkspace)
	private.Get("/workspaces/{workspaceID}/git", api.workspaceGit)
	private.Post("/workspaces/{workspaceID}/git/refresh", api.refreshWorkspaceGit)
	private.Get("/workspaces/{workspaceID}/git/commits", api.workspaceGitCommits)
	private.Get("/workspaces/{workspaceID}/git/commits/{sha}", api.workspaceGitCommit)
	private.Post("/workspaces/{workspaceID}/git/branches", api.createGitBranch)
	private.Patch("/workspaces/{workspaceID}/git/branches/{branch}", api.renameGitBranch)
	private.Delete("/workspaces/{workspaceID}/git/branches/{branch}", api.deleteGitBranch)
	private.Patch("/workspaces/{workspaceID}/git/branches/*", api.renameGitBranch)
	private.Delete("/workspaces/{workspaceID}/git/branches/*", api.deleteGitBranch)
	private.Post("/workspaces/{workspaceID}/git/checkout", api.checkoutGit)
	private.Post("/workspaces/{workspaceID}/git/remotes", api.addGitRemote)
	private.Patch("/workspaces/{workspaceID}/git/remotes/{remote}", api.setGitRemote)
	private.Delete("/workspaces/{workspaceID}/git/remotes/{remote}", api.deleteGitRemote)
	private.Post("/workspaces/{workspaceID}/git/fetch", api.fetchGit)
	private.Post("/workspaces/{workspaceID}/git/pull", api.pullGit)
	private.Post("/workspaces/{workspaceID}/git/push", api.pushGit)
	private.Post("/workspaces/{workspaceID}/git/stage", api.stageGitChanges)
	private.Post("/workspaces/{workspaceID}/git/unstage", api.unstageGitChanges)
	private.Post("/workspaces/{workspaceID}/git/discard", api.discardGitChanges)
	private.Post("/workspaces/{workspaceID}/git/commit", api.commitGitChanges)
	private.Post("/workspaces/{workspaceID}/worktrees", api.createWorktree)
	private.Get("/workspaces/{workspaceID}/files", api.workspaceFiles)
	private.Post("/workspaces/{workspaceID}/files", api.writeWorkspaceFile)
	private.Put("/workspaces/{workspaceID}/file-content", api.writeWorkspaceFile)
	private.Post("/workspaces/{workspaceID}/files/refresh", api.refreshWorkspaceFiles)
	private.Get("/workspaces/{workspaceID}/file-preview", api.workspaceFilePreview)
	private.Post("/workspaces/{workspaceID}/file-preview", api.requestWorkspaceFilePreview)
	private.Post("/workspaces/{workspaceID}/image-preview", api.requestWorkspaceImagePreview)
	private.Get("/workspaces/{workspaceID}/changes", api.workspaceChanges)
	private.Post("/workspaces/{workspaceID}/changes/refresh", api.refreshWorkspaceChanges)
	private.Get("/workspaces/{workspaceID}/diff-preview", api.workspaceDiffPreview)
	private.Post("/workspaces/{workspaceID}/diff-preview", api.requestWorkspaceDiffPreview)
	private.Get("/workspaces/{workspaceID}/codex/mcp", api.workspaceCodexMCP)
	private.Get("/workspaces/{workspaceID}/codex/models", api.workspaceCodexModels)
	private.Post("/workspaces/{workspaceID}/codex/models/refresh", api.refreshWorkspaceCodexModels)
	private.Post("/workspaces/{workspaceID}/codex/mcp/refresh", api.refreshWorkspaceCodexMCP)
	private.Get("/workspaces/{workspaceID}/codex/skills", api.workspaceCodexSkills)
	private.Post("/workspaces/{workspaceID}/codex/skills/refresh", api.refreshWorkspaceCodexSkills)
}

func (api *API) registerCodexRoutes(private chi.Router) {
	private.Get("/threads", api.threads)
	private.Post("/threads", api.createThread)
	private.Get("/threads/{threadID}", api.thread)
	private.Patch("/threads/{threadID}", api.updateThread)
	private.Post("/threads/{threadID}/fork", api.forkThread)
	private.Post("/threads/{threadID}/fork-worktree", api.forkThreadToWorktree)
	private.Delete("/threads/{threadID}", api.deleteThread)
	private.Get("/threads/{threadID}/events", api.threadEvents)
	private.Get("/threads/{threadID}/usage", api.threadUsage)
	private.Post("/threads/{threadID}/turns", api.startTurn)
	private.Post("/threads/{threadID}/steer", api.steerTurn)
	private.Post("/threads/{threadID}/events/{eventID}/rewrite", api.rewriteTurn)
	private.Post("/threads/{threadID}/interrupt", api.interruptTurn)
	private.Get("/threads/{threadID}/codex/status", api.threadCodexStatus)
	private.Post("/threads/{threadID}/codex/status/refresh", api.refreshThreadCodexStatus)
	private.Get("/threads/{threadID}/goal", api.threadGoal)
	private.Post("/threads/{threadID}/goal/refresh", api.refreshThreadGoal)
	private.Put("/threads/{threadID}/goal", api.setThreadGoal)
	private.Delete("/threads/{threadID}/goal", api.clearThreadGoal)
	private.Post("/threads/{threadID}/compact", api.compactThread)
}

func (api *API) registerScheduledTaskRoutes(private chi.Router) {
	private.Get("/scheduled-tasks", api.scheduledTasks)
	private.Post("/scheduled-tasks", api.createScheduledTask)
	private.Put("/scheduled-tasks/{taskID}", api.updateScheduledTask)
	private.Delete("/scheduled-tasks/{taskID}", api.deleteScheduledTask)
}

func (api *API) registerApprovalRoutes(private chi.Router) {
	private.Get("/approvals", api.approvals)
	private.Post("/approvals/{approvalID}/decision", api.decideApproval)
}

func (api *API) registerSecretSetRoutes(private chi.Router) {
	private.Get("/secret-sets", api.secretSets)
	private.Post("/secret-sets", api.upsertSecretSet)
	private.Delete("/secret-sets/{secretID}", api.deleteSecretSet)
}

func (api *API) registerCredentialProfileRoutes(private chi.Router) {
	private.Get("/credential-profiles", api.credentialProfiles)
	private.Post("/credential-profiles", api.saveCredentialProfile)
	private.Delete("/credential-profiles/{profileID}", api.deleteCredentialProfile)
}

func (api *API) registerSettingsRoutes(private chi.Router) {
	private.Get("/settings/gemini-cli", api.geminiCLISettings)
	private.Post("/settings/gemini-cli/check-updates", api.checkGeminiCLIUpdates)
	private.Post("/settings/gemini-cli/select-version", api.selectGeminiCLIVersion)
	private.Get("/settings/claude-cli", api.claudeCLISettings)
	private.Post("/settings/claude-cli/check-updates", api.checkClaudeCLIUpdates)
	private.Post("/settings/claude-cli/select-version", api.selectClaudeCLIVersion)
	private.Get("/settings/codex-cli", api.codexCLISettings)
	private.Post("/settings/codex-cli/check-updates", api.checkCodexCLIUpdates)
	private.Post("/settings/codex-cli/select-version", api.selectCodexCLIVersion)
}

func (api *API) registerDeploymentRoutes(private chi.Router) {
	private.Get("/deployment-targets", api.deploymentTargets)
	private.Get("/deployment-targets/{targetID}/review", api.deploymentTargetReview)
	private.Post("/deployment-targets", api.createDeploymentTarget)
	private.Put("/deployment-targets/{targetID}", api.updateDeploymentTarget)
	private.Delete("/deployment-targets/{targetID}", api.deleteDeploymentTarget)
	private.Get("/deployments", api.deployments)
	private.Get("/deployments/{deploymentID}", api.deploymentDetails)
	private.Delete("/deployments/{deploymentID}", api.deleteDeployment)
	private.Post("/deployment-targets/{targetID}/deploy", api.startDeployment)
	private.Post("/deployment-targets/{targetID}/rollback", api.rollbackDeployment)
	private.Post("/deployment-targets/{targetID}/container", api.deploymentContainerAction)
	private.Post("/deployment-targets/{targetID}/container/{action}", api.deploymentContainerAction)
	private.Delete("/deployment-targets/{targetID}/container", api.deploymentContainerAction)
}

func (api *API) registerOperationRoutes(private chi.Router) {
	private.Get("/operations", api.operations)
	private.Get("/operations/{operationID}", api.operationStatus)
	private.Post("/operations/{operationID}/interrupt", api.interruptOperation)
}

func (api *API) registerAlertRoutes(private chi.Router) {
	private.Get("/alerts", api.alerts)
	private.Post("/alerts/{alertID}/acknowledge", api.acknowledgeAlert)
	private.Get("/audit", api.auditLog)
}

func (api *API) registerRealtimeRoutes(private chi.Router) {
	private.Get("/ws", api.websocket)
}
