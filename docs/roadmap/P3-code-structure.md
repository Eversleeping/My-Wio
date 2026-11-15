# P3 代码结构

返回[路线图总览](README.md)。状态以总览的“进度总表”为准，本文件只写步骤细节和执行记录。

本阶段有两类工作。P3-01 至 P3-07 和 P3-10 是拆分巨型文件，原则上只移动代码、不改行为。P3-08 和 P3-09 是前端取数方式的收敛，会改代码的组织方式，但用户看到的结果不变。

## 现状

### 后端的六个大文件

| 文件 | 行数 | 里面有什么 |
| --- | --- | --- |
| `internal/httpapi/api.go` | 851 | `New` 函数里的一个 `router.Route("/api", ...)` 代码块（第 107 行起，约 200 行）注册了全部 191 条路由。其余是健康检查、初始化、登录、认证中间件、WebSocket、静态资源和几个公共辅助函数 |
| `internal/httpapi/resources.go` | 3258 | 按行号顺序：概览、服务器与注册与指标、项目、工作区与 Git、导入与发现、工作区文件与预览、Codex 线程与轮次、审批、密钥集、部署、告警与审计 |
| `internal/store/store.go` | 2641 | 打开数据库与启动迁移（21 个 `migrate*` 函数里有 14 个在本文件）、用户与登录会话、服务器与注册、清单与项目、工作区与各类快照、操作队列、事件、线程改写、指标 |
| `internal/store/resources.go` | 1384 | `Thread` 结构体与 Codex 线程方法、审批、密钥集、部署目标与部署记录、告警、审计 |
| `internal/agentgateway/gateway.go` | 1516 | 连接与收发、上行信封分发 `handle`（约 200 行）、操作结果处理 `handleOperationResult`（约 500 行，由三十多处 `operation.Kind == "..."` 判断串成）、Codex 轮次收尾、审批、标题生成、指标告警 |
| `internal/agent/client.go` | 1428 | 连接与重连、`handleOperation`（约 440 行）、`execute`（约 230 行，一个按操作类型分发的 `switch`）、Git 读写、项目创建与删除、部署、心跳、指标、清单 |

这几个包里已经有按领域拆出去的文件，例如 `httpapi/claude.go`、`httpapi/workspace_lifecycle.go`、`store/workspace_git.go`、`agentgateway/gemini.go`。新文件沿用同样的命名方式。

超过 1000 行但不在本阶段处理的文件：`internal/codexadapter/adapter.go`（1637 行，P2-09 会改它）、`internal/sshbootstrap/bootstrap.go`（1130 行）、`internal/deployer/deployer.go`（1034 行）。后两个职责单一，按决策 D7 只做维护。

### 前端

- **`web/src/App.tsx`，2133 行，约 148 KB。** 它同时承担六种职责：应用外壳（登录态、布局、懒加载路由）；实时通道（一条 WebSocket 驱动 11 个刷新范围）；地址栏路由（`navigation`、`readLocationState`、`locationFor`）；Codex 会话的全部实现（`SessionView` 约 460 行，加上线程列表、事件分页、审批、子 Agent 活动、事件渲染）；工作区文件面板（`WorkspaceFilesPanel`、`useWorkspacePreview`）；被各页面复用的小组件和函数（`Field`、`Dialog`、`ErrorBanner`、`message`）。
- **页面反过来依赖外壳。** `pages/CodexPage.tsx`、`pages/ClaudePage.tsx`、`pages/GeminiPage.tsx`、`pages/claudeManagement.tsx`、`pages/geminiManagement.tsx`、`components/SessionPreviews.tsx` 都从 `App` 导入符号，另有 17 个测试文件导入 `App`。外壳懒加载页面，页面又静态导入外壳，形成环。
- **取数。** `web/src/useData.ts` 的 `useData(path, dependency, cache)` 有 55 个调用点，分布在 19 个文件里。路径是各处手拼的字符串；`dependency` 是任意值，变化就重新请求，通常传入实时通道给出的修订号；缓存是按路径存放的模块级 `Map`，由 `cache` 参数逐处选择是否启用。写操作直接调用 `api.ts` 的 `post`、`patch` 等函数（`App.tsx` 39 处、`ProjectsPage.tsx` 25 处），成功后各自手工调用 `reload`。
- **定时轮询。** 除 `main.tsx` 的两处（Service Worker 更新、前端版本检查）之外，还有 8 处 `setInterval` 和 3 个带退避的等待循环，清单见 P3-09。
- **`web/src/styles.css`，2604 行。** 由 `main.tsx` 一次性导入。前约 1700 行是基础样式与深色主题，其后是按区域分节的浅色主题。
- **`web/src/i18n.tsx`，2821 行，约 166 KB。** 一个 `en` 对象和一个类型为 `Record<MessageKey, string>` 的 `zh` 对象，各约 1380 个键，加上 Provider 和 `useI18n`。键名带领域前缀，数量最多的是 `codex.`（323）、`server.`（205）、`project.`（185）、`gemini.`（153）、`settings.`（120）。

## 目标

1. 后端的 HTTP 处理器、Store、网关、Agent 客户端里，没有超过 1000 行的非测试文件。
2. `App.tsx` 只剩应用外壳，不超过 400 行；`pages/` 和 `components/` 下没有文件从 `App` 导入。
3. 取数路径、刷新范围、写后失效各有一处定义，页面不再手拼路径和手工串联 `reload`。
4. 定时器只出现在少数几个有名字的公共钩子里，每一处都写明它在弥补什么。

不在本阶段范围内：

- 改变任何接口、数据结构或界面行为。
- 合并三种引擎的实现。那是 P2 的工作；本阶段只是把文件拆开，让 P2 的改动落在小文件上。
- 引入新的前端框架或路由库。是否引入 TanStack Query 由决策 D5 在 P3-08 进行中决定。

## 设计

### “纯移动”的定义与校验

标注为“纯移动”的提交只允许三种改动：把顶层声明从一个文件搬到同一个包的另一个文件；增删 `import`；前端文件里为被搬走的符号加上 `export`。不改名、不调整函数体、不顺手修缺陷。

Go 代码可以用下面的校验和自动确认。它去掉包声明、`import` 和空行后把包内所有非测试文件的行排序再求和，同一个包内的纯移动不会改变结果。

```bash
# 用法：pure_move_sum internal/httpapi
# 在改动前后各运行一次，两次输出必须相同
pure_move_sum() {
  ls "$1"/*.go | grep -v _test.go | xargs cat \
    | grep -vE '^(package |import \(|import "|\)$|$)' \
    | grep -vE '^[[:space:]]+([A-Za-z_.]+ )?"[^"]+"$' \
    | LC_ALL=C sort | sha256sum
}
```

基线提交 `0247c1c` 上四个包的校验和前 16 位，供第一次拆分前核对工作区是否干净：

| 包 | 校验和前缀 |
| --- | --- |
| `internal/httpapi` | `6cfa3d90d4451573` |
| `internal/store` | `da0190ed9e5e9ec9` |
| `internal/agentgateway` | `d5ffd87d8342e6e3` |
| `internal/agent` | `2242abc1addf6a02` |

基线之后只要有别的提交改过这些包，前缀就会变。实际执行时以“本步骤开始前”的输出为准，把前后两次的值都写进“执行记录”。

前端文件的移动需要增删 `export`、改写多行 `import`，没有同样可靠的校验和。改用下面两条：

- `git diff --color-moved=dimmed-zebra --color-moved-ws=allow-indentation-change` 逐段确认，除 `import`、`export` 之外没有新增或删除的行。
- 全部 Vitest 用例、类型检查和 Playwright 用例通过。

### 后端目标布局

下表的行号是基线提交里各段的起始位置，实际边界以函数边界为准。

**`internal/httpapi`**（来自 `resources.go`）

| 新文件 | 内容 | 起始行 |
| --- | --- | --- |
| `summary.go` | 概览 | 31 |
| `servers.go` | 服务器列表、注册、指标 | 66 |
| `projects.go` | 项目列表、创建、远端准备、重试、更新 | 266 |
| `workspaces.go` | 工作区列表与详情 | 696 |
| `workspace_git.go` | Git 状态、写操作排队、分支、远端、拉取、推送、暂存、提交、创建 worktree | 710 |
| `project_import.go` | 导入、发现、`deleteProjectLegacy` | 1146 |
| `workspace_files.go` | 文件树、预览、变更、差异 | 1327 |
| `codex_threads.go` | 线程列表、详情、更新、分叉、创建、删除、事件，以及 `forkThreadToWorktree`（1078） | 1657 |
| `codex_turns.go` | 发起、编辑重发、追加、`handleTurn`、中断 | 2014 |
| `approvals.go` | 审批 | 2313 |
| `secret_sets.go` | 密钥集 | 2380 |
| `deployments.go` | 部署目标、发起、回滚、容器动作、环境变量解析 | 2433 |
| `alerts.go` | 告警与审计 | 3113 |

文件末尾的辅助函数各自跟着唯一的调用方走；被多个领域使用的并入 `api.go` 已有的辅助函数区。拆完后 `resources.go` 删除。

**`internal/store`**（来自 `store.go` 和 `resources.go`）

| 新文件 | 内容 | 来源与起始行 |
| --- | --- | --- |
| `store.go`（保留） | `Store`、`Open`、`OpenWithOptions`、`Q`、`NewID` | `store.go` 54、650 |
| `migrations_legacy.go` | 本文件里的 14 个 `migrate*` 函数 | `store.go` 202 至 621、1101、2616 |
| `users.go` | 用户与登录会话 | `store.go` 672 |
| `servers.go` | 服务器、注册、心跳 | `store.go` 769 |
| `projects.go` | 清单写入、项目、`ProjectByRemote` | `store.go` 1085；`resources.go` 449 |
| `workspaces.go` | 工作区，文件、变更、差异快照 | `store.go` 1469 |
| `operations.go` | 操作队列：入队、领取、完成、查询 | `store.go` 1721 |
| `events.go` | 事件写入与分页读取 | `store.go` 2221 |
| `codex_threads.go` | `Thread` 与线程方法、`RewriteThread`、`CommitThreadRewrite` | `resources.go` 22；`store.go` 2401 |
| `approvals.go` | 审批 | `resources.go` 388 |
| `secret_sets.go` | 密钥集 | `resources.go` 532 |
| `deployments.go` | 部署目标、部署记录、快照、状态、容器状态 | `resources.go` 635 |
| `alerts.go`、`audit.go` | 告警；审计 | `resources.go` 1324、1351 |
| `metrics.go` | `SaveMetrics` | `store.go` 2592 |

`CodexSnapshot` 相关方法（`store.go` 1686）并入已有的 `codex_snapshots.go`。拆完后 `resources.go` 删除。

**`internal/agentgateway`**（来自 `gateway.go`）

| 新文件 | 内容 |
| --- | --- |
| `gateway.go`（保留） | `Gateway`、`New`、`Wake`、`Connect`、`sendKeepalive`、`receiveWithTools`、`handle`、`flush`、`publish` |
| `operation_results.go` | `handleOperationResult`、`publishOperationResult`、`reconcileOperation`、`reconcileCompletedTurn`、`uncertainStatus` |
| `codex.go` | `acceptCodexTurn`、`failCodexTurn`、`failCodexControlOperation`、`completedTurnStatus`、`queueBestEffortInterrupt`、`upsertApproval`、`bindCodexThread`、标题相关函数 |
| `scheduled_turns.go` | `scheduledTurnStatus`、`scheduledTurnMessage` |
| `deployments.go` | `failLostDeployment`、`failDeploymentOperation` |
| `alerts.go` | `evaluateMetrics`、`evaluateOperationLatency` |

**`internal/agent`**（来自 `client.go`）

| 新文件 | 内容 |
| --- | --- |
| `client.go`（保留） | `Client`、`NewClient`、`Run`、`connect`、`sendLoop`、`periodic`、`queue` |
| `operations.go` | `operationContext`、`handleOperation`、`execute` |
| `workspace_git.go` | `inspectWorkspaceGit`、`writeWorkspaceGit` |
| `projects.go` | `createProject`、`deleteProject`、`projectCreateDestination`、`safeProjectName` |
| `deployments.go` | `runDeployment`、`runRollback`、`runContainerAction`、脱敏函数 |
| `telemetry.go` | `enqueueHeartbeat`、`enqueueMetrics`、`enqueueInventory`、`inventoryRoots`、`managedRoots`、`refreshInventory` |
| `codex.go` | `codexEnvironment`、`currentCodexPath`、`setCodexPath` |

### 前端目标布局

```text
web/src/
  App.tsx                  应用外壳：登录态、布局、导航、懒加载路由
  navigation.ts            navigation、readLocationState、locationFor
  text.ts                  message、pretty、copyText 等文本辅助函数
  realtime/
    scopes.ts              刷新范围、realtimeScopesForEvent、初始修订号
    useRealtime.ts         WebSocket 连接、重连退避、修订号
  components/
    FormUI.tsx             Field、ErrorBanner、LanguageSwitch、LoadingScreen、带翻译的 Dialog 包装
    LazyMarkdown.tsx       懒加载 Markdown 渲染器的 MarkdownContent 包装
    FilePanes.tsx          FilePreviewPane、EditableFilePreviewPane、FileDiffPane、PreviewStatusNote
  workspace/
    WorkspaceFilesPanel.tsx  WorkspaceFilesPanel、ChangedFilesView、FileTreeItem
    fileTree.ts              buildFileTree
    useWorkspacePreview.ts
  codex/
    SessionView.tsx        SessionView、CreateThread、SnapshotNotice
    useThreadList.ts       mergeThreads、useThreadList、groupThreadsByWorkspace
    useThreadEvents.ts     线程事件缓存、mergeThreadEvents、useThreadEvents
    conversation.ts        conversationEvents、groupCommandEvents、normalize*、collectSubagentActivity
    events.tsx             ConversationEventItem、SubagentEvent、ToolEvent、CommandEventGroup、RawEventItem、WorkingIndicator
    approvals.tsx          ApprovalPrompt、ApprovalActions、approvalDetail
    waiters.ts             waitForGoalSnapshot、waitForThread、waitForWorkspace、waitForBackoff
  data/                    P3-08 建立，见该步骤
  styles/、i18n/           P3-10 建立，见该步骤
```

文件名是建议，执行时可以调整，但同一类东西只放一处。`web/src/codex/` 是过渡位置，P2-06 迁移 Codex 时会把它并入 `web/src/sessions/`。

## 步骤

### P3-01 路由表按领域拆分

**做法**

1. 先提交一个路由表快照测试，再动路由。用 `chi.Walk` 遍历路由器，输出每条路由的方法、路径和中间件个数，排序后与测试数据目录里的快照文件比较。`New` 的返回类型是 `http.Handler`；测试写在包内，对返回值做 `chi.Routes` 类型断言，断言不成立时改为在包内提供一个只供测试使用的取路由器函数。
2. 把 `router.Route("/api", ...)` 代码块里的注册语句按领域拆成若干方法，例如 `registerServerRoutes(private chi.Router)`、`registerWorkspaceRoutes`、`registerCodexRoutes`、`registerGeminiRoutes`、`registerClaudeRoutes`、`registerDeploymentRoutes`、`registerSettingsRoutes`。集中放在一个新文件 `routes.go` 里，`api.go` 只保留对它们的调用。
3. `private.Use(api.authenticate, api.requireCSRF)` 保持在所有私有路由注册之前。公开路由（健康检查、初始化、登录、Agent 注册）和私有路由的归属不变。

各领域的路由数量（按路径第一段统计），用来核对没有遗漏：

| 第一段 | 数量 | 第一段 | 数量 |
| --- | --- | --- | --- |
| `/workspaces` | 51 | `/deployment-targets` | 10 |
| `/threads` | 20 | `/settings` | 9 |
| `/servers` | 19 | `/scheduled-tasks` | 4 |
| `/gemini` | 17 | `/sessions`、`/secret-sets`、`/operations`、`/deployments`、`/credential-profiles`、`/auth` | 各 3 |
| `/projects` | 15 | `/setup`、`/approvals`、`/alerts`、`/agent` | 各 2 |
| `/claude` | 15 | `/ws`、`/summary`、`/health`、`/audit`、`/ai-clis` | 各 1 |

**验收**

- 路由表快照测试在拆分前后都通过，快照文件在拆分提交里没有变化。
- `api.go` 里不再有逐条的路由注册语句。

**说明**

这份快照测试之后长期保留。P2-05 新增 `/api/sessions/...` 路由时，快照的变化会如实出现在提交里，方便审阅。

### P3-02 拆分 `httpapi/resources.go`

纯移动。

**做法**

1. 运行 `pure_move_sum internal/httpapi`，记下结果。
2. 按“后端目标布局”把 `resources.go` 的内容搬到各领域文件。一次搬一个领域，每搬完一个就编译一次。
3. 测试文件 `resources_test.go` 本步骤不拆。它很大，但拆它没有紧迫性，可以在以后改到相关测试时顺带处理。
4. 再次运行 `pure_move_sum internal/httpapi`。

**验收**

- 两次校验和相同。
- `resources.go` 已删除；`internal/httpapi` 下非测试文件都不超过 1000 行。
- P3-01 的路由表快照测试通过。

**风险与回滚**

单个提交，可整体回滚。主要风险是与同时进行的功能改动冲突，所以动手前确认没有别的会话正在改 `internal/httpapi`，并尽快推送。

### P3-03 拆分 `store/store.go` 与 `store/resources.go`

纯移动。前置是 P1-02，因为那一步会把启动迁移的调用链从 `OpenWithOptions` 搬进迁移运行器，先后顺序反了会互相冲突。

**做法**

1. 运行 `pure_move_sum internal/store`，记下结果。
2. 按“后端目标布局”搬移。`migrate*` 函数只搬位置，不改名、不改内容、不改调用顺序；它们已经属于 P1 的基线迁移，内容被冻结。
3. `store.go` 最后只剩类型定义、打开连接、`Q`、`NewID` 和少量公共辅助函数。
4. 再次运行校验和。

**验收**

- 两次校验和相同。
- `resources.go` 已删除；`store.go` 不超过 300 行。
- `make test-postgres` 通过，P1-01 的结构一致性测试通过。

### P3-04 拆分 `agentgateway/gateway.go` 与 `agent/client.go`

分成三个提交。前两个是纯移动，第三个是机械提取。前置是 P0-07：这两个文件正是跨进程验收测试覆盖的链路，三个提交之后那组测试都应当不需要修改就能通过。

**提交一：网关，纯移动**

按“后端目标布局”把 `gateway.go` 里的整函数搬到各文件，用 `pure_move_sum internal/agentgateway` 校验。

**提交二：Agent 客户端，纯移动**

同样处理 `client.go`，用 `pure_move_sum internal/agent` 校验。这个提交改了 Agent 依赖的包，按总览的改动纪律在同一提交里升级 `buildinfo.Version`。

**提交三：把三个巨型函数的分支提取成具名函数**

1. 对象是 `Gateway.handleOperationResult`、`Client.handleOperation` 和 `Client.execute`。
2. 每个分支的函数体原样提取成一个方法，例如 `completeWorkspaceFiles`、`executeClaudeTurn`。原函数保留为分发骨架：判断的顺序不变，原来会提前返回的分支仍然返回，原来不返回、继续往下走的分支仍然继续。
3. 分支之间共享的局部变量（例如 `handleOperationResult` 开头算出的 `geminiTurn`、`geminiInterrupted`）作为参数传入，不要改成结构体字段。
4. 提取之前先确认每个分支都有测试覆盖；没有的先补特性测试并单独提交。
5. 不在这一步把 `switch` 改成注册表，也不引入操作类型常量。那是 P4-01 和 P4-04 的工作。

**验收**

- 前两个提交各自的校验和前后相同。
- 第三个提交之后，`internal/agentgateway` 和 `internal/agent` 的全部测试通过，其中包含 `agentgateway/session_concurrency_test.go`。
- 两个包里没有超过 1000 行的非测试文件；被提取的三个函数各自不超过 150 行。

**风险与回滚**

`handleOperationResult` 里各段判断不是互斥的：同一个操作结果可能先后命中多段，例如 `git.worktree.create` 在 `gateway.go` 第 740、782、800 行各被判断一次。提取时最容易出错的就是把“继续往下走”改成了“返回”。逐段对照，三个提交各自可回滚。

### P3-05 `App.tsx` 拆分一：公共组件

**做法**

1. 把不依赖应用状态的小组件和函数搬出 `App.tsx`：`Field`、`ErrorBanner`、`LanguageSwitch`、`LoadingScreen`、带翻译的 `Dialog` 包装、懒加载 Markdown 渲染器的 `MarkdownContent` 包装、`message`、`pretty`、`copyText`、`FilePreviewPane`、`EditableFilePreviewPane`、`FileDiffPane`、`PreviewStatusNote`。目标位置见“前端目标布局”。
   当前依赖核对补充（2026-11-15）：预览面板直接使用 `useWorkspacePreview`，该钩子及其私有类型、轮询常量提前随面板移到 `workspace/useWorkspacePreview.ts`；`previewLanguage`、`formatFileSize` 随面板移动，避免公共组件再从 `App` 导入。
2. 导出名保持不变，调用方只改导入路径。`components/Dialog.tsx` 里已经有一个同名的 `Dialog`，`App.tsx` 里的是给它补上翻译后的关闭按钮文案的包装；两者继续并存，不要合并。
3. `PageProps` 接口被六个页面使用，搬到 `types.ts`。`FilePreviewSelection` 类型本来就定义在 `codexContent.ts`，`App.tsx` 只是转手导出；调用方改为直接从 `codexContent` 导入。
4. 更新六个页面和组件文件以及相关测试文件的导入。

**验收**

- `pages/` 和 `components/` 下不再有文件从 `App` 导入上面这些符号。用 `grep -rn 'from "\.\./App"' web/src/pages web/src/components` 核对剩余的导入，把结果贴到“执行记录”。
- 构建通过 `check:bundle`：单个 JavaScript 块不超过 500 kB，入口不预加载 `vendor-markdown`。把入口块的体积记到“执行记录”，供后两步对比。

### P3-06 `App.tsx` 拆分二：实时通道与路由

这一步不是纯移动：把写在 `App` 组件体里的 WebSocket 逻辑提取成钩子，需要重新划定它和外壳之间的接口。

**做法**

1. 先确认测试覆盖。`RealtimeRefresh.test.tsx`、`AppStartup.test.tsx`、`AppSession.test.tsx` 已经覆盖一部分；对照下面的行为清单逐项检查，缺的先补测试并单独提交：
   - 建立连接、断线后按带随机抖动的指数退避重连（`websocketReconnectDelay`）。
   - 收到事件后只增加相关刷新范围的修订号（`realtimeScopesForEvent`），并让对应线程的事件缓存失效（`invalidateThreadEvents`）。
   - 登录态失效时关闭连接并回到登录页。
   - 浏览器前进、后退（`popstate`）时，视图与地址栏保持同步。
2. 提取 `realtime/scopes.ts` 和 `realtime/useRealtime.ts`。钩子对外提供修订号、连接状态和事件订阅。Codex 的线程事件缓存（`invalidateThreadEvents` 等）通过订阅接入，实时通道本身不引用任何引擎的代码。
3. 提取 `navigation.ts`。地址栏格式 `/?view=<视图>&thread=<ID>` 保持不变，协作结果和通知里已经持久化了这种链接。
4. `claudeManagement.tsx` 和 `geminiManagement.tsx` 对 `locationFor` 的导入改指向 `navigation.ts`。

**验收**

- 第 1 条里的行为清单每一项都有对应的测试并通过。
- Playwright 桌面和移动端用例通过。
- `App.tsx` 里不再出现 `WebSocket` 和 `window.location` 的直接使用。

**风险与回滚**

实时通道出问题时的表现是“界面不刷新”，测试不一定能发现。发布后在真实环境里做一次人工确认：发起一轮会话、触发一次 Git 操作、让 Agent 断线重连，三种情况下界面都自动更新。做不了时在“执行记录”里注明。

### P3-07 `App.tsx` 拆分三：Codex 会话与工作区文件面板

以移动为主。

**做法**

1. 把工作区文件面板搬到 `web/src/workspace/`：`WorkspaceFilesPanel`、`ChangedFilesView`、`FileTreeItem`、`buildFileTree`、`useWorkspacePreview`、`FilePreviewSelection` 类型。`ClaudePage.tsx` 和 `GeminiPage.tsx` 的导入随之更新。
2. 把 Codex 会话的实现搬到 `web/src/codex/`，分组见“前端目标布局”。`pages/CodexPage.tsx` 的多行导入改指向新位置。
   当前依赖核对补充（2026-11-15）：预览钩子已在 P3-05 搬走；供现有测试使用的 `ServersPage` / `CodexPage` 懒加载包装及对应路由声明搬到 `pages/lazyRoutes.tsx`，`CodexPageProps` 放进 `types.ts`；滚动位置与滚动辅助函数放入 `codex/scroll.ts`，避免缓存与会话组件互相依赖。
3. `clearCodexSessionMemory` 随线程事件缓存一起搬走。外壳在退出登录时会调用它；P3-06 的通用实时钩子通过订阅接入缓存失效，所以 `App.tsx` 保留对同一缓存模块的一处导入（`clearCodexSessionMemory`、`invalidateThreadEvents`）。这是外壳对 Codex 代码仅剩的模块依赖，P2-06 再把清理与订阅都换成引擎无关的入口。
4. `App.tsx` 第 632 行的 `useAuditList` 在基线提交里没有任何调用方，`pages/SettingsPage.tsx` 第 78 行有一份同名实现在用。动手时再确认一次，确认后删除 `App.tsx` 里的这一份，单独提交，不算在移动的提交里。
5. `App.tsx` 里剩下的应当只有：`App` 组件、懒加载路由的声明、布局和导航。

**验收**

- `App.tsx` 不超过 400 行。
- `grep -rn 'from "\.\./App"\|from "\./App"' web/src` 的结果只剩 `main.tsx` 和以 `App` 整体为测试对象的测试文件。
- 构建通过 `check:bundle`。Codex 会话的代码此前在入口块里，搬走后应当进入 Codex 页面的懒加载块；把入口块体积的前后对比记到“执行记录”。
- Vitest、类型检查、Playwright 通过。

**说明**

`SessionView` 内部约 460 行，本步骤只搬不拆。它的内部结构由 P2-06 迁移 Codex 时重新组织。

### P3-08 前端数据层收敛

2026-11-15 执行基线 `2c9877b`：P2-06 已完成，三个引擎模块位于 `sessions/engines/`，分页列表重用现有 `useData`。本步迁移数据层时保留根目录兼容导出，使尚未迁移的会话/组件也共用请求合并与缓存；原生事件窗口仍留在引擎模块。按监控、操作、仪表盘、服务器、项目、部署、设置分批提交。先为并发合并/独立取消/写入失效增加数据层测试，再迁移页面。刷新范围通过应用级数据上下文读取，不再由页面转交实时修订号；写入端点声明失效范围。既有审计 offset 分页不改变后端协议。

**做法**

第一部分，在现有 `useData` 的基础上收敛：

1. 盘点 55 个 `useData` 调用点和各页面直接发起的请求，列成表：路径、传入的 `dependency`、是否启用缓存、写操作之后刷新了哪些数据。表格放进本文件的“执行记录”。
2. 建立 `web/src/data/`。按领域提供带类型的端点函数，返回路径和响应类型，页面不再手拼路径字符串。`api.ts` 搬入这个目录。
3. 让取数点自己声明依赖的刷新范围，由数据层从实时通道读取修订号。页面不再需要把 `realtime` 修订号作为属性一层层传下去。
4. 提供统一的写操作辅助函数：声明这次写入会使哪些范围失效，成功后由数据层触发刷新。替换各页面里手工串联的 `reload`。
5. 缓存策略由端点定义决定，不再由调用点的布尔参数决定。同一路径的并发请求合并为一次。

第二部分，决策 D5：

6. 第一部分完成一半左右（例如迁移完监控、操作、服务器三个页面）时评估是否改用 TanStack Query。判断依据写下来：分页与无限列表是否仍然需要手写（`useThreadList`、`useThreadEvents`、`useAuditList`）；是否需要乐观更新和失败重试；引入后入口块增加多少体积。结论记到总览的“决策记录”。
7. 如果决定引入，端点函数和刷新范围的定义保持不变，只替换底层实现。

迁移顺序：监控、操作、仪表盘，然后是服务器、项目、部署、设置。三个会话页面不在本步骤迁移，它们在 P2-06 重写时直接使用新的数据层。

**验收**

- 除三个会话页面外，页面文件里不再出现手拼的 `/api` 路径字符串和对 `post`、`patch`、`put`、`remove` 的直接调用。
- `useData.test.tsx` 的用例全部保留或有等价的新用例。
- Vitest、类型检查、Playwright 通过；`check:bundle` 通过。

**风险与回滚**

按页面逐个迁移，每个页面一个提交。新旧两种取数方式在过渡期并存，共用同一份缓存和修订号。

### P3-09 轮询收敛

2026-11-15 执行基线 `7551c00`：会话模块已移入 `sessions/engines`，Claude/Gemini共用 `sessions/useSessionRevision`，文件面板位于 `workspace/WorkspaceFilesPanel`。数据层已统一通用读取；通过应用DataProvider为这些活跃读取和原生事件窗口注册一个可见期间的断线兜底，重连立即刷新。按定时器逐份提交；先提交实时更新的特性测试。文件扫描过期由 `ExpireWorkspaceFileScans` 在控制面维护时写库，目前没有配套实时事件，需要保留仅扫描中的公共可见轮询以获取超时，不能单凭正常扫描结果事件删除这一路径。操作结果、变更/Git结果与协作更新则已有实时发布。前端版本/Service Worker定时器不动。

**现有定时器清单**

| 位置 | 间隔 | 条件 | 作用 |
| --- | --- | --- | --- |
| `pages/ClaudePage.tsx:72` | 5 秒 | 页面可见 | 无条件刷新会话列表和当前会话 |
| `pages/GeminiPage.tsx:64` | 5 秒 | 页面可见 | 同上 |
| `pages/GeminiPage.tsx:269` | 15 秒 | 任务运行中 | 更新“当前时间”，用于显示距上次活动多久 |
| `pages/OperationsPage.tsx:56` | 3 秒 | 存在进行中的操作 | 刷新操作列表 |
| `components/SessionUsage.tsx:35` | 5 秒 | 任务运行中且页面可见 | 刷新用量 |
| `components/SessionCollaboration.tsx:42` | 3 秒 | 已授权、有关联会话或面板展开，且页面可见 | 刷新协作状态 |
| `App.tsx:756` | 5 秒 | 文件扫描进行中 | 刷新文件快照 |
| `App.tsx:763` | 2 秒 | 变更扫描或 Git 刷新进行中，且页面可见 | 刷新变更和 Git 状态。代码注释写明原因：实时消息可能延迟或断开 |

另有三个带退避的等待循环：`waitForGoalSnapshot`、`waitForThread`、`waitForWorkspace`。它们等的是一次具体操作的结果，属于合理用法，本步骤只把它们统一到一个公共的等待函数上。

`main.tsx` 的两处定时器（Service Worker 更新、前端版本检查）保持不变。

**做法**

1. 对清单里的每一项，先查清它在弥补什么。分三种情况：
   - 数据变化时控制面已经发布了实时事件，轮询只是保险。
   - 数据变化时控制面没有发布事件。
   - 只是需要一个时钟。
2. 第一种：在数据层实现唯一的断线兜底。实时通道断开期间，对当前活跃的取数点按固定间隔刷新；连接恢复后停止，并立即刷新一次。然后删除对应的轮询。
3. 第二种：优先在控制面补发事件。补不了的保留轮询，但改用公共钩子，并在调用处写明原因。
4. 第三种：改用一个公共的时钟钩子，页面不可见时暂停。
5. 一次只处理一项，每项一个提交。删除轮询之前，先有测试证明实时路径能让界面更新。

**验收**

- `grep -rn "setInterval" web/src --include=*.ts --include=*.tsx | grep -v "\.test\."` 的结果只剩 `main.tsx`、数据层的断线兜底和公共钩子。
- 有测试模拟实时通道断开，验证兜底刷新生效、恢复后停止。`WorkspacePreviewPolling.test.tsx` 和 `RealtimeRefresh.test.tsx` 是起点。
- 清单里每一项的调查结论和处理方式都记在“执行记录”里。

**风险与回滚**

删错轮询的后果是界面停在旧状态，直到用户手动刷新。Claude 和 Gemini 页面的 5 秒无条件刷新掩盖了多少实时事件的缺失，目前不清楚，这两项放在最后处理，并在发布后观察一个版本周期。

### P3-10 拆分 `styles.css` 与 `i18n.tsx`

两部分都是纯移动，各一个提交。这两个文件几乎每个功能提交都会改，动手前确认没有别的会话正在改界面，并尽快推送。

**做法：样式**

1. 记下拆分前构建产物里 CSS 文件的内容校验和。
2. 建立 `web/src/styles/`，按文件里现有的分节把 `styles.css` 切成若干文件，例如基础与布局、通用组件、会话、工作区文件、监控、部署、浅色主题。用一个 `index.css` 按原顺序引入，`main.tsx` 改为导入它。
3. 层叠顺序必须保持：把各文件按引入顺序拼接起来，结果要与原文件逐字节相同。

**做法：文案**

1. 建立 `web/src/i18n/`。`en` 和 `zh` 各自按键名的领域前缀切成若干文件（`codex`、`server`、`project`、`gemini`、`claude`、`settings`、`deployment`、其余合并为 `common`），再合并成完整的字典。Provider 和 `useI18n` 留在 `i18n/index.tsx`，对外的导入路径 `./i18n` 保持可用。
2. 类型约束不能变松。现在 `zh` 的类型是 `Record<MessageKey, string>`，缺键和多键都会在类型检查时报错。拆分后每个中文分片的类型写成对应英文分片键集合的 `Record`，保持同样的严格程度。
3. 用展开语法合并分片时，重复的键会被静默覆盖。增加一个测试：各分片键数之和等于合并后的键数，两种语言的键集合完全相同。

**验收**

- 样式：拼接结果与原文件逐字节相同；构建产物里 CSS 的内容校验和与拆分前相同。
- 文案：新测试通过；合并后的 `en` 和 `zh` 与拆分前逐键逐值相同，可以用一次性脚本导出两份 JSON 比较，脚本不提交。
- Vitest、类型检查、Playwright 通过；`check:bundle` 通过。

**说明**

按语言懒加载字典可以减小入口体积，但会引入首屏的加载状态，不属于纯移动。如果要做，另立步骤。

## 阶段完成标准

- 进度总表里 P3 的步骤全部 `已完成` 或 `已取消`。
- “目标”一节的四条都可以用命令核对，核对结果写进“执行记录”。
- 重新统计总览“基线数字”里的文件行数，把前后对比写进“执行记录”。

## 执行记录

按时间顺序追加，每条包含日期、步骤编号、会话标识、做了什么、验收结果、遗留事项。

### 2026-11-15 · M2 / P3-01 · Codex

- 从 `f8a847a` 的干净工作区继续，当前 Agent 版本 0.3.16。原布局以 `0247c1c` 为基线；M1 已提取迁移运行器、引入操作常量，实际移动边界以当前声明为准，各纯移动步骤重新记录校验和。
- P3-01 先提交方法、路径和中间件数量快照，再拆路由；公开与私有路由边界及中间件顺序保持不变。
- M2 自动本地提交授权见总览，不涉及推送与部署。

- 基线快照：`chi.Walk` 实测 191 条路由（公开 3 层、私有 5 层中间件），`go test ./internal/httpapi -run '^TestRouteSnapshot$' -count=1` 通过；测试及快照先独立提交。

- 当前路由按 18 个私有领域及公开入口拆入 `routes.go`，`api.go` 只调用注册入口；快照文件保持不变。
- 验收：前端构建（含 bundle 检查）、`go vet ./...`、`go test ./...`、全部 449 个 Vitest 用例、类型检查、Go 构建、格式和 `git diff --check` 全部通过。注册语句的方法、路径、处理器、公开/私有归属逐条比对相同；快照与基线提交 `ec31a30` 相同。P3-01 完成；本地提交号在下一步骤领取时回填。

### 2026-11-15 · P3-02 · Codex

- 按当前声明边界拆分 `resources.go`；共享限制常量和 Git URL 辅助函数归入 `api.go`，线程/审计响应结构归各领域。`forkThreadToWorktree` 归线程领域。现有文件与建议目标无重名冲突；保留测试文件，每搬一个领域编译一次。

- 拆分前后 `internal/httpapi` 校验和均为 `5cd3161aff43d14948f0f2308804208f8fcd1d7ddd3b9f48dc32b31833f04ba5`。14 个目标文件逐个移动、逐个编译通过，`resources.go` 删除；所有非测试文件均不足 1000 行。
- 验收：前端构建（含 bundle）、`go vet ./...`、`go test ./...`（含路由快照）、449 个 Vitest 用例、类型检查、Go 构建、格式与差异空白检查全部通过。P3-02 完成。

### 2026-11-15 · P3-03 · Codex

- M1 已将启动编排放入 `migrate.go` / `migrate_baseline.go`；本步保持原调用链与被冻结的迁移内容，仅将 `store.go` 的旧迁移及其专属路径辅助函数搬入 `migrations_legacy.go`。
- 补充当前布局：`Setting` / `SetSetting` 归 `settings.go`；操作指标聚合与指标读取归 `metrics.go`；已有 `codex_snapshots.go` 接收快照声明。`store.go` 保留连接、公共 ID / Token 辅助函数及类型。
- 校验差异来自 `gofmt` 对三个相邻单行辅助函数的对齐；恢复相邻布局后，拆分前后校验和均为 `b586c2471fe4b01d6b2a496356258beb6a0d28ef76dd966735d47f68fd2ed712`。`resources.go` 删除，`store.go` 为 103 行；迁移内容与顺序未变。
- 验收：前端构建（含 bundle）、`go vet ./...`、`go test ./...`（含 P1 结构一致性）、`make test-postgres`、449 个 Vitest 用例、类型检查、Go 构建、格式与空白检查全部通过。所有 Store 非测试文件均不足 1000 行；P3-03 完成。

### 2026-11-15 · P3-04 · Codex

- 当前版本已使用 P4-01 操作常量，沿用它们，保持持久化值不变。网关新增的 `noActiveCodexTurn` / `isCodexTurnOperation` 随 Codex 函数移动；Agent 的运行期去重类型随操作调度移动，命令探测辅助函数随遥测移动。
- 按三份提交执行；移动提交先分别校验包内代码，机械提取前检查两个包的分支覆盖，缺失先补特性测试。跨进程测试保持原样。

- 网关纯移动：前后校验和均为 `3775a855abfcc96fa85892a532cad4654b71687da15e5483f10af494e31b1159`；`gateway.go` 为 459 行，最大文件 `operation_results.go` 为 619 行。
- 网关纯移动验收：通用五项、Go 构建、格式与空白检查全部通过，449 个前端用例通过；先提交这一份可独立回滚的移动。
- Agent 纯移动：前后校验和均为 `bf194bd15b146d905cffb7142f062ab9d0205c5ce18e87471ef1651fb0799174`。`client.go` 为 299 行，最大文件 `operations.go` 为 725 行；Agent 版本升级至 `0.3.17`。
- Agent 纯移动验收：前端构建（含 bundle）、`go vet ./...`、`go test ./...`、449 个 Vitest 用例、类型检查、Go 构建、格式与空白检查全部通过；将这一份移动独立提交后再检查分支覆盖。
- 提取前覆盖核对：两个包原有全部测试通过；新增 5 个特性测试、60 个分项，覆盖损坏命令拒绝与重复投递回执一致性、清单发布、无 ID 命令拒绝、损坏资源结果不完成操作。新增测试单独执行通过，补充覆盖与原有覆盖合并后，所有准备提取的操作分支均有入口覆盖；未提取的通用错误/竞态护栏保持原位。
- 新测试先独立提交；Agent 只有测试文件变化，没有修改编译进 Agent 的代码，因此本份测试提交不升级版本号。
- 机械提取：Agent 的 46 个分支、网关的 18 个资源分支与提取前的 Go token 序列逐一比对一致（仅扣除参数引用所需的解引用、局部 `err` 声明与收尾返回）。两个 worktree 源会话释放分支仍按原顺序运行，通用完成、重复回执和取消判断留在原函数。
- 三个目标函数当前行数：`Gateway.handleOperationResult` 115、`Client.handleOperation` 117、`Client.execute` 46。两包非测试文件最大分别为 557 / 443 行；Agent 版本升级至 `0.3.18`。
- 最终验收：通用五项、Go 构建、格式、空白、`make staticcheck`、`make version-check` 全部通过；未经修改的会话并发及跨进程测试通过；真实浏览器完成初始化、登录、内置 Agent 与三引擎回复，1 个端到端用例通过。P3-04 完成。

### 2026-11-15 · P3-05 · Codex

- 公共组件、文本函数、预览面板及其钩子按修订后的计划移动，保留翻译 Dialog 包装和原有底层 Dialog；页面属性归 `types.ts`，预览选择类型由调用方直接导入原定义。
- 拆分前入口块为 321.86 kB，未预加载 `vendor-markdown`；以本次拆分前的声明逐段比对，仅允许 import / export 调整。
- 声明内容比对：123 个原有顶层声明均保持一致（忽略 export 和相对 import 的调整），并审阅移动差异。入口块现为 308,875 字节（308.88 kB），bundle 检查通过且不预加载 Markdown。
- `pages/` / `components/` 剩余从 `App` 导入的文件：`ClaudePage.tsx`、`GeminiPage.tsx` 只取 `WorkspaceFilesPanel`；`claudeManagement.tsx`、`geminiManagement.tsx` 只取 `locationFor`；`CodexPage.tsx` 取会话、线程列表、缓存、等待、分组和文件面板等后续拆分符号。公共组件、文本函数、`PageProps` 与预览选择类型均已改指向新模块或原定义。
- 验收：通用五项、Go 构建、格式与空白检查全部通过；Vitest 449 / 449；桌面和移动端 Playwright 33 通过、11 按现有配置跳过、0 失败。P3-05 完成。

### 2026-11-15 · P3-06 · Codex

- 既有 `AppSession` 覆盖连接、抖动退避、稳定后重置、失效退出及切换线程期间的缓存失效；补充刷新范围修订号的集成断言和浏览器前进/后退的视图、线程同步测试，先锁定当前行为再提取。
- 通用实时钩子维护修订号和连接状态、提供流失效订阅；外壳通过订阅接入 Codex 缓存，钩子不导入引擎模块。地址栏写入也归导航模块，保留原 URL 及 history 行为。
- 新旧特性测试在提取前通过：`RealtimeChannel`、`RealtimeRefresh`、`AppSession`、`AppStartup` 共 13 / 13；类型检查通过。先独立提交测试基线。

- 提取后除 `App` 的计划内钩子接线变化之外，98 个既有顶层声明保持原样。`App.tsx` 已无 `WebSocket` 和 `window.location` 直接使用；实时钩子不导入 `App` 或 Codex 模块，缓存失效订阅保留在修订号更新之前。
- 验收：通用五项、Go 构建、格式与空白检查全部通过；Vitest 451 / 451（含重连退避、过期退出、缓存失效、范围刷新与前进/后退）；桌面和移动端 Playwright 33 通过、11 既有跳过、0 失败。P3-06 完成。
- 本轮未发布或部署，未在已部署环境执行“发起会话、Git 操作、Agent 重连”的人工实时刷新回归；保留为发布后的人工检查记录，不代称自动化验收。

### 2026-11-15 · P3-07 · Codex

- 引用搜索再次确认：`App` 中的 `useAuditList` 只有定义，连同仅被它使用的 `AuditListPage`、`auditListPageSize`、`mergeAuditEntries` 一起删除；`SettingsPage` 的实际审计实现继续使用。按计划先单独提交删除，再移动会话与文件面板。
- 删除校验：类型检查通过，保留的 80 个声明内容比对一致；只有孤立审计声明及 import 调整发生变化。拆会话前入口块为 309,178 字节（309.18 kB）。

- 移动校验：删除孤立代码后的 80 个原有声明全部保持原样，仅改 import / export；工作区文件树、预览钩子、Codex 线程缓存、滚动状态、会话、事件、审批、等待函数各归独立模块，懒加载包装归 `pages/lazyRoutes.tsx`。
- `App.tsx` 现为 167 行；全库从 `App` 导入的结果只剩 `main.tsx` 与整体测试 `AppStartup.test.tsx`、`AppSession.test.tsx`、`RealtimeChannel.test.tsx`。页面与组件反向依赖已清除。
- 入口块从 309,178 字节降为 210,853 字节（减少 31.8%；相对 M2 开始时 321.86 kB 约减少 34.5%）。构建源映射确认 `SessionView.tsx` 只在 Codex 页面的懒加载块中，不在入口；所有 JS 块低于 500 kB，入口无 Markdown 预加载。
- 最终验收：前端构建（含 bundle）、`go vet ./...`、`go test ./...`、Vitest 451 / 451、类型检查、Go 构建、格式与空白检查全部通过；桌面和移动端 Playwright 33 通过、11 既有跳过、0 失败；真实浏览器的初始化、登录、内置 Agent 与三引擎回复复验通过。P3-07 完成。

### 2026-11-15 · M2 完成核对 · Codex

M2 的 P3-01 至 P3-07 全部完成，各步骤及必要测试基线均已本地提交。P3-08 / P3-09 属于后续 M4，P3-10 未领取；本轮没有推送、发布或部署。

| 文件 | 总览原始基线 | 当前 |
| --- | --- | --- |
| `internal/httpapi/api.go` | 851 | 732 |
| `internal/httpapi/resources.go` | 3258 | 已删除，按领域拆分 |
| `internal/store/store.go` | 2641 | 103 |
| `internal/store/resources.go` | 1384 | 已删除，按领域拆分 |
| `internal/agentgateway/gateway.go` | 1516 | 459 |
| `internal/agent/client.go` | 1428 | 299 |
| `web/src/App.tsx` | 2133 | 167 |

- 四个目标 Go 包的最大非测试文件行数：HTTP API 732、Store 987、网关 557、Agent 443，均不超过 1000；Store 入口不超过 300、App 不超过 400、三个目标分发函数均不超过 150。
- Go 纯移动前后校验和全部一致；路由快照保持 191 条及公开/私有中间件边界；Agent 当前版本 `0.3.18`。
- 数据库专项验收已在 P3-03 通过（含 PostgreSQL 及结构一致性），静态检查与版本护栏已在 P3-04 通过；此后只改前端及文档。最终通用验收与两套浏览器测试结果见 P3-07。
- 无未提交交接事项；已部署环境的人工实时刷新回归仍见 P3-06 的记录。
- 最终文档回填：七个 M2 步骤的完成状态、前置依赖和本地提交引用核对通过；Markdown 本地链接与 `git diff --check` 通过。此份回填只有文档变化，不重复运行已通过的构建与测试。

- 2026-11-15，P3-08 数据层开工，Codex：按基线 `2c9877b` 用 TypeScript AST 盘点51个现存 `useData` 调用及57个页面直接请求，完整路径表达式、依赖、缓存及显式刷新见 [数据盘点](P3-data-inventory.md)。与原计划55处不同，已先修订执行基线。`api`/`useData` 移入 `data/` 并保留兼容导出；加入按路径合并且按消费者租约取消的读取，以及领域查询、实时修订上下文、成功写入失效、账号缓存清理。既有5项读取测试保留，新增2项并发/取消测试与3项范围/写入/缓存测试；会话游标4项测试继续保留。页面按领域迁移，D5在服务器页迁移后评估，当前未改变轮询策略。
- 2026-11-15，P3-08 监控批次：监控四个读取点改用类型端点与刷新范围；确认告警成功后由端点使监控/仪表盘失效。并发数据基础14项通过，监控2项及操作/实时相关9项定向验证通过。基础提交 `db184d4`。
- 2026-11-15，P3-08 操作批次：操作列表/筛选及中断使用领域端点，页面不再接收实时属性；成功中断自动失效，失败时保留原错误和状态重读，3秒运行中兜底暂时保留到P3-09。原4项页面用例通过。
- 2026-11-15，P3-08 仪表盘批次：汇总查询改为端点自身声明dashboard范围和1秒节流；保留原节流/实时批处理/无关范围隔离场景，实时6项验证通过。页面改从应用数据上下文订阅。

- 2026-11-15，P3-08 服务器批次：服务器/凭据读取和SSH探测、流式安装/修复、卸载、撤销、元信息、Agent更新改为领域端点；流式写入只有收到完整成功结果且无错误事件才失效。8项服务器及凭据/实时共28项通过，类型检查通过。
- D5中段评估：现已迁移监控、操作、仪表盘、服务器；通用并发去重/消费者取消/范围失效已有14项基础验证，未出现需引入查询库解决的问题。会话游标窗口和原生事件回卷、阅读位置、LRU仍需领域逻辑；审计沿用原offset协议，无乐观写入和自动重试要求。决定继续现有实现，不增加依赖，最终构建以原500kB护栏验收。

- 2026-11-15，P3-08 项目批次：项目筛选、工作区、隐藏项目、详情、Git读取及项目/工作区/Git操作统一使用类型端点，成功写入由projects范围失效；保留原路径编码、载荷和确认流程。项目与子对话框22项测试、类型检查通过；Git操作联合类型移入数据层并保留原导出。

- 2026-11-15，P3-08 部署批次：部署目标/历史/日志/配置审阅读取，以及创建、编辑、部署、回滚、容器操作和删除统一走领域端点；服务器、工作区和密钥复用公共查询。5项部署交互测试、类型检查通过，确认与健康检查载荷保持原样。项目批次提交 `570f8e0`。

- 2026-11-15，P3-08 设置批次：三引擎CLI版本、凭据、密钥、定时任务和原生会话选择器改走类型端点，版本子组件从数据上下文取范围；审计分页钩子移入data并保持offset与已加载尾页重读。定时任务对话框保留原载荷/默认值和回调，设置页自身依靠成功写入失效刷新。设置4项交互测试及类型检查通过；部署批次提交 `f4ee20d`。清理先前已迁移页面遗留的直接API导入，全量验收随后执行。

- 2026-11-15，P3-08 最终验收：64文件464项Vitest、前端构建/类型检查/bundle、桌面与移动Playwright 33通过11既有跳过、Go vet及全量Go测试全部通过。非会话页面无直接API请求和手拼资源路径；D5不引入新依赖。P3-08完成，继续P3-09。

- 2026-11-15，P3-09 基础：删除定时器前补6项实时界面特性测试并先提交 `6d70a98`。DataProvider加入唯一断线兜底，通用读取和Codex原生事件窗口登记活跃回调；5秒间隔、隐藏暂停/回到前台立即刷新、重连立即刷新并停表、卸载和空路径取消注册。新增2项断线/可见性/重连/清理测试，与既有读取及实时用例共15项通过，类型检查通过。公共可见间隔与时钟钩子供后续逐项迁移。

- 2026-11-15，P3-09 操作列表：`operation.*`由网关在结果持久化后发布，未知事件安全映射包含operations；新增界面测试已锁定实时修订到结果显示。删除页面3秒运行中轮询，断线由活跃查询公共兜底接管。
