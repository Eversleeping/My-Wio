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
3. `clearCodexSessionMemory` 随线程事件缓存一起搬走。外壳在退出登录时会调用它，所以 `App.tsx` 保留对它的一处导入；这是外壳对引擎代码仅剩的直接依赖，P2-06 再把它换成与引擎无关的清理入口。
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

（暂无）
