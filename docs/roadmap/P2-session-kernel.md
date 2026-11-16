# P2 统一会话内核

返回[路线图总览](README.md)。状态以总览的“进度总表”为准，本文件只写步骤细节和执行记录。

这是原有 P0 至 P6 中投入最大的阶段之一。目标是让“会话”成为一个与引擎无关的概念：三种引擎共用一套模型、一套控制面接口、一套 HTTP 路由和一个前端外壳，引擎差异收敛到各自的驱动实现和能力声明里。

2026-11-14 用户确认新增 [P7 任务编排与团队协作](P7-task-orchestration.md)（D10、D11）。P2 提供它需要的统一会话引用、读模型和驱动入口；任务、计划依赖、成果版本、审查和动态协作在 P7 实现。P2 保持现有行为，不能借此提前加入任务状态或扩大模型授权。

## 现状

### 每一层都有三份

| 层 | Codex | Gemini | Claude |
| --- | --- | --- | --- |
| 线程表 | `codex_threads` | `gemini_threads` | `claude_threads`，另有 `claude_thread_options`、`claude_thread_management` |
| 定时任务表 | `codex_scheduled_tasks` | `gemini_scheduled_tasks` | `claude_scheduled_tasks` |
| 服务器 CLI 状态 | `servers.codex_version`、`codex_ready` | `servers.gemini_version`、`gemini_ready` | `server_claude_cli` |
| 服务器凭据绑定 | `server_credential_profiles`、`server_credential_updates` | `server_gemini_credential_*` | `server_claude_credential_*` |
| Store 结构体 | `store.Thread`（`store/resources.go`） | `store.GeminiThread`（`store/gemini.go`） | `store.ClaudeThread`（`store/claude.go`） |
| HTTP 路由前缀 | `/api/threads` | `/api/gemini/threads` | `/api/claude/threads` |
| HTTP 处理器 | `httpapi/resources.go` 里的 `threads`、`handleTurn` 等，另有 `codex_native.go` | `httpapi/gemini*.go` 共 8 个文件 | `httpapi/claude*.go` 共 8 个文件 |
| 网关事件入口 | 信封类型 `event` | `gemini_event` | `claude_event` |
| Agent 适配器 | `internal/codexadapter` | `internal/geminiadapter` | `internal/claudeadapter` |
| 前端页面 | `pages/CodexPage.tsx`，主体 `SessionView` 在 `App.tsx` | `pages/GeminiPage.tsx` | `pages/ClaudePage.tsx` |
| 前端类型 | `Thread` | `GeminiThread` | `ClaudeThread` |

三张定时任务表的结构完全相同。`store/scheduled_tasks.go` 已经在用 `strings.ReplaceAll` 把 Codex 的查询语句改写成另外两种引擎的版本。

### 已经存在的统一雏形

这些是可以直接生长的起点，不需要从零设计：

- `sessiontools.Ref{Engine, ID}`：引擎无关的会话标识，协作功能全程使用。
- `store/session_collaboration.go` 的 `collaborationSessions` 查询：用 `UNION` 把三张线程表拼成统一视图，字段有 `engine`、`native_id`、`model`、`mode`、`effort`、`operation_id` 等。
- `/api/sessions/{engine}/{threadID}/collaboration`：已有的引擎参数化路由。
- `/api/scheduled-tasks`：一套路由服务三种引擎，用 `agent_kind` 字段区分。
- `events` 表：三种引擎共用，`stream_id` 就是线程 ID，事件类型带引擎前缀。

### 需要注意的不对称

- `agent_operations.thread_id` 和 `approvals.thread_id` 的外键只指向 `codex_threads`。Gemini 和 Claude 的操作通过线程表的 `operation_id` 反向关联。
- 能力判断分散在各线程结构体的 `*Supported` 字段里（`SteerSupported`、`RewriteSupported`、`GoalSupported` 等），由各自的查询函数根据 Agent 版本和 CLI 版本填充。
- 三种引擎的状态集合不完全一样。Gemini 和 Claude 有“确认已停止”的流程，Codex 没有；Codex 有审批、目标、压缩，Gemini 有目标和计划，Claude 有会话内提问和压缩事件。
- 登录会话已经占用了表名 `sessions`，统一后的会话表不能用这个名字。

## 目标

1. 控制面里按引擎名分支的代码只出现在驱动注册处和各驱动内部。
2. 新增一个会话功能时，公共部分只写一次；引擎相关部分通过驱动接口的一个方法或一个能力位表达。
3. 前端只有一个会话页面外壳，引擎差异体现为可插拔的渲染器和设置面板。
4. 过程中每一步都可发布，用户看到的行为不变。

不在本阶段范围内：

- 统一三种 CLI 的原生行为。引擎之间真实存在的能力差异要保留并如实呈现，不要为了对称而模拟。
- 改变 Agent 与 CLI 之间的通信方式。
- 改变线上的操作类型字符串。P2 期间控制面与 Agent 之间的协议保持不变，协议层的收敛在 P4。
- 引入任务对象、任务调度器、成员通信或审查返工；这些新增行为属于 P7，不以完成 P2 宣称已经具备。

### 与任务层的接口边界

- 会话始终用（引擎，会话 ID）标识；任务层另外记录执行操作 ID，不能把会话的最新一次操作当作某个历史步骤的结果。
- 统一会话服务是 P7 创建、发送、查询和中断轮次的入口。任务服务不依赖三张线程表的具体结构，不要求先做 P2-08 的物理合表。
- 保留当前入队、授权、工作区锁和结果对账的事务语义；P7-01 再核对原子派单所需的契约。若需扩展接口，在 P7 独立设计和验收，不在本阶段暗改事务边界或模拟一种 CLI 不具备的能力。
- 会话可以独立存在；任务关联、成员角色和验收状态放在任务层，不写进 CLI 原生历史来代替持久化。

## 目标设计

以下是方向性的草图。字段和方法以实现时的真实需要为准，P2-01 的盘点结果可以修正它。

2026-11-15 执行基线更新为 M2 结束的 `be2c2d1`：Codex Store 声明已在 `store/codex_threads.go`，HTTP 线程/轮次在 `httpapi/codex_threads.go`、`codex_turns.go`，路由在 `httpapi/routes.go`，前端主体在 `web/src/codex/SessionView.tsx`；下文旧 `resources.go` / `App.tsx` 引用按这些位置理解。

依赖核对修订：Store 已依赖 `sessiontools.Ref`，因此别名迁入的 `internal/session` 必须保持为不依赖 Store 的契约包；驱动实现和注册表的装配位于 `internal/sessionservice`（可以导入 Store）。协作的事务内数据访问与锁留在 Store，编排通过事务接口接入上层服务，不能用分开的 Store 调用替代原事务。登录会话已经有 `Store.SessionByToken`，统一 AI 会话以 `AISession` / `AISessions` 命名，避免与现有 `store.Session` 类型冲突。

### 包与类型

新增 `internal/session` 包，位于 `store` 之上、`httpapi` 之下。

```go
package session

type Engine string

const (
    Codex  Engine = "codex"
    Gemini Engine = "gemini"
    Claude Engine = "claude"
)

// Ref 取代 sessiontools.Ref。过渡期在 sessiontools 里保留类型别名。
type Ref struct {
    Engine Engine `json:"engine"`
    ID     string `json:"id"`
}

type Session struct {
    Ref
    WorkspaceID, ProjectID, ServerID, Path string
    ProjectName, ServerName, ServerStatus string
    NativeID    string // codex_thread_id 或 session_id
    Title       string
    Status      string
    Model       string
    Mode        string // Codex/Gemini 的 approval_mode，Claude 的 permission_mode
    Effort      string
    OperationID string
    PinnedAt, ArchivedAt *time.Time
    ProjectPinnedAt, ProjectHiddenAt *time.Time
    CreatedAt, UpdatedAt time.Time
    Capabilities Capabilities
}

type Capabilities struct {
    Images, Steer, SteerImages     bool
    Rewrite, Fork, ForkWorktree    bool
    Questions, Approvals           bool
    Goal, Plan, Compact            bool
    Skills, SkillsManage           bool
    Effort, ConfirmStopped         bool
}
```

### 控制面驱动接口

每种引擎实现一个驱动，注册到一张表里。HTTP 处理器、调度器、协作服务只和接口打交道。

```go
type Driver interface {
    Engine() Engine
    Capabilities(server ServerInfo) Capabilities

    Create(ctx context.Context, in CreateInput) (Session, error)
    QueueTurn(ctx context.Context, s Session, in TurnInput) (protocol.StreamEvent, error)
    Steer(ctx context.Context, s Session, in SteerInput) error
    Interrupt(ctx context.Context, s Session) error
    ConfirmStopped(ctx context.Context, s Session) error
    Fork(ctx context.Context, s Session, in ForkInput) (Session, error)
    Update(ctx context.Context, s Session, in UpdateInput) error
    Delete(ctx context.Context, s Session) error
}
```

引擎独有的功能（Codex 的审批和状态快照、Gemini 的目标模式、Claude 的技能发现）不进公共接口。它们通过可选的小接口暴露，处理器用类型断言判断是否支持，例如 `type GoalDriver interface { ... }`。这样公共接口保持精简，不会被某一种引擎的概念污染。

驱动的第一版实现只是对现有 Store 方法的薄包装：`QueueTurn` 在 Claude 驱动里调用 `Store.QueueClaudeTurn` 或 `QueueClaudeRewrite`。底下保留三张原生线程表，由驱动封装。2026-11-15依据D8的取消条件评估后，不再实施P2-08物理合表。

### HTTP 路由

| 新路由 | 取代 |
| --- | --- |
| `GET /api/sessions?engine=&archived=&q=&limit=&cursor=` | `/threads`、`/gemini/threads`、`/claude/threads` |
| `POST /api/sessions/{engine}` | 三个创建接口 |
| `GET、PATCH、DELETE /api/sessions/{engine}/{id}` | 三组详情、更新、删除 |
| `GET /api/sessions/{engine}/{id}/events` | 三个事件接口，统一为 `before`、`after`、`limit` 游标 |
| `POST /api/sessions/{engine}/{id}/turns` | 三个发起轮次接口 |
| `POST /api/sessions/{engine}/{id}/events/{eventID}/rewrite` | 三个编辑重发接口 |
| `POST /api/sessions/{engine}/{id}/steer`、`/interrupt`、`/confirm-stopped`、`/fork`、`/fork-worktree` | 对应的三组接口 |
| `GET /api/sessions/{engine}/{id}/usage` | 三个用量接口 |
| `POST /api/sessions/{engine}/{id}/questions/{requestID}/answer` | Gemini 和 Claude 的回答接口 |

列表路由省略 `engine` 参数时返回三种引擎的混合列表，排序和分页规则对三种引擎一致。`archived`、`q`、`engine` 都在分页前应用；不能沿用只在 Codex 里存在、数据变化时容易重复或跳项的裸 `offset`。列表项必须带上统一分组所需的 `engine`、项目和服务器名称、服务器状态、项目置顶和隐藏时间、会话置顶和归档时间；引擎独有字段仍通过能力对象或引擎模块表达。

按 P6-01 的 Q8、Q11，列表在分页前用 `q` 搜索组名、路径、标题、服务器名、Wio 会话 ID 和原生会话 ID；响应固定为 `{items,next_cursor,groups}`。混合列表采用不透明的会话级游标，排序键依次为项目置顶标志与时间、会话置顶标志与时间、`updated_at`、`engine`、`id`。`groups` 在 `engine`、`archived`、`q` 过滤后、会话分页前计算，至少包含 `workspace_id`、匹配会话数、最近更新时间和项目置顶时间；名称和规范工作区标题与 `/workspaces` 合并后在前端生成，并按 P6-01 Q7 排组。不能只从当前页推断哪些工作区有会话，否则 Q2 的“显示全部工作区”会把尚未翻到的工作区误判为空。按项目和工作区组织的新入口（总览的决策 D9）要靠它取数。

引擎独有的接口保留在引擎命名空间下，例如 `/api/sessions/codex/{id}/goal`、`/api/sessions/gemini/{id}/goal`。请求的引擎不支持某个操作时，统一返回同一种错误码和错误结构，前端据此隐藏入口。

响应体在过渡期同时包含旧字段（`steer_supported` 等）和新的 `capabilities` 对象，直到 P2-10。

### 前端

```text
web/src/sessions/
  api.ts            按引擎参数化的请求函数
  types.ts          AISession、Capabilities、DisplayItem
  useSessionList.ts 列表、分组、置顶、归档
  useSessionEvents.ts 事件窗口、增量、游标
  SessionPage.tsx   外壳：侧栏、时间线、输入区、预览面板
  Sidebar.tsx / Timeline.tsx / Composer.tsx
  engines/
    codex.tsx       事件到 DisplayItem 的转换、模型选择器、设置面板、额外操作
    gemini.tsx
    claude.tsx
```

每个引擎模块导出同一种形状的对象，外壳根据 `capabilities` 决定显示哪些入口。现有的 `codexContent.ts`、`geminiContent.ts`、`claudeContent.ts` 是各引擎转换逻辑的起点。

地址栏的 `/?view=claude&thread=ID` 形式必须继续可用，因为协作结果和通知里已经持久化了这种链接。

按总览的决策 D9，三个引擎页面最终会合并成按项目和工作区组织的一个入口。所以外壳不要假定“一个页面只有一种引擎”：列表、置顶、分组、选中状态都用（引擎，会话 ID）标识会话，引擎差异只通过引擎模块表达。

### 数据表（P2-07；P2-08按D8取消）

M4使用schema2/3的扩展迁移，已发布基线与原生线程结构保留：

- `scheduled_tasks`：三引擎共用任务结构，以`engine`区分原生线程；保留原任务字段和server外键。
- `server_engines`：以`(server_id,engine)`为主键，合并CLI状态与已应用凭据绑定；绑定时间不随心跳改变，Git绑定继续独立。
- `codex_threads`、`gemini_threads`、`claude_threads`及其原生附表继续由驱动读写。公共列表通过[AISession统一读模型](../../internal/store/ai_sessions.go)读取，以`(engine,id)`标识会话。

原先的`ai_sessions`物理表及`engine_state`草案不实施。会出现在SQL条件或原子更新中的原生字段继续保留为真实列；原生会话ID、配置修订、问题和分叉快照等差异仍属于驱动。定时任务没有跨三张线程表的数据库外键，创建/改绑定必须在同一事务内锁定原生线程，线程和父级删除必须在同一事务内清理任务；这些约束在取消物理合表后继续有效。

## 步骤

### P2-01 引擎行为盘点与契约测试

**做法**

1. 盘点三种引擎在下列操作上的行为，写成本文件末尾的“引擎差异清单”：创建、发起轮次、编辑重发、运行中追加、中断、确认停止、分叉、分叉到 worktree、归档、删除、事件分页、用量。每项记录：请求参数、成功响应、各种失败的状态码和错误文本、状态流转、是否占用工作区写锁、对 Agent 版本和 CLI 版本的要求。
2. 把每处差异标注为“有意”（引擎本身不同）或“偶然”（实现时没对齐）。偶然的差异登记到总览的“发现的问题”，由用户决定统一到哪一种行为。
3. 盘点三张线程表每个字段的用途，标出哪些出现在 `WHERE`、`UPDATE ... SET x = 子查询` 或唯一约束里。
4. 新增表驱动的契约测试，对三种引擎运行同一组用例，通过现有的 HTTP 路由完成。Agent 一侧沿用 `internal/httpapi` 现有测试的做法：用 `PendingOperations` 读出排队的操作，再用 `CompleteOperation` 写入结果，不启动真实的 Agent。引擎不支持的用例显式跳过并写明原因。真正穿过 Agent 和引擎进程的验证由 P0-07 负责，两者的分工见 P0-07 的“说明”。

**验收**

- 差异清单完整，每个偶然差异都有处理结论或已登记。
- 契约测试通过，并且在后续每一步都保持通过。这组测试是整个 P2 的安全网。

**说明**

这一步不改任何产品代码。它的产出决定了后面接口的形状，不要跳过。

### P2-02 `internal/session` 包与统一能力解析

**做法**

1. 新建 `internal/session`，定义 `Engine`、`Ref`、`Capabilities`。`sessiontools.Ref` 改为类型别名，保持协作协议的 JSON 不变。
2. 实现唯一的能力解析函数：输入引擎、Agent 版本、CLI 版本，输出 `Capabilities`。内部调用 `buildinfo.Supports*`。
3. 三个线程结构体的 `*Supported` 字段改为从这个函数取值，JSON 字段名和取值保持不变。响应里增加一个 `capabilities` 对象，与旧字段并存。
4. 为能力解析写表驱动测试：列出若干（引擎、Agent 版本、CLI 版本）组合及预期结果，覆盖 `buildinfo/version.go` 里每个门槛的边界。

**验收**

- 契约测试通过；前端不需要任何改动就能正常工作。
- 除了驱动和能力解析之外，没有新增的 `buildinfo.Supports*` 调用点。

**说明**

`docs/CLAUDE.md` 和 `docs/GEMINI.md` 里的“兼容版本”表格今后以能力解析的测试表为准。

### P2-03 合并近乎相同的处理器

**做法**

把替换引擎名后几乎一样的成对文件合并为按引擎参数化的单一实现。按差异从小到大做：

| 文件对 | 行数 | 差异行 |
| --- | --- | --- |
| `httpapi/claude_updates.go`、`gemini_updates.go` | 141、141 | 2 |
| `httpapi/claude_credentials.go`、`gemini_credentials.go` | 71、71 | 6 |
| `store/claude_questions.go`、`gemini_questions.go` | 81、70 | 11 |
| `httpapi/claude_management.go`、`gemini_management.go` | 75、85 | 44 |

合并时把 Codex 的对应实现（`codex_updates.go` 等）一并纳入，如果它的结构允许。路由保持不变，只是三条路由指向同一个处理器加不同的引擎参数。

**验收**

- 契约测试和现有测试通过。
- 被合并的文件对消失，行数净减少。

**说明**

这一步是低风险的热身，用来验证“按引擎参数化”在这个代码库里的写法，为 P2-04 定下风格。

### P2-04 统一读模型与控制面驱动接口

规模大，分三个提交。

**提交一：统一读模型**

1. 把 `collaborationSessions` 查询提升为公共的会话查询，放到 `store` 里独立的文件，提供 `Session(ctx, ref)` 和 `ListSessions(ctx, filter)`。
2. 协作（`store/session_collaboration*.go`）、用量（`store/session_usage.go`）、定时任务列表改用它。
3. 三个引擎各自的线程查询函数暂时保留，供驱动内部使用。

**提交二：驱动接口与三个实现**

1. 在 `internal/session` 定义 `Driver` 和注册表。
2. 三个驱动分别包装现有的 Store 方法。引擎独有功能用可选接口暴露。
3. 驱动需要的 Store 方法如果签名差异太大，先在驱动内部适配，不改 Store。

**提交三：调用方迁移**

1. `store/session_collaboration_actions.go` 里 22 处按引擎分支改为调用驱动。这个文件在 `store` 包里，而驱动在它之上；需要把协作动作的编排上移到 `internal/sessionservice`，`store` 只保留数据访问。上移是这一步里最需要小心的部分，先移动、后替换分支，分两次提交也可以。
2. `internal/scheduler/scheduler.go` 的 `enqueue`、`enqueueClaude`、`enqueueGemini` 改为通过驱动入队。
3. `httpapi/scheduled_tasks.go` 里的引擎分支改为调用驱动。

**验收**

- 契约测试、协作测试（`store/session_collaboration_test.go` 等）、调度器测试通过。
- 在 `internal/store`、`internal/scheduler`、`internal/sessionservice` 里搜索 `"claude"`、`"gemini"`、`"codex"` 字面量，剩余的只出现在驱动实现、SQL 的引擎列取值和测试里。把搜索结果贴到“执行记录”。

**风险与回滚**

协作功能有持久化的排队流程（worktree 准备、轮次结束后分叉、自动续聊）。上移编排逻辑时，事务边界必须保持不变：原来在同一个事务里完成的事情，移动后仍然在同一个事务里。每个提交单独可回滚。

### P2-05 统一 HTTP 路由

**做法**

1. 实现“目标设计”一节的 `/api/sessions/...` 路由，处理器只依赖驱动接口。
2. 列表接口按“目标设计”统一筛选和公共字段：`q` 在分页前搜索组名、路径、标题、服务器名、Wio 会话 ID 和原生会话 ID；响应固定为 `{items,next_cursor,groups}`；混合列表按项目置顶标志与时间、会话置顶标志与时间、`updated_at`、`engine`、`id` 使用不透明游标稳定分页；`groups` 在 `engine`、`archived`、`q` 过滤后、会话分页前生成，至少返回 `workspace_id`、匹配会话数、最近更新时间和项目置顶时间。契约测试至少覆盖：省略 `engine` 的混合页、三种单引擎页、活动和归档视图、六类搜索字段且搜索在分页前执行、项目和会话置顶、相同时间戳下不重不漏、完整分组摘要不受当前页截断，以及三个驱动返回相同的公共分组字段。
3. 事件接口统一为同一套游标参数和默认窗口。当前 Gemini 和 Claude 的事件接口固定返回最多 500 条，Codex 的接口有自己的分页和 `view=raw` 参数；以功能最完整的一种为准，其余补齐。
4. 旧路由全部保留，改为转发到新处理器。它们的响应必须与改动前逐字段一致，用契约测试保证；其中 Codex 的数组/分页对象双响应和 `archived=all` 也属于兼容面，不能反向污染新路由的契约。
5. 审计动作名和实时事件类型保持不变，前端的实时刷新依赖它们。
6. 为新路由写一份对三种引擎运行的契约测试，与 P2-01 的旧路由测试并存。

**验收**

- 新旧两套契约测试都通过。
- `httpapi/claude.go`、`httpapi/gemini.go` 以及 `resources.go` 里的线程处理器缩减为路由转发或被删除。

**说明**

这一步完成后，新路由还没有调用方。不要在同一步里改前端。

### P2-06 前端会话外壳

规模大，按引擎分批迁移，每批一个提交。

2026-11-15 开工基线 `ad055e7`：公共 API 已在 `docs/SESSION_API.md` 固化；会话页面主体和 Codex 钩子已随 M2 移出 App。P3-08 尚未建立数据层，P2-06 先建立可由该步骤复用的会话端点与取数接口，保留现有通用缓存和修订号来源，避免让两步互为前置；P3-08 再收敛其他页面。引擎模块保留各自时间线、原生设置和创建默认值；公共外壳接管布局、侧栏、选择、列表/详情、移动端三态、文件预览及公共会话动作。新增 API 的分页与搜索通过统一列表钩子接入；三个导航入口和旧深链不变。现有页面测试在迁移时保留完整场景，改为针对外壳与相应引擎模块运行。

**做法**

1. 建立 `web/src/sessions/` 的 `api.ts` 和 `types.ts`，只调用新路由。
2. 先抽公共部分，不改页面结构：从 `ClaudePage.tsx` 和 `GeminiPage.tsx` 抽出列表与分组、侧栏、输入区、图片选择、重命名与删除对话框。这两个页面最相似，先让它们共用同一批组件。公共侧栏按 P6-01 的差异清单留出服务端搜索和游标翻页、完整工作区分组摘要、项目与会话置顶、归档操作以及（引擎，会话 ID）键；详情请求拿到但当前列表窗口没有的会话要合并进对应组。移动端保留当前三个页面都有的“会话 / 文件 / 对话”三态。列表刷新由外壳注入一个公共 revision，不在引擎页面内各建轮询器；P3-09 尚未完成时最多保留一个页面级、仅页面可见时运行的兜底轮询。
3. 建立 `SessionPage` 外壳和引擎模块接口，把 Claude 迁移进去，然后是 Gemini。
4. 最后迁移 Codex。它的会话视图是 `App.tsx` 里的 `SessionView`，逻辑最多（审批、状态快照、目标、子 Agent 活动、长会话分页），需要 P3-07 先把它移出 `App.tsx`。
5. 每迁移一个引擎，对应的旧页面文件删除，旧测试改写为针对新外壳的测试，覆盖点不减少。

**验收**

- 现有的 `ClaudePage.test.tsx`、`GeminiPage.test.tsx`、Codex 相关测试所覆盖的行为，在新外壳的测试里都有对应用例。
- Playwright 桌面和移动端用例通过。
- `/?view=<engine>&thread=<id>` 形式的旧链接仍能打开对应会话。
- 构建通过 `check:bundle` 的体积检查。

**风险与回滚**

三个引擎分三批，任何一批出问题都只回滚那一批。旧页面在对应引擎迁移完成前不要删除。

**说明**

这一步不改用户可见的行为：导航里仍然是三个引擎入口，各自只显示自己的会话。把三个入口合并成一个是决策 D9 的后续工作，在 [P6](P6-unified-session-entry.md) 里单独成步，不要夹带在这里。动手前先看 P6-01 是否已经有结论，它可能对外壳的列表和新建流程提出要求。这一步要做到的是：合并入口时只需要改导航和列表的取数范围，不需要再动外壳。

### P2-07 定时任务与服务器引擎配置合表

2026-11-15 开工基线 `056227d`：P2-04已将列表提升到AISession统一来源，当前调度写入通过`scheduledTable`选择旧表，原计划的`strings.ReplaceAll`已不存在。分两批新增版本化迁移，已发布的baseline/schema与旧迁移不修改：先将定时任务复制到带engine的新表并切换全部运行期读写/清理/保留逻辑，再合并服务器引擎状态及已应用凭据绑定。保留旧表和列，但不再作为运行期权威来源；Git凭据为独立能力，其绑定/更新表继续保留。凭据更新的操作记录也继续保留用于密文快照追溯。

`server_engines.updated_at`保留原凭据绑定时间，不能随心跳改写，否则会错误改变Gemini/Claude配置修订；无绑定时允许NULL，与CLI状态行的存在性区分。迁移与运行期适配需保持“无已应用配置”的空值和旧模型快照语义。两批均验证SQLite/PostgreSQL、旧/新HTTP契约、调度、协作与并发；用现有授权保留的数据库备份在隔离副本中复验，不修改生产数据库。

P1降级护栏已部署：旧控制面会拒绝较新的schema，不能直接指向升级库回滚。回滚必须恢复升级前数据库备份，或先用支持新schema的工具导出/协调增量；恢复备份会丢失升级后的变更。旧表只是恢复参考，并非可直接降级的保证。

依赖 P1 完成。这是第一个真正改表的步骤，用结构最简单的表来验证迁移流程。

**做法**

1. 新迁移：建立 `scheduled_tasks` 表，在现有结构上增加 `engine` 列；把三张旧表的数据复制进去。复制前断言三张表的 `id` 没有重复，有重复则迁移失败并报出冲突的 ID。
2. `scheduled_tasks.thread_id`无法声明指向三张原生表的外键。创建/改绑定时在事务内锁定目标线程；驱动删除以及项目/工作区父级删除在同一事务内清理对应任务，并用SQLite/PostgreSQL及并发测试保障。P2-08按D8取消后继续保留这些约束，不再安排添加统一线程外键。
3. `store/scheduled_tasks.go`改为读写新表，移除`scheduledTable`等旧物理表选择辅助函数；保留原生线程查找和事务锁适配。
4. 旧表保留不删，不再写入。清理在 P2-10。
5. 同样的方式处理服务器级的引擎配置：建立 `server_engines(server_id, engine, cli_version, ready, credential_profile_id, profile_revision, model, updated_at)`，合并 `servers` 表的四个引擎列、`server_claude_cli` 和三组凭据绑定表。这一部分可以拆成单独的提交。

**验收**

- P1-01 的结构一致性测试增加新夹具后通过，两种数据库都通过。
- 用带真实定时任务数据的库副本验证：迁移后任务数量、下次运行时间、启用状态都与迁移前一致。
- 调度器测试和定时任务的 HTTP 测试通过。

**风险与回滚**

旧版控制面会被P1的schema版本护栏拒绝。回滚需恢复升级前数据库备份（升级后的变更将丢失），或使用支持新schema的工具协调增量。旧表保留到P2-10作为恢复参考，不作为可直接降级的保证。

### P2-08 线程表合并

2026-11-15，P2-07验收完成后，依据D8预先允许的取消条件，将本步标记为`已取消`。不新增物理线程合表迁移。

**评估证据与边界**

| 检查 | 结果 |
| --- | --- |
| 公共生命周期、协作和调度 | 通过session驱动/注册表和统一读模型访问；sessionservice及scheduler非测试代码没有三张物理线程表名 |
| HTTP与前端 | `/api/sessions`走统一读取和引擎传输适配器；前端公共外壳/列表按`(engine,id)`工作，非测试代码不引用物理线程表 |
| 剩余原生HTTP查询 | approvals.go有两条Codex审批查询，summary.go有一条Codex历史计数；均属于原生兼容行为，不构成三套通用生命周期实现 |
| 行为和数据验证 | 新旧路由契约、混合列表、协作和调度测试、SQLite/PostgreSQL及race通过；真实副本65项原生会话关键字段摘要未变，三引擎真实进程浏览器冒烟通过 |
| 定时任务引用完整性 | P2-07已覆盖同名跨引擎线程、并发创建/删除和父级级联清理；保留事务锁与清理约束 |

公共调用方已经能复用同一会话接口，物理合表不会再消除一层公共分支，却会重写原生原子更新和多张表的外键。当前保留驱动和原生结构，M5统一入口与P7任务编排继续依赖会话接口。P2-10的前置改为P2-06/P2-07发布并运行一个版本周期，清理范围排除仍在使用的原生表及凭据追溯结构。

以下为取消前的迁移方案、验收和风险，仅保留历史说明，不继续执行。

**做法**

1. 根据 P2-01 的字段盘点，确定 `ai_sessions` 的最终列清单，写进本文件的“目标设计”。
2. 新迁移：建表，复制三张线程表及 `claude_thread_options`、`claude_thread_management` 的数据。复制前断言 ID 不重复。
3. 外键改指向新表：`agent_operations.thread_id`、`approvals.thread_id`、`gemini_goals.thread_id`、`scheduled_tasks.thread_id`。SQLite 改外键需要重建表，迁移里按 SQLite 官方推荐的重建流程处理。
4. 三个驱动的数据访问改为读写新表。`Store` 里三套线程方法合并或删除。
5. `agent_operations.thread_id` 开始为 Gemini 和 Claude 的操作填值，协作查询里用线程表 `operation_id` 反查的写法可以简化。
6. 旧表保留不删，不再写入。

**验收**

- 两种数据库的结构一致性测试、契约测试、协作测试、调度器测试全部通过。
- 用真实数据的库副本验证：三种引擎的会话数量一致；随机抽取若干会话，标题、状态、模型、原生会话 ID、置顶、归档时间一致；能继续对话。
- 并发相关测试（`store/session_concurrency_test.go`、`agentgateway/session_concurrency_test.go`）通过。

**风险与回滚**

这是整个路线图里风险最高的一步。

- 必须在 P1-03 的降级保护已经在生产运行之后做。
- 升级说明里写明最低可回滚版本，以及回滚会丢失升级后产生的会话变更。
- 入队逻辑里有依赖行级条件更新的并发控制（例如只有状态不是运行中时才能把线程置为排队）。合表后这些条件更新的语义必须保持，逐条对照迁移。

### P2-09 Agent 端运行器接口

**做法**

1. 以 `cd293a9` 核对，P3-04 已把会话操作移动到 `internal/agent/operation_sessions.go` 和 `operation_cli.go`，分派位于 `operations.go`。提取自更新检查与载荷解码；Gemini / Claude 的工作区规范化、协作桥接共用入口。Codex 的路径检查、桥接和常驻 app-server 生命周期继续由原生适配器负责，不能改变原有检查顺序。
2. 定义 Agent 端的运行器接口，三个引擎包装器各自组合现有原生适配器，实现发起轮次、追加、中断、回答提问。接口统一调用入口和返回值，保留各引擎的 JSON 类型与中断结果；原生适配器不反向依赖 Agent 包。事件发布也收敛为一处入口，仍使用旧信封，为 P4-05 做准备。
3. 线上的操作类型字符串和载荷结构保持不变。
4. 同一提交里升级 `buildinfo.Version`。

**验收**

- Agent 包和三个适配器包的测试通过。
- P0-07 的跨进程验收测试不需要修改就能通过。如果假引擎支持，再给三种引擎各补一条“运行中追加”的用例。
- 在一台真实服务器上，用新 Agent 对三种引擎各完成一次：发起、追加、中断、编辑重发。这一步需要用户配合或授权；做不了时在“执行记录”里注明。

**说明**

三种 CLI 的进程模型不同：Codex 是常驻的 app-server，Gemini 走 ACP，Claude 是每轮一个进程。接口只统一调用入口，不要强行统一生命周期。

### P2-10 清理旧路由、旧表、旧类型

前置：P2-06和P2-07已经随某个版本发布，并且在生产运行过至少一个版本周期。P2-08按D8取消，不再作为前置；不能删除仍在驱动中使用的原生线程表。

2026-11-16 开工核对（`ab4ab40`）：统一外壳及 schema 2/3 已随 `bda5584` 部署，之后又完成 M5 `63e5307` 和交互改进 `e09f490` 两次生产发布，版本周期前置已满足（Agent 版本仍为 0.3.21，不等于控制面未发布）。本步骤在同一提交中清理接口及前端，并追加存储收缩迁移；全部验收后才关闭步骤。

清理边界：删除旧的三套会话集合、详情及生命周期 HTTP 路由，统一接口只返回 `capabilities` 能力对象；`steer_ready` / `recovery_pending` 等动态状态继续保留。项目批量归档、工作区模型和技能管理仍有实际调用方且没有统一替代路由，继续保留，不能按引擎名一并删除。原生驱动/CLI 字段及 Store 内部用于原生命令的能力值不是旧 HTTP 类型，按实际调用判断是否保留。最终引用核对还须删除驱动中仅服务旧列表/详情/事件路由的处理器及 offset 响应类型；保留公共参数解析，有用的事件边界测试迁往统一事件入口。

存储范围以运行期引用审计为准：三张旧定时任务表、三张旧引擎状态/绑定表、`servers` 中四个旧 Codex/Gemini 状态列，以及混合绑定表中已停用的 Codex 绑定列。混合表的 Git 字段、原生线程/附表、凭据更新和已应用密文快照追溯继续保留。冻结基线和已发布迁移/夹具不改；新增迁移须验证两种数据库、失败整体回滚、重复启动及历史升级。schema 4 尚未发布，不虚构其历史发布夹具，沿用现有已发布 schema 3 夹具并以迁移前的 schema 4 合成库验证收缩。

**做法**

1. 删除旧 HTTP 路由和响应里的旧能力字段。
2. 新迁移：只删除P2-07已经停用的三张旧定时任务表及服务器引擎配置表/列。三张原生线程表及附表、仍服务Git绑定的结构和仍用于已应用密文快照追溯的操作记录继续保留；逐项审计运行期引用后才能收缩。
3. 删除不再使用的前端旧响应类型和旧请求路径；引擎模块仍依赖的原生字段类型继续保留，不以统一外壳为由直接删除。
4. 更新 `docs/USER_GUIDE.md` 第 7 节的能力对照、`docs/SESSION_COLLABORATION.md`、`README.md` 的仓库结构。

**验收**

- 全部测试通过。
- 重新统计总览“基线数字”里与会话相关的各项，把前后对比写进“执行记录”。

## 阶段完成标准

- 进度总表里 P2 的步骤全部 `已完成` 或 `已取消`。
- 公共生命周期编排和调度经统一驱动访问，不再按引擎分支；驱动注册、SQL 列值、原生事件解释、CLI/凭据管理、历史迁移和测试仍可使用引擎名。按 P2-04 的逐项审计边界验收，不要求把原生持久化及独有协议伪装为无引擎逻辑。
- 新增一个假想的第四种引擎时，需要改动的位置可以列成一份不超过一页的清单。把这份清单写出来作为阶段的最后一项产出。

## 引擎差异清单

详细盘点见 [P2-01 引擎行为与持久化字段清单](P2-engine-contracts.md)：12 项操作（分叉与 worktree 分开）、版本门槛、全部物理列、原子条件与错误索引。`session_contract_test.go` 通过真实认证路由对三引擎执行公共生命周期、失败保护、事件/用量、追加、重发和两类分叉；Codex 无确认停止接口的用例明确跳过。CLI 原生输入/历史/中断差异有意保留；列表、空更新、错误文本和成功响应等偶然差异已登记总览，旧路由在过渡期兼容，P2-10 发布后退役。

## 执行记录

按时间顺序追加，每条包含日期、步骤编号、会话标识、做了什么、验收结果、遗留事项。

- 2026-11-14，方向同步，11-14 Codex：按 D10、D11 补充 P2 与 P7 的职责和接口边界，明确任务按实际操作关联结果、复用驱动且不依赖物理合表。仅更新计划，未领取或实现 P2 步骤；文档验证统一记录在 P7-00。

- 2026-11-15，M3 / P2-01，Codex：从 `be2c2d1` 干净工作区领取；先盘点并锁定旧路由契约。本轮逐任务自动本地提交授权见总览。

- 2026-11-15，P2-01，Codex：完成行为/版本/物理列盘点和显式错误索引（`P2-engine-contracts.md`）；新增真实认证路由的三引擎表驱动契约，覆盖创建/详情/更新/列表、轮次入队与完成、写锁、重发、追加、队列取消、确认停止、归档/删除、事件游标、用量、两类分叉；保存六份逐字段响应快照。36 个矩阵用例，Codex 不支持确认停止明确跳过。未改产品代码，偶然差异登记总览。`npm --prefix web run build`、`go vet ./...`、`go test ./...`（199.65 秒）、`npm --prefix web test`（451 项）、`npm --prefix web run typecheck`、Go 双入口构建、格式、差异检查与路线图本地链接检查均通过；无需 PostgreSQL/页面 E2E（未改 Store 或交互）。按授权本地提交，随后继续 P2-02。

- 2026-11-15，P2-02 开工修订：`Engine` 采用 string 类型别名并定义三个常量，保持现有协作构造器给 Ref.Engine 传普通 string 的源码兼容性。能力对象区分完整续聊 `fork` 和消息边界 `fork_messages`，增加现有 Gemini thinking/yolo 与协作版本能力；它只表达版本支持，不代表在线、已初始化或运行中就绪。边界测试覆盖会话相关门槛；自更新、部署回滚等非会话门槛继续由 buildinfo 原有测试覆盖。

- 2026-11-15，P2-02，Codex：新增不依赖 Store 的 `internal/session` 契约包；Ref 迁为别名，统一 20 个能力位及版本解析。三种线程读取增加 capabilities；Gemini/Claude 旧 Supported 字段全部从同一结果赋值，动态 ready 判定不变；Codex 只多查版本列（不公开）以解析能力。新增 Ref 线上格式、全部会话版本边界（含 CLI 双门槛、无效版本和描述后缀）、高版本引擎能力形状，以及详情/列表的旧字段与新对象一致性测试；更新兼容版本文档。`go list -deps ./cmd/agent` 确认 session/sessiontools 为 Agent 依赖，同步版本 0.3.19。
  - 验收：前端构建、go vet、全部 Go 测试（275.92 秒）、PostgreSQL（26.76 秒）、前端类型检查、双入口构建、fmt/diff、staticcheck 与 version-check 均通过；旧路由 36 项矩阵保持通过。首次 Vitest 为 450 通过/1 失败，失败为总览已登记的 AppSession WebSocket 清理时序断言；源码不变的文件复验 6 项通过，完整复验 451 项通过。未改页面交互，不要求额外 E2E。本地提交后继续 P2-03。

- 2026-11-15，P2-03 开工核对：Codex 的版本设置/检查/选择与另两引擎相同，一并合并；`updateCodexCLI` 的旧门槛、专门操作和错误响应继续独立。Codex 凭据是多 profile 绑定/加密包，问题回答走审批/输入流程，项目归档的错误映射、审计名与发布事件也不同，不硬并入两引擎公共实现。合并范围保持 HTTP/Store 旧公开方法作为薄适配，问题回答事务和 Claude 特有取消检查均保留。

- 2026-11-15，P2-03，Codex：八个成对重复文件消失，统一为 `cli_settings.go`、`engine_credentials.go`、`session_management.go`、`session_questions.go`；Codex 通用版本设置也合入，专有更新操作保留在 `codex_updates.go`。按原九个文件/新五个文件统计，929→613 行，净减316行。旧方法/响应类型作为薄适配保留；Claude 问题取消检查、两引擎不同版本门槛、凭据命令/入队签名、fork-worktree 响应差异、审计和事件字符串全部保留。问题回答的加锁、回执复用和插入仍处于同一个原事务。
  - 验收：旧路由矩阵、版本设置/更新、凭据和问题回答定向测试通过；前端构建、go vet、全部 Go 测试（266.98秒）、PostgreSQL（27.48秒）、451项 Vitest、typecheck、双入口构建、fmt/diff 均通过；真实进程浏览器冒烟通过（setup/login/内置 Agent/三引擎回复，9.55秒）。未修改 Agent 依赖包，版本保持0.3.19。按授权本地提交后继续 P2-04。

- 2026-11-15，P2-04 开工修订：统一查询底层保留 revoked 服务器行，公开会话/协作投影再过滤；定时任务历史保留其原有 offline 行和项目原名，避免读模型合并改变列表。先提交读模型，再处理驱动与调用方。协作入口仍需支持 Store 原有 API 和内部测试；为避免 Store→sessionservice→Store 循环，将不依赖 Store 的协作编排放入 `internal/sessionservice/core`，通过绑定原事务的端口访问数据；根 sessionservice 装配公共驱动，Store 驱动文件适配私有事务方法。引擎字面量验收按职责检查：公共编排/调度不能有引擎分支；原生事件解释、兼容迁移、凭据/CLI 持久化属于驱动持久化侧，逐项记录，不在 M3 改写 Agent 协议或迁移 SQL。

- 2026-11-15，P2-04 提交一，Codex：新增 AISession/AISessions 统一读模型（现有 Session 是登录会话，故避免同名）；公共字段、版本能力、Codex 执行配置、字面量子串搜索、归档/引擎过滤和项目/会话置顶排序集中在同一查询。协作投影、定时任务列表与用量读取复用该来源；保留定时任务的任务服务器/项目原名和 revoked 历史，以及 Codex 用量对 revoked 行的旧读取语义。新增三引擎读模型、搜索转义/分页前过滤和撤销历史测试。Store/HTTP 的读模型、定时任务、用量、旧路由契约和协作矩阵定向测试通过（20.26秒/5.53秒）；完整验收在步骤结束前执行。P2-04 继续进行。

- 2026-11-15，P2-04 提交二，Codex：在 Store 无关的 session 包定义 Driver/Registry、统一记录和调度输入，Store 原结构保留类型别名；sessionservice 装配三种原生驱动，迁移调度入队及 HTTP 定时任务的引擎参数校验。默认模式、错误分类/文本、版本门槛、Codex 先 claim 再 queue 的事务边界及另两引擎的原子 run 回写保持不变；唤醒与日志通过驱动构造参数注入。定时任务/旧路由契约/调度定向检查通过（scheduler13.997秒，httpapi4.353秒）。session 是 Agent 依赖，版本同步0.3.20；继续协作事务驱动和编排迁移，完整验收仍待步骤结束。

- 2026-11-15，P2-04 驱动审计，Codex：公共协作动作与准备阶段移至 `sessionservice/core`（不依赖 Store），Store 事务端口绑定原 `*sqlx.Tx`，未增加 begin/commit 或改变 savepoint。三种事务驱动分别实现 Create/Send/Steer/Interrupt/Fork/Pin/SetExecution；公共调度和 sessionservice/core 不再按引擎分支。保留 Store 入口及类型别名，旧协作测试直接穿过新编排。定向 Store46.794秒、scheduler16.767秒、httpapi6.289秒均通过。
  - 字面量检查命令：`rg -n '"(claude|gemini|codex)"' internal/store internal/scheduler internal/sessionservice --glob '*.go' --glob '!**/*_test.go'`。下列38处是实际结果；scheduler/sessionservice 无匹配。`session_driver_collaboration.go` 是驱动实现；`session_usage.go` / `session_collaboration_result.go` 解释引擎原生事件；线程创建/问题回答是原生持久化适配；`scheduled_tasks.go` 是旧物理表/原生 lookup 适配（P2-07 合表再收缩）；CLI/凭据/Agent 更新是服务器引擎持久化配置。没有把这些边界改写为公共生命周期编排，亦未修改迁移/协议取值。SQL 引擎列和本来就有的测试不纳入业务分支数。

```text
internal/store/session_usage.go:39:	if provider == "claude" {
internal/store/session_usage.go:44:	if provider == "codex" {
internal/store/session_usage.go:80:		if provider == "gemini" {
internal/store/session_usage.go:161:		if provider == "gemini" {
internal/store/session_questions.go:32:	return s.queueSessionAnswer(ctx, threadID, requestID, answers, questionSpec{"claude", "Claude", "claude_threads", claudeThreadSelect, "claude_version", true})
internal/store/session_questions.go:35:	return s.queueSessionAnswer(ctx, threadID, requestID, answers, questionSpec{"gemini", "Gemini", "gemini_threads", geminiThreadSelect, "gemini_version", false})
internal/store/claude.go:97:	if err := s.createCollaborationSessionTx(ctx, tx, sessiontools.Ref{Engine: "claude", ID: id}, workspaceID, "Claude", model, mode, effort); err != nil {
internal/store/gemini.go:134:	if err := s.createCollaborationSessionTx(ctx, tx, sessiontools.Ref{Engine: "gemini", ID: id}, workspaceID, "Gemini", model, mode, level); err != nil {
internal/store/credential_profiles.go:41:	codex, err := s.preferredCredentialProfile(ctx, "codex")
internal/store/credential_profiles.go:104:	if scope == "codex" {
internal/store/credential_profiles.go:113:	if scope == "codex" {
internal/store/credential_profiles.go:221:			scope = "codex"
internal/store/codex_threads.go:343:	if err := s.createCollaborationSessionTx(ctx, tx, sessiontools.Ref{Engine: "codex", ID: id}, workspaceID, title, "", "on-request", ""); err != nil {
internal/store/cli_install.go:43:	if kind == "gemini" {
internal/store/cli_install.go:45:	} else if kind == "claude" {
internal/store/cli_install.go:47:	} else if kind != "codex" {
internal/store/cli_install.go:54:	if kind == "claude" {
internal/store/cli_install.go:65:	if command.Kind != "codex" && command.Kind != "gemini" && command.Kind != "claude" {
internal/store/cli_install.go:106:		if op.Kind == protocol.OpCodexUpdate && kind == "codex" {
internal/store/session_collaboration_result.go:18:	if t.Engine != "codex" || (op.Kind != protocol.OpCodexTurnStart && op.Kind != protocol.OpCodexTurnRewrite) {
internal/store/agent_update.go:69:	for _, kind := range []string{"codex", "gemini", "claude"} {
internal/store/scheduled_tasks.go:40:	if kind == "claude" {
internal/store/scheduled_tasks.go:43:	if kind == "gemini" {
internal/store/scheduled_tasks.go:55:	if kind == "claude" {
internal/store/scheduled_tasks.go:59:	if kind == "gemini" {
internal/store/scheduled_tasks.go:63:	if kind != "" && kind != "codex" {
internal/store/session_driver_collaboration.go:139:		if !collaborationModelPattern.MatchString(strings.TrimSuffix(p.Model, "[1m]")) || len(p.Model) > 128 || engine != "claude" && strings.HasSuffix(p.Model, "[1m]") {
internal/store/session_driver_collaboration.go:151:	case "codex":
internal/store/session_driver_collaboration.go:157:	case "claude":
internal/store/session_driver_collaboration.go:159:	case "gemini":
internal/store/session_driver_collaboration.go:187:	case "claude":
internal/store/session_driver_collaboration.go:197:	case "gemini":
internal/store/session_driver_collaboration.go:263:	if t.Engine == "codex" && t.OperationID == "" {
internal/store/session_driver_collaboration.go:287:		if t.Engine == "codex" {
internal/store/session_driver_collaboration.go:307:	case "codex":
internal/store/session_driver_collaboration.go:309:	case "claude":
internal/store/session_driver_collaboration.go:311:	case "gemini":
internal/store/session_driver_collaboration.go:319:	if t.Engine == "codex" {
```

- 2026-11-15，P2-04 完成，Codex：统一读模型、公共驱动/注册表、三种事务驱动、协作动作/延迟准备编排及调度调用方迁移均完成；保留旧 Store/HTTP API、数据库表和线上字符串。完整验收通过：前端构建19.83秒、go vet、全部 Go 测试267.28秒、PostgreSQL59.03秒、451项 Vitest、typecheck、双入口构建、fmt/diff、staticcheck/version-check，真实进程三引擎冒烟9.63秒通过。第一次 vet 检出跨包 LookupError 的未命名结构体字段，补齐字段名后重跑通过；未出现产品行为回归。未改页面交互，无需页面 E2E。前两个提交为506115b、e376acf，本次提交完成步骤；继续P2-05。

- 2026-11-15，P2-05 开工修订，Codex：读取基于P2-04统一查询，采用SQL键集游标（包含全部排序键并绑定筛选条件），分组摘要独立于当前会话页计算；新列表默认活动会话、50条，上限100，archived支持false/true/all。HTTP的原生校验、审计和成功响应保留在三个引擎传输适配器里，由消费方的小接口提供动作；它们组合公共session.Driver，旧路由与新路由复用同一动作实现，避免重新实现旧错误/事务。新详情/列表使用统一公共字段并保留原生旧字段；新事件统一500默认/1000上限和before/after/view=raw，Codex保留对话投影，其他引擎保留原生事件。缺失动作或版本不支持均返回409、CAPABILITY_UNSUPPORTED。旧路由的数组/分页对象、过滤宽松程度、错误顺序、固定事件窗口不变。

- 2026-11-15，P2-05 实现，Codex：统一路由、三引擎传输适配器、SQL键集分页及分页前完整工作区摘要已实现；新增[统一会话API说明](../SESSION_API.md)。列表/详情/创建/更新合并公共读模型和原生旧字段，其余原生动作沿用已有请求/成功响应。旧claude.go/gemini.go及Codex公共线程入口缩为转发；191条旧路由全部保留，路由快照显式增加23条至214条，认证/CSRF中间件数量不变。新增三引擎新路由生命周期矩阵，与旧矩阵共享场景但独立运行；读取/归档/搜索/置顶/事件/不支持动作/问题回答另行验证新契约。版本同步0.3.21（session是Agent依赖）。SQLite键集与旧/新路由定向测试、go vet通过；PostgreSQL分页矩阵通过（58.622秒）。最终全量验收进行中。

- 2026-11-15，P2-05 验收完成，Codex：`npm --prefix web run build`（20.27秒）、`go vet ./...`、`go test ./...`（311.58秒）、`make test-postgres`（59.29秒）、`npm --prefix web test`（451项全部通过）、`npm --prefix web run typecheck`、Go双入口构建、格式与差异检查、staticcheck、版本护栏全部通过；真实进程浏览器三引擎冒烟通过（9.59秒）。新旧路由契约及SQLite/PostgreSQL分页矩阵均通过，包含删除游标锚点、列表头部插入、筛选绑定和完整分组摘要。对照P2-04提交审计52个原生声明，规范化接收者和空白后函数体无变化。路线图本地Markdown链接检查通过。未改前端交互，无需额外页面E2E。按授权本地提交，随后登记实际提交号并结束M3。

- 2026-11-15，M3 完成，Codex：P2-01至P2-05全部验收通过并已本地提交；P2-05实现提交为`96efec6`，总表已登记实际提交号。控制面具备统一会话读模型、驱动和`/api/sessions`路由，旧接口继续兼容，Agent版本0.3.21。本轮按单里程碑授权结束，未领取M4/P2-06，无待续的代码改动；未推送、发布或部署。本次收尾仅修改进度文档，检查本地Markdown链接、步骤状态与提交引用一致性及`git diff --check`，不重复运行已通过的构建测试。

- 2026-11-15，M4 / P2-06 开工，Codex：从已部署的`ad055e7`干净工作区领取，先实现会话端点、公共类型和键集列表钩子。列表重用原`useData`的事件合并与取消语义，翻页沿用不透明游标，实时刷新从新首页游标重读已加载尾页；筛选变化隔离旧请求，混合列表按（引擎，ID）去重，完整组摘要独立于已加载窗口。4项定向测试覆盖筛选切换/迟到响应、头部插入后的尾页刷新、错误保留与重试及混合标识，全部通过；类型检查通过。当前属于P2-06基础提交，完整验收及三个引擎迁移继续进行，未标记步骤完成。

- 2026-11-15，P2-06 / Claude 批次，Codex：建立公共 `SessionPage`、`Sidebar` 和工作区分组，Claude 原页面移入 `sessions/engines/claude.tsx`，路由懒加载同步迁移。列表使用统一游标、服务端搜索和完整组摘要，详情深链可合并进当前组；原生创建默认值、模型/技能发现、纠偏、改写、问题回答、协作和项目操作保留。生命周期、用量、问题与管理动作切至新会话路径，项目/工作区原路由保留。50 项 Claude 场景、3 项服务器范围场景通过；3 项分组测试覆盖空组、窗口外摘要及深链筛选，全部通过；类型检查通过。本批为独立迁移提交，Gemini、Codex 和公共输入区抽取继续进行，完整构建/E2E 在步骤结束前执行。

- 2026-11-15，P2-06 / Gemini 批次，Codex：Gemini 页面迁入 `sessions/engines/gemini.tsx`，与 Claude 共用外壳、侧栏、游标列表、完整分组与深链合并；工作区直接创建与原生配置默认值保留。新增共享输入区、附件列表、重命名对话框，保留各引擎的禁用条件、Slash 键盘接管、输入法判定、图片处理并发策略和标题 trim 差异。Gemini 生命周期/目标/用量/问题/管理及协作子会话删除切至统一路径。两个原页面的121项场景和分组/范围/用量/协作等27项测试共148项通过，类型检查通过。Claude 批次提交 `61d5806`，Codex 迁移及全量验收继续进行。

- 2026-11-15，P2-06 / Codex 批次，Codex：Codex 原页面与原生模块移入 `sessions/engines/`，统一使用外壳/侧栏/不透明游标列表；原生审批抽屉、工作区写权限、待入列表的分叉会话、目标和快照、事件增量/回卷/LRU 与阅读位置保留。应用外壳改从引擎无关的会话内存注册表清理和失效缓存。Claude/Gemini 管理菜单及其对话框收敛为泛型钩子，保留不同的分叉能力门槛；项目批量归档继续走项目 API，并增加两个路径回归用例。统一页面修订钩子暂时承接可见页面的5秒保险轮询，P3-09 再收敛到实时断线恢复机制。
  - 全量 Vitest 首次与浏览器并行时两个已登记的用例超时；没有修改超时或产品行为，停止其他重任务后以 `--maxWorkers=1` 完整复验63个文件、457项全部通过。此次新增的两项项目归档与共享管理改动另做定向复验。
  - Playwright44项中33项通过、11项按原桌面专用规则跳过；三个引擎的桌面/移动图片、输入与旧深链覆盖均通过。构建、类型检查和体积护栏通过，最大块206.77kB（上限500kB）。Go通用检查尚待步骤关闭前执行。
  - 共享管理最终定向复验3个文件126项全部通过（Claude51、Gemini72、服务器范围3），包括新增的两项项目批量归档路径回归。

- 2026-11-15，P2-06 完成，Codex：最终实现提交 `e26e117`（基础 `e4a64bf`、Claude `61d5806`、Gemini `12925ef`）。最终前端构建/类型及体积检查、`go vet ./...`、`go test ./...` 全部通过。真实控制面/内置Agent/三种CLI替身的浏览器验收1项通过（8.7秒），请求未经拦截，验证统一新建/读取/发送链路实际可用。旧深链和三个入口保留。完成本地研发验收，未推送或部署；开始M4后续数据层与刷新收敛。

- 2026-11-15，P2-07 迁移前基线：从空库导出0.3.21/schema1的SQLite与PostgreSQL结构夹具，仅包含结构与版本记录，无业务数据。新增三引擎活动会话禁止删除/空闲删除后定时任务消失的特性测试。SQLite结构/删除检查8.329秒、PostgreSQL完整迁移存储测试56.153秒通过；先提交基线。Store不在cmd/agent依赖中，本步只改控制面存储不升级Agent版本。

- 2026-11-15，P2-07 迁移复核补充：新表暂时没有线程外键，除了驱动删除，还必须保留项目/工作区删除导致的原生线程级联清理。在相同事务内清理不存在的（engine,thread_id），覆盖项目元数据删除、托管项目删除回执及工作区删除回执；空工作区/空项目删除与服务器删除已有空条件或server外键保障。先补充计划再加入适配，不改变原删除条件。

- 没有线程外键时还需保持并发引用完整性：创建/改绑定时任务在写入事务内对目标原生线程做条件行锁，线程已被删除则返回原有不存在错误，避免线程读取后删除与新任务插入交错产生孤立记录。锁不改变线程字段值；父级级联删除会等待同一行锁并在事务内清理任务。

- 2026-11-15，P2-07 定时任务批次完成：新增schema2并逐字段复制三张旧表，全部运行期调度/结果归属/取消/保留清理转到新表；旧表保留。SQLite覆盖19列一致性、冲突事务回滚重试、跨引擎同名线程、父级删除和并发引用完整性；PostgreSQL完整集成58.313秒通过。`go vet ./...`、`go test ./...`最终通过（Store232.490秒、scheduler18.728秒）；首次全量发现两处测试触发器/原始SQL仍写旧表，修正测试适配后完整复验通过。前端未变，沿用P3-09已通过的构建、478项Vitest及33项浏览器结果。本批先本地提交，P2-07继续服务器引擎配置迁移与真实副本演练。

- 2026-11-15，P2-07 服务器配置迁移复核：SQLite资源管理和PostgreSQL旧库集成测试使用仅id/name的人工servers表，缺少所有真实基线夹具从最早版本就存在的codex_version/codex_ready。本批迁移要复制这些历史值；先修订该人工夹具，补上这两列的原默认值，不改已发布schema或迁移。运行期旧模型回填测试显式准备旧绑定快照，同时为新读路径准备新绑定，继续独立验证不可变的旧迁移。

- 2026-11-15，P2-07 服务器配置批次实现完成，Codex：新增schema3，CLI/已应用绑定迁至server_engines，心跳只更新CLI字段，凭据回执只更新绑定字段；保留绑定时间、源修订、Claude应用模型快照、独立Git绑定和密文操作追溯。原生/统一会话版本读取全部切换。旧schema与基线迁移函数保持不变；无Agent依赖变动，版本保持0.3.21。
  - SQLite迁移/凭据/配置/旧结构/协作定向检查通过；PostgreSQL完整集成59.966秒通过。真实保留备份经校验后在本机隔离实例恢复：2项定时任务的19列、6台服务器的18项引擎配置逐字段双向比对一致，65项原生会话关键字段摘要不变；重复迁移、旧清单拒绝schema3通过，演练22.676秒。生产数据库未修改，临时测试和私有环境信息未进入仓库。
  - 最终前端构建19.60秒、typecheck11.94秒、go vet、全部Go测试175.10秒（Store168.075秒）、迁移/调度删除/跨会话并发race15.919秒、真实三引擎浏览器1项8.6秒、fmt/staticcheck/version-check及Go双入口构建均通过。首次Go全量发现SQLite人工资源夹具也缺少Codex历史列，补全测试基线后完整复验通过。
  - 通用Vitest连续两轮均为477通过/1失败，均为总览已登记的AppSession关闭WebSocket时序断言；源码不变的文件定向复验6项通过。未修改该测试或产品行为，已向用户确认是否将测试同步修正单独纳入本轮。P2-07及M4验收暂不关闭；本批实现先本地提交，未推送或部署。未提交的重构实现将在本次提交后清空，后续只剩验收处理与D8评估；分支main。

- 2026-11-15，M4当前交接，Codex：定时任务批次91404fa、心跳特性测试54c15f2、服务器引擎批次df004c4均已本地提交；总表P3-08/P3-09验收和提交列填反的问题已在da74e81修正，并检查58行无其他列位错误。main无未提交产品或测试改动，未推送、部署。
  - 尚余：用户确认是否把已登记的AppSession测试同步修正纳入本轮。拟只将关闭WebSocket的立即断言改为waitFor等待，并新增对应import；产品代码不变。确认前未修改测试。完整Go/PostgreSQL/真实副本/race/真实浏览器/构建/类型检查/静态检查均已通过；两次全量Vitest的同一时序失败及独立6项通过结果见上一记录，不能把它记成全量通过。
  - D8只读评估已备好，未领取前置尚未关闭的P2-08：sessionservice、scheduler和web/src非测试代码不引用三张物理线程表；统一列表和动作已经由读模型及驱动访问。HTTP层仅剩approvals.go两条Codex原生审批查询及summary.go一条Codex历史计数，属于保留行为的独有兼容边界。具备按D8取消物理合表的条件，最终状态在P2-07验收关闭后登记。若取消，需同步M4目标、P2-10前置/清理范围，并保留P2-07的应用级引用完整性约束；不要删除仍在使用的原生线程表。未启动M5。

- 2026-11-15，用户授权的独立测试同步修正，Codex：AppSession只新增waitFor导入，并等待WebSocket.closed变为true后断言，未修改产品代码或放宽超时。定向6项通过；临时阻止WebSocket关闭时该用例仍按预期失败，反向探测文件已删除。完整Vitest 68文件478项全部通过（142.42秒），前端构建/体积护栏20.39秒、typecheck13.28秒通过。此改动单独提交；此前等待授权的事项已解除，继续P2-07和M4验收收尾。Go/Store/浏览器产品源码与df004c4相同，沿用已通过的对应验收，不重复运行无关重任务。

- 2026-11-15，P2-07验收关闭，Codex：定时任务迁移91404fa、服务器引擎迁移df004c4及心跳特性测试54c15f2均完成。独立AppSession测试修正f9bf855后，通用验收全部通过；SQLite/PostgreSQL结构与数据迁移、调度/新旧HTTP契约、协作并发及race、真实备份隔离恢复与逐字段比对、重复启动、降级拒绝、真实三引擎浏览器冒烟均通过，实际命令和结果见前述记录。总表改为已完成，验收GPT-6（Codex，自验）。未更改Agent依赖或版本，未推送、发布或部署。继续执行D8的P2-08必要性评估。

- 2026-11-15，D8 / P2-08评估完成，Codex：P2-07前置已在a795faa关闭，按既定取消条件将P2-08标记已取消，证据和剩余原生兼容查询列于本步。统一会话服务、协作、调度及前端不再依赖物理线程合表；保留原子写入、原生附表及P2-07的应用级引用完整性约束。同步M4交付目标、数据设计与P2-10前置/清理范围，未改产品代码、未新增迁移。本次只做文档调整，验证链接、步骤依赖、表格列位与git diff --check，沿用已通过的实现验收，不重复构建测试。M4四项实现全部完成，一项按D8取消；未启动M5。

- 2026-11-15，M4收尾，Codex：P2-07在a795faa完成验收登记，P2-08在fc41aee按D8取消；独立测试修正f9bf855已通过68文件478项完整Vitest和正反验证。P2-06/P3-08/P3-09此前均完成，M4全部步骤现为已完成或已取消。总表已回填实际提交号；Markdown链接、58行列位、提交引用、步骤依赖和M5未开始边界检查通过。本次纯文档收尾不重复已通过的构建测试。main无待续代码，未推送或部署，下载的真实数据库副本和私有连接临时文件已清理。停止在M4，不自动启动M5。

### 2026-11-16 P2-09 开始

- Codex 在 main / `cd293a9` 领取；前置 P2-04、P3-04 已完成。更新拆分后的源码定位，明确 Codex 原生路径 / 桥接检查不能被提前，以保持行为不变。
- 已有完整 malformed 命令、重复派发、原生追加 / 问答 / 中断测试及 P0-07 跨进程链路；先单独补齐三引擎启动前的错误优先级与工作区拒绝测试，再引入运行器。事件发送路径已有跨进程测试覆盖，收敛后再补入口契约断言。

- P2-09 前置特性测试：`go test ./internal/agent -run 'TestSessionStartPreparationOrder|TestOperationDispatchRejectsMalformedCommands|TestExecuteRejectsMalformedCommandsBeforeSideEffects' -count=1` 通过；覆盖自更新拒绝先于 JSON 解码、Gemini / Claude 工作区校验先于环境与适配器配置。仅测试和阶段定位更新，单独提交。

### 2026-11-16 P2-09 本地完成

- GPT-6（Codex）验收，main。新增私有 `sessionRunner` 及三种引擎包装实现，统一发起（含原生重发）、追加、中断和提问 / 审批回答入口。原生命令类型、上下文、中断超时及返回数据不变；Gemini / Claude 保留停止回执，Codex 仍依靠原生事件完成。
- 提取启动前的更新保留检查、宽松 JSON 解码，Gemini / Claude 共用原位置的工作区规范化和协作桥接；Codex 的路径校验、协作与常驻 app-server 生命周期继续由原生适配器负责。保留自更新拒绝先于解码、工作区拒绝先于配置的顺序。
- 三引擎事件统一从 `emitSessionEvent` 入队，仍发原 `event` / `gemini_event` / `claude_event` 信封，已有事件 ID 保留、缺失 ID 自动生成，原始内容、时间、序号和重要队列标记不变，为 P4-05 提供统一发出位置。
- Agent 版本同步 0.3.23。附带规范 P4-02 两个文件的导入排序，无逻辑变化。抽取后逐函数比较 `operation_cli.go` / `operation_sessions.go` 中保留的其他操作，忽略空白后全部与前置提交相同。

| 验收 | 实际结果 |
| --- | --- |
| `npm --prefix web run build` | 通过，5.32 秒 |
| `go vet ./...`、`go build ./cmd/controlplane ./cmd/agent` | 通过，无诊断 |
| `go test ./...` | 全部通过；控制面跨进程用例 7.191 秒、HTTP 309.842 秒、Store 242.080 秒 |
| Agent、Codex / Gemini / Claude 适配器及控制面包强制重跑 | 全部通过，2.261 / 1.953 / 1.620 / 4.006 / 5.470 秒 |
| 运行器 / 原有 malformed 派发与准备顺序 | 通过；三引擎控制动作在触及适配器前拒绝错误 JSON |
| 事件入口契约 | 通过；旧信封、已有 ID、生成 ID 唯一性、完整事件内容不变 |
| `npm --prefix web test -- --maxWorkers=1` | 73 文件、510 用例通过，160.34 秒 |
| `npm --prefix web run typecheck` | 通过，无诊断 |
| `make fmt-check`、Agent 版本护栏、`git diff --check` | 通过；0.3.22 → 0.3.23 |

P0-07 原验收场景和期望值未修改即通过（仅整理 import 排序）；原假引擎尚不支持三种运行中追加，因此未扩充该可选场景。三个原生适配器的追加 / 提问 / 中断集成用例全部通过。没有改 Store 或页面交互，本步不需要另跑 PostgreSQL / E2E。真实服务器三引擎发起、追加、中断、编辑重发尚待明确发布授权，已按步骤允许的方式记录此限制；未推送、未部署，未夹带缺陷修复。


### 2026-11-16 P2-10 开始

- Codex 在 main / `ab4ab40` 领取。核对总览三次生产发布记录，收缩前置满足；先补当前原生能力与统一对象一致的边界验证，再清理已停用接口与响应。路线图已更新实际文件/范围，保留项目归档、原生技能/模型管理及凭据追溯。

### 2026-11-16 P2-10 收缩清理与验收

Codex 在 main 完成同一步骤的接口、前端与数据库收缩；按逐任务授权验收后本地提交。本轮不推送、不部署，也未访问生产数据库。

- 移除 52 条旧会话路由（含工作区下创建别名）、41 个 API 薄包装及其共享 `legacySession`，并删除三个驱动中共 9 个仅供旧列表/详情/事件路由使用的处理器。统一详情/事件直接走公共实现，移除旧 offset 列表响应类型及专用常量；保留公共分页参数解析。原生动作校验、CLI 字段、项目批量归档、工作区模型和技能接口继续使用。
- 统一响应只公开 `capabilities` 能力对象，不再公开原生 `_supported` 别名；动态 `steer_ready`、`recovery_pending` 不变。前端 Gemini/Claude 引擎模块和技能弹窗改读能力对象，保留仍被模块使用的原生线程字段类型。设置页的定时任务选择器也迁往统一列表；与工作区任务共用可取消、完整遍历 cursor 的 hook，失败时不展示部分结果并可重试。新增设置页跨页选择验证；工作区已有同 ID 跨引擎、分页及重试覆盖继续通过。
- 生命周期/能力测试迁到真实认证的统一路由；新增三引擎旧接口 404 且不删除原会话的验证。旧响应快照和旧 offset API 独有测试随接口退役；统一 CRUD、混合列表、搜索、游标矩阵和 Store 分页覆盖保留。1001 条事件的最近窗口、最大 1000 条、raw/conversation、before/after 及顺序测试改走统一事件入口；统一接口既有的 `limit=0` 返回 400 断言继续保留。跨进程测试改用统一路径，能力完整期望表与旧字段禁止出现断言保留。
- 追加 schema 5 `retire_session_compatibility_storage`，删除六张停用表和五列，准确清单及回滚方式见[生产部署指南](../PRODUCTION_DEPLOYMENT.md#更新回滚与备份)。没有改冻结基线、已发布迁移或历史夹具，也没有修改任何操作/事件的持久化取值。原生线程及附表、Git 绑定、凭据更新和密文追溯记录继续保留；本步无 Agent 依赖源码改动，Agent 版本保持 0.3.26。
- SQLite / PostgreSQL 共用收缩断言：构造 schema 4 的三引擎同 ID 会话以及新旧不同的任务/配置，逐表逐行比较全部保留数据；执行全部 DROP 后注入失败，验证结构与数据整体回滚；成功后验证删除范围、重复迁移稳定、版本 5 只记录一次、旧程序拒绝连接，心跳、Git 及当前模型投影正常。历史回填测试改成从真实的迁移前结构准备数据，避免在 schema 5 上删除 baseline 记录后伪造旧库而重建已退役表；PostgreSQL 的历史模型测试用独立 schema，最新库继续验证运行期读取。
- 更新用户手册的能力说明、协作接口边界、仓库结构、第四引擎扩展清单及破坏性升级说明。schema 5 发布后，schema 1/3/4 制品都不能直接连接该库；最低可回滚制品必须支持 schema 5，否则同步恢复升级前备份并处理升级后的增量。这里记录的是待发布方案，不代表线上迁移已经执行。

统计方法：文件用 `wc` 等价的行数统计；路由按 `testdata/routes.txt`（路由快照测试验证）；表数用合成 schema 4 和真实 `store.Open` 的内存空库查询，排除 SQLite 内部表；测试数用 Go `-list '^Test'`、Vitest 和 Playwright 运行器。`0` 表示文件已拆出/删除，不表示同领域逻辑消失。

| 项目 | 路线图初始基线 | P2-10 完成时 / 本步前后 |
| --- | --- | --- |
| HTTP 路由 | 191 条 | 本步 215 → 163；旧会话相关路由 52 → 0，其余引擎配置/归档接口保留 |
| httpapi 非测试源码总行数 | 未记 | 本步 9839 → 9359，净减 480 行 |
| `httpapi/resources.go` / `store/store.go` | 3258 / 2641 | 0（已按领域拆分）/ 103 |
| `agentgateway/gateway.go` / `agent/client.go` | 1516 / 1428 | 371 / 299 |
| `web/src/App.tsx` | 2133 | 177 |
| `styles.css` / `i18n.tsx` | 2604 / 2821 | 2618 / 2843，本步未调整全局样式/文案结构 |
| 成对 updates / credentials 处理器 | 141×2 / 71×2 | 原文件均已合入公共配置/凭据实现，四个旧文件 0 行 |
| `httpapi/claude.go` + `gemini.go` | 695 | 本步 73 → 0；原生动作在引擎驱动中保留 |
| 实际数据表 | 静态 55 / 启动实际 56 | 本步 schema 4 的 61 → schema 5 的 55（含迁移版本表）；历史扩展增加表后收缩 6 张 |
| 原生线程表 / 定时任务表 | 3 / 3 | 3 / 1；线程表按 D8 保留、统一任务表取代旧三表 |
| 启动迁移 | 21 个无版本函数 | 5 项有版本迁移，既有版本跳过执行；首次 baseline 内仍保留冻结的历史函数 |
| `session_collaboration_actions.go` 引擎分支 | 22 | 0；公共调度/协作无引擎名字面量，原生持久化/事件及配置边界按阶段标准保留 |
| `buildinfo.Supports*` | 31 | 31；P4-03 的线上升级前置未满足，本步不删历史门槛 |
| 测试运行器 | Go 852；前端 449；E2E 44 次 | Go 默认顶层测试 932；前端 511；E2E 43 通过 / 11 既有跳过 |

验收记录：

- `npm --prefix web run build`、`npm --prefix web run typecheck`、Go 双入口构建、`make fmt-check`、Agent 版本护栏及 `git diff --check` 通过；护栏确认无 Agent 源码改动。Markdown 本地链接检查 73 个目标全部存在。
- `go vet ./...`、`go test ./...` 第一轮全部通过；最终删除旧驱动死代码后的全量复验全部通过（httpapi 310.651 秒，Store 263.777 秒）。统一路由、退役路由与事件边界定向复验通过（66.078 秒）。
- `make test-postgres` 通过（114.085 秒），包含历史结构夹具升级、新库一致性及收缩回滚断言。此前首次因历史模型测试在最新库上读取已删表失败，已按真实旧结构准备数据后复验通过，没有修改实际产品读取。
- `npm --prefix web test -- --pool=threads --maxWorkers=1` 最终 73 文件 / 511 项全部通过（169.20 秒）。首次与 Go/PostgreSQL 等重任务并行时出现 6 项查询等待/5 秒超时；源码和等待条件未变的 5 文件复验 134 项通过，随后完整复验通过。已在总览登记，未夹带测试超时或产品修复。
- `npm --prefix web run e2e` 桌面/移动合计 43 项通过、11 项既有跳过（约 3 分钟）。真实 `TestCrossProcessAcceptance` 通过（1.979 秒）；没有把模拟 CLI 的真实进程链路当作线上升级验收。

M6 仍有 P4-03：必须先发布能力协商版本并核对所有未吊销 Agent 完成升级，才可确定最低版本并移除历史门槛。本轮可本地推进的实现完成后停在此发布节点，不提前关闭 M6，也不启动 M7。
