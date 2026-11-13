# P2 统一会话内核

返回[路线图总览](README.md)。状态以总览的“进度总表”为准，本文件只写步骤细节和执行记录。

这是路线图里投入最大、回报也最大的阶段。目标是让“会话”成为一个与引擎无关的概念：三种引擎共用一套模型、一套控制面接口、一套 HTTP 路由和一个前端外壳，引擎差异收敛到各自的驱动实现和能力声明里。

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

## 目标设计

以下是方向性的草图。字段和方法以实现时的真实需要为准，P2-01 的盘点结果可以修正它。

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

由 P2-01 填写。

| 操作 | Codex | Gemini | Claude | 差异性质 | 处理 |
| --- | --- | --- | --- | --- | --- |
| | | | | | |

## 执行记录

按时间顺序追加，每条包含日期、步骤编号、会话标识、做了什么、验收结果、遗留事项。

（暂无）
