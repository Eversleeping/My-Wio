# P3-08 数据读取与页面请求盘点

基线 `2c9877b`，由 TypeScript AST 逐项读取源码；运行时值、凭据和部署环境不在本表。P2-06 已将会话模块移入 `sessions/engines/`，旧路线图的55个读取点已过时，实际 `useData` 调用为51处。

## 读取

| 位置 | 路径（源码表达式） | dependency | 缓存 |
| --- | --- | --- | --- |
| `web/src/App.tsx:46` | `auth === "authenticated" ? "/approvals" : null` | `realtimeRevisions.approvals` | false（默认） |
| `web/src/components/ClaudeSettings.tsx:13` | `"/settings/claude-cli"` | `realtime` | false（默认） |
| `web/src/components/CollaborationTaskDialog.tsx:61` | `open ? `/sessions/${engine}/${encodeURIComponent(sessionID)}/collaboration` : null` | `open` | false（默认） |
| `web/src/components/GeminiSettings.tsx:13` | `"/settings/gemini-cli"` | `realtime` | false（默认） |
| `web/src/components/HiddenProjectsButton.tsx:16` | `open ? "/projects/hidden" : null` | `realtime` | false（默认） |
| `web/src/components/SessionCollaboration.tsx:38` | `path` | ``${revision}:${tick}`` | false（默认） |
| `web/src/components/SessionCollaboration.tsx:39` | `settings ? "/workspaces" : null` | `settings` | false（默认） |
| `web/src/components/SessionUsage.tsx:39` | `path` | ``${throttled}:${tick}`` | false（默认） |
| `web/src/pages/DashboardPage.tsx:13` | `"/summary"` | `summaryRealtime` | false（默认） |
| `web/src/pages/DeploymentsPage.tsx:172` | `"/deployment-targets"` | `realtime` | false（默认） |
| `web/src/pages/DeploymentsPage.tsx:173` | `"/deployments"` | `realtime` | false（默认） |
| `web/src/pages/DeploymentsPage.tsx:178` | `"/workspaces"` | `realtime` | false（默认） |
| `web/src/pages/DeploymentsPage.tsx:179` | `"/servers"` | `realtime` | false（默认） |
| `web/src/pages/DeploymentsPage.tsx:180` | `"/secret-sets"` | `realtime` | false（默认） |
| `web/src/pages/DeploymentsPage.tsx:194` | `detailID ? `/deployments/${detailID}` : null` | `realtime` | false（默认） |
| `web/src/pages/DeploymentsPage.tsx:195` | `editingTarget ? `/deployment-targets/${editingTarget.id}/review` : null` | `realtime` | false（默认） |
| `web/src/pages/MonitoringPage.tsx:30` | `"/servers"` | `realtime` | false（默认） |
| `web/src/pages/MonitoringPage.tsx:31` | `"/alerts"` | `realtime` | false（默认） |
| `web/src/pages/MonitoringPage.tsx:35` | `serverID ? `/servers/${serverID}/metrics?hours=${rangeHours}` : null` | ``${realtime}:${serverID}:${rangeHours}`` | false（默认） |
| `web/src/pages/MonitoringPage.tsx:36` | `serverID ? `/servers/${serverID}/operation-metrics?hours=${rangeHours}` : null` | ``${realtime}:operation:${serverID}:${rangeHours}`` | false（默认） |
| `web/src/pages/OperationsPage.tsx:52` | `path` | `realtime` | false（默认） |
| `web/src/pages/ProjectsPage.tsx:55` | `resourcePath("/projects", { name: projectName, server_id: projectServerID, status: projectStatus })` | `realtime` | false（默认） |
| `web/src/pages/ProjectsPage.tsx:56` | `resourcePath("/workspaces", { path: workspacePath, server_id: workspaceServerID, git_status: workspaceGitStatus })` | `realtime` | false（默认） |
| `web/src/pages/ProjectsPage.tsx:57` | `"/projects/hidden"` | `realtime` | false（默认） |
| `web/src/pages/ProjectsPage.tsx:58` | `"/servers"` | `realtime` | false（默认） |
| `web/src/pages/ProjectsPage.tsx:68` | `detailProjectID ? `/projects/${detailProjectID}` : null` | `realtime` | false（默认） |
| `web/src/pages/ProjectsPage.tsx:200` | `gitWorkspaceID ? `/workspaces/${gitWorkspaceID}/git` : null` | `realtime` | false（默认） |
| `web/src/pages/ScheduledTaskDialog.tsx:128` | `open && form.agent_kind==="gemini" && geminiWorkspace ? `/workspaces/${geminiWorkspace}/gemini/models` : null` | `open` | false（默认） |
| `web/src/pages/ServersPage.tsx:59` | `"/servers"` | `realtime` | false（默认） |
| `web/src/pages/ServersPage.tsx:60` | `"/credential-profiles"` | `realtime` | false（默认） |
| `web/src/pages/SettingsPage.tsx:79` | ``/audit?limit=${auditListPageSize}`` | `realtime` | false（默认） |
| `web/src/pages/SettingsPage.tsx:151` | `"/settings/codex-cli"` | `realtime` | false（默认） |
| `web/src/pages/SettingsPage.tsx:152` | `"/credential-profiles"` | `realtime` | false（默认） |
| `web/src/pages/SettingsPage.tsx:153` | `"/secret-sets"` | `realtime` | false（默认） |
| `web/src/pages/SettingsPage.tsx:155` | `"/scheduled-tasks"` | `realtime` | false（默认） |
| `web/src/pages/SettingsPage.tsx:156` | `"/threads"` | `realtime` | false（默认） |
| `web/src/pages/SettingsPage.tsx:157` | `"/claude/threads"` | `realtime` | false（默认） |
| `web/src/pages/SettingsPage.tsx:158` | `"/gemini/threads"` | `realtime` | false（默认） |
| `web/src/sessions/engines/claude.tsx:65` | `selectedThreadID ? `/sessions/claude/${selectedThreadID}` : null` | `dependency` | false（默认） |
| `web/src/sessions/engines/claude.tsx:66` | `"/workspaces"` | `dependency` | false（默认） |
| `web/src/sessions/engines/claude.tsx:67` | `"/servers"` | `realtime` | false（默认） |
| `web/src/sessions/engines/claude.tsx:155` | ``/sessions/claude/${thread.id}/events?session=${encodeURIComponent(thread.session_id)}`` | `throttledRevision` | false（默认） |
| `web/src/sessions/engines/codex.tsx:75` | `"/workspaces"` | `realtime` | false（默认） |
| `web/src/sessions/engines/codex.tsx:95` | `selectedThreadID && !knownSelectedThread ? `/sessions/codex/${selectedThreadID}` : null` | ``${realtime}:${selectedThreadID}`` | false（默认） |
| `web/src/sessions/engines/gemini.tsx:56` | `"/workspaces"` | `realtime` | false（默认） |
| `web/src/sessions/engines/gemini.tsx:57` | `selectedThreadID ? `/sessions/gemini/${selectedThreadID}` : null` | `dependency` | false（默认） |
| `web/src/sessions/engines/gemini.tsx:210` | ``/sessions/gemini/${thread.id}/goal`` | ``${realtime}:${reload}:${thread.status}:${thread.operation_id}`` | false（默认） |
| `web/src/sessions/useSessionList.ts:21` | `path` | `revision` | false（默认） |
| `web/src/workspace/WorkspaceFilesPanel.tsx:32` | `workspaceID ? `/workspaces/${workspaceID}/files` : null` | ``${realtime}:${workspaceID}`` | false（默认） |
| `web/src/workspace/WorkspaceFilesPanel.tsx:33` | `workspaceID && mode === "changes" ? `/workspaces/${workspaceID}/changes` : null` | ``${realtime}:${workspaceID}:${mode}`` | false（默认） |
| `web/src/workspace/WorkspaceFilesPanel.tsx:34` | `workspaceID && mode === "changes" ? `/workspaces/${workspaceID}/git` : null` | ``${realtime}:${workspaceID}:${mode}`` | false（默认） |

## 页面直接请求

表中包含一次性读取和写入；刷新一栏列出所在函数内的显式 reload，嵌套操作另外复用页面的 refresh。页面迁移后由领域端点维护相同的刷新范围，失败写入保持原有错误处理。

| 位置 | 方法 | 路径（源码表达式） | 原有显式刷新 |
| --- | --- | --- | --- |
| `web/src/pages/DeploymentsPage.tsx:214` | put | ``/deployment-targets/${editingTarget.id}`` | targets.reload |
| `web/src/pages/DeploymentsPage.tsx:215` | post | `"/deployment-targets"` | targets.reload |
| `web/src/pages/DeploymentsPage.tsx:221` | post | ``/deployment-targets/${target.id}/deploy`` | targets.reload, deployments.reload |
| `web/src/pages/DeploymentsPage.tsx:222` | post | ``/deployment-targets/${target.id}/rollback`` | targets.reload, deployments.reload |
| `web/src/pages/DeploymentsPage.tsx:226` | post | ``/deployment-targets/${target.id}/container`` | targets.reload |
| `web/src/pages/DeploymentsPage.tsx:236` | remove | ``/deployment-targets/${target.id}`` | targets.reload, deployments.reload |
| `web/src/pages/DeploymentsPage.tsx:237` | remove | ``/deployments/${item.id}`` | deployments.reload |
| `web/src/pages/MonitoringPage.tsx:74` | post | ``/alerts/${alert.id}/acknowledge`` | alerts.reload |
| `web/src/pages/OperationsPage.tsx:64` | post | ``/operations/${encodeURIComponent(operation.id)}/interrupt`` | operations.reload |
| `web/src/pages/ProjectsPage.tsx:103` | post | `"/projects"` | projects.reload, workspaces.reload |
| `web/src/pages/ProjectsPage.tsx:106` | post | `"/projects/import"` | projects.reload, workspaces.reload |
| `web/src/pages/ProjectsPage.tsx:114` | post | `"/projects/discover"` | projects.reload, workspaces.reload |
| `web/src/pages/ProjectsPage.tsx:130` | post | ``/projects/${project.id}/${blank ? "retry-create" : "retry-import"}`` | projects.reload |
| `web/src/pages/ProjectsPage.tsx:140` | post | ``/projects/${project.id}/deletion-plan`` | 见调用方/操作结果 |
| `web/src/pages/ProjectsPage.tsx:149` | api | ``/projects/${deleteProjectID}`` | projects.reload, workspaces.reload |
| `web/src/pages/ProjectsPage.tsx:161` | patch | ``/projects/${workspace.project_id}`` | projects.reload, hiddenProjects.reload |
| `web/src/pages/ProjectsPage.tsx:172` | patch | ``/projects/${detailProjectID}`` | projects.reload, hiddenProjects.reload, detail.reload |
| `web/src/pages/ProjectsPage.tsx:205` | post | ``/workspaces/${workspaceID}/git/refresh`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:220` | post | ``${base}/branches`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:221` | patch | ``${base}/branches/${segment(action.branch)}`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:222` | remove | ``${base}/branches/${segment(action.branch)}?force=${action.force}`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:223` | post | ``${base}/checkout`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:224` | post | ``${base}/remotes`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:225` | patch | ``${base}/remotes/${segment(action.remote)}`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:226` | remove | ``${base}/remotes/${segment(action.remote)}`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:227` | post | ``${base}/fetch`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:228` | post | ``${base}/pull`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:229` | post | ``${base}/push`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:230` | post | ``${base}/stage`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:231` | post | ``${base}/unstage`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:232` | post | ``${base}/discard`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:233` | post | ``${base}/commit`` | gitSnapshot.reload |
| `web/src/pages/ProjectsPage.tsx:251` | patch | ``/workspaces/${manageWorkspaceID}`` | workspaces.reload |
| `web/src/pages/ProjectsPage.tsx:257` | post | ``/workspaces/${manageWorkspaceID}/move`` | workspaces.reload |
| `web/src/pages/ProjectsPage.tsx:263` | post | ``/workspaces/${manageWorkspaceID}/copy`` | workspaces.reload |
| `web/src/pages/ProjectsPage.tsx:269` | post | ``/workspaces/${manageWorkspaceID}/deletion-plan?force=${force}`` | 见调用方/操作结果 |
| `web/src/pages/ProjectsPage.tsx:275` | remove | ``/workspaces/${manageWorkspaceID}?mode=${mode}&force=${force}`` | workspaces.reload, projects.reload |
| `web/src/pages/ScheduledTaskDialog.tsx:112` | put | ``/scheduled-tasks/${form.id}`` | 见调用方/操作结果 |
| `web/src/pages/ScheduledTaskDialog.tsx:113` | post | `"/scheduled-tasks"` | 见调用方/操作结果 |
| `web/src/pages/ServersPage.tsx:110` | api | `"/servers"` | 见调用方/操作结果 |
| `web/src/pages/ServersPage.tsx:120` | post | `"/servers/ssh/probe"` | 见调用方/操作结果 |
| `web/src/pages/ServersPage.tsx:131` | postStream | `installPath` | servers.reload |
| `web/src/pages/ServersPage.tsx:181` | post | `"/servers/ssh/probe"` | 见调用方/操作结果 |
| `web/src/pages/ServersPage.tsx:189` | post | ``/servers/${retiringServer.id}/ssh/uninstall`` | servers.reload |
| `web/src/pages/ServersPage.tsx:205` | remove | ``/servers/${target.id}`` | servers.reload |
| `web/src/pages/ServersPage.tsx:225` | patch | ``/servers/${editingServer.id}`` | servers.reload |
| `web/src/pages/ServersPage.tsx:233` | post | ``/servers/${server.id}/agent-update`` | 见调用方/操作结果 |
| `web/src/pages/SettingsPage.tsx:100` | api | ``/audit?limit=${auditListPageSize}&offset=${next}`` | 见调用方/操作结果 |
| `web/src/pages/SettingsPage.tsx:123` | api | ``/audit?limit=${auditListPageSize}&offset=${offset}`` | 见调用方/操作结果 |
| `web/src/pages/SettingsPage.tsx:186` | post | `"/credential-profiles"` | profiles.reload |
| `web/src/pages/SettingsPage.tsx:190` | post | `"/secret-sets"` | secrets.reload |
| `web/src/pages/SettingsPage.tsx:191` | post | `"/settings/codex-cli/check-updates"` | codexSettings.reload |
| `web/src/pages/SettingsPage.tsx:192` | post | `"/settings/codex-cli/select-version"` | codexSettings.reload |
| `web/src/pages/SettingsPage.tsx:197` | put | ``/scheduled-tasks/${task.id}`` | scheduledTasks.reload |
| `web/src/pages/SettingsPage.tsx:204` | remove | ``/scheduled-tasks/${task.id}`` | scheduledTasks.reload |
| `web/src/pages/SettingsPage.tsx:211` | remove | ``/credential-profiles/${profile.id}`` | profiles.reload |
| `web/src/pages/SettingsPage.tsx:218` | remove | ``/secret-sets/${secret.id}`` | secrets.reload |
