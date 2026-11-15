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

驱动的第一版实现只是对现有 Store 方法的薄包装：`QueueTurn` 在 Claude 驱动里调用 `Store.QueueClaudeTurn` 或 `QueueClaudeRewrite`。底下仍然是三张表，直到 P2-08。

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

### 数据表（P2-07、P2-08）

```sql
CREATE TABLE ai_sessions (
  id TEXT PRIMARY KEY,
  engine TEXT NOT NULL,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  native_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'idle',
  model TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT '',
  effort TEXT NOT NULL DEFAULT '',
  operation_id TEXT NOT NULL DEFAULT '',
  engine_state TEXT NOT NULL DEFAULT '{}',
  pinned_at TIMESTAMP,
  archived_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

原则：会出现在 SQL 条件里或需要原子更新的引擎私有字段保留为真实列，其余放进 `engine_state`。哪些字段属于前一类，由 P2-01 的盘点确定。例如 `gemini_threads.model_credential_revision` 在入队事务里用子查询原子更新，应当保留为列。

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

依赖 P1 完成。这是第一个真正改表的步骤，用结构最简单的表来验证迁移流程。

**做法**

1. 新迁移：建立 `scheduled_tasks` 表，在现有结构上增加 `engine` 列；把三张旧表的数据复制进去。复制前断言三张表的 `id` 没有重复，有重复则迁移失败并报出冲突的 ID。
2. 线程表合并之前，`scheduled_tasks.thread_id` 无法声明指向三张表的外键。在这期间，删除线程时由驱动的 `Delete` 负责删除对应的定时任务，并为此补测试。P2-08 完成后再加外键。
3. `store/scheduled_tasks.go` 改为读写新表，去掉 `strings.ReplaceAll` 生成查询的做法和 `scheduledTaskTable`。
4. 旧表保留不删，不再写入。清理在 P2-10。
5. 同样的方式处理服务器级的引擎配置：建立 `server_engines(server_id, engine, cli_version, ready, credential_profile_id, profile_revision, model, updated_at)`，合并 `servers` 表的四个引擎列、`server_claude_cli` 和三组凭据绑定表。这一部分可以拆成单独的提交。

**验收**

- P1-01 的结构一致性测试增加新夹具后通过，两种数据库都通过。
- 用带真实定时任务数据的库副本验证：迁移后任务数量、下次运行时间、启用状态都与迁移前一致。
- 调度器测试和定时任务的 HTTP 测试通过。

**风险与回滚**

迁移后旧版控制面读的是不再更新的旧表。升级说明里要写明：升级后如果回滚，回滚期间新建或修改的定时任务会丢失。旧表保留到 P2-10，保证数据本身可恢复。

### P2-08 线程表合并

受决策 D8 约束。开始前先确认是否仍然需要做。

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

1. `internal/agent/client.go` 里处理 `gemini.turn.start`、`claude.turn.start`、`codex.turn.start` 等操作的分支，有共同的前置步骤：检查是否正在自更新、解析载荷、解析工作区根目录、建立协作桥接。把公共部分提取出来。
2. 定义 Agent 端的运行器接口，三个适配器各自实现：发起轮次、追加、中断、回答提问。
3. 线上的操作类型字符串和载荷结构保持不变。
4. 同一提交里升级 `buildinfo.Version`。

**验收**

- Agent 包和三个适配器包的测试通过。
- P0-07 的跨进程验收测试不需要修改就能通过。如果假引擎支持，再给三种引擎各补一条“运行中追加”的用例。
- 在一台真实服务器上，用新 Agent 对三种引擎各完成一次：发起、追加、中断、编辑重发。这一步需要用户配合或授权；做不了时在“执行记录”里注明。

**说明**

三种 CLI 的进程模型不同：Codex 是常驻的 app-server，Gemini 走 ACP，Claude 是每轮一个进程。接口只统一调用入口，不要强行统一生命周期。

### P2-10 清理旧路由、旧表、旧类型

前置：P2-06 和 P2-08 已经随某个版本发布，并且在生产运行过至少一个版本周期。

**做法**

1. 删除旧 HTTP 路由和响应里的旧能力字段。
2. 新迁移：删除三张旧线程表、三张旧定时任务表、旧的服务器引擎配置表和列。
3. 删除前端的 `Thread`、`GeminiThread`、`ClaudeThread` 类型和旧请求路径。
4. 更新 `docs/USER_GUIDE.md` 第 7 节的能力对照、`docs/SESSION_COLLABORATION.md`、`README.md` 的仓库结构。

**验收**

- 全部测试通过。
- 重新统计总览“基线数字”里与会话相关的各项，把前后对比写进“执行记录”。

## 阶段完成标准

- 进度总表里 P2 的步骤全部 `已完成` 或 `已取消`。
- 在 `internal/httpapi`、`internal/store`、`internal/scheduler`、`internal/sessionservice` 中，引擎名字面量只出现在驱动注册、SQL 列值和测试里。
- 新增一个假想的第四种引擎时，需要改动的位置可以列成一份不超过一页的清单。把这份清单写出来作为阶段的最后一项产出。

## 引擎差异清单

详细盘点见 [P2-01 引擎行为与持久化字段清单](P2-engine-contracts.md)：12 项操作（分叉与 worktree 分开）、版本门槛、全部物理列、原子条件与错误索引。`session_contract_test.go` 通过真实认证路由对三引擎执行公共生命周期、失败保护、事件/用量、追加、重发和两类分叉；Codex 无确认停止接口的用例明确跳过。CLI 原生输入/历史/中断差异有意保留；列表、空更新、错误文本和成功响应等偶然差异已登记总览，旧路由继续兼容。

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
