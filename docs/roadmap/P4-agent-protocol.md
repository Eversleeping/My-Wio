# P4 Agent 协议

返回[路线图总览](README.md)。状态以总览的“进度总表”为准，本文件只写步骤细节和执行记录。

控制面与 Agent 之间的协议目前靠约定维持：类型是散落的字符串，能力靠版本号推断，载荷没有线上格式测试。本阶段把这些约定变成编译器和测试能检查的东西。线上的字符串取值和 JSON 字段全程不变，变的只是代码里引用它们的方式，以及双方互相了解对方的方式。

## 现状

### 传输与信封

- 一条 gRPC 双向流，服务名 `wio.agent.v1.AgentService`，方法 `Connect`。编解码是 `internal/protocol/protocol.go` 里的 `jsonCodec`，消息就是 JSON，构建不需要 protoc。
- 上行是 `AgentEnvelope{message_id, server_id, kind, occurred_at_unix_ms, payload_json}`，下行是 `ControlEnvelope{operation_id, kind, created_at_unix_ms, payload_json}`。`kind` 是普通字符串，`payload_json` 是按 `kind` 解释的 JSON。
- `internal/protocol` 有 9 个非测试文件、1156 行、106 个结构体，只有一个测试文件（`gemini_input_test.go`，50 行）。这个包不依赖仓库里任何其他包。
- `proto/agent.proto` 只有 27 行，描述两个信封和 `Connect` 方法，自初始版本以来没有改过。它不参与构建，也不描述任何一种 `kind` 或载荷。真正的契约在 Go 结构体和两端的字符串里。
- Agent 用注册令牌认证（`authorization` 元数据）。网关的 `Connect` 只校验令牌，不检查 Agent 版本。

### 类型字符串

**Agent 发给控制面的信封类型，共 16 种。**

| 类型 | 载荷 |
| --- | --- |
| `heartbeat` | `protocol.Heartbeat` |
| `metrics` | `protocol.Metrics` |
| `inventory` | `protocol.Inventory` |
| `event`、`gemini_event`、`claude_event` | `protocol.StreamEvent`，分别对应 Codex、Gemini、Claude |
| `claude_ownership_lost`、`claude_ownership_active`、`gemini_ownership_lost`、`gemini_ownership_active` | `protocol.GeminiOwnership`（四种共用） |
| `operation_started` | `protocol.OperationStarted` |
| `operation_result` | `protocol.OperationResult` |
| `operation_reconcile` | `protocol.OperationReconcile` |
| `agent_update_status` | 没有结构体，是一个 `map[string]string`，键为 `version` 和 `status` |
| `deployment_status` | `protocol.DeploymentStatus` |
| `session_tool_request` | `sessiontools.Call` |

**控制面发给 Agent 的非操作类型，共 2 种。** `keepalive`（`operation_id` 为空，载荷是 `protocol.GeminiKeepalive`）和 `session_tool_response`（载荷是 `sessiontools.Reply`）。除此之外的下行信封都是操作。

**操作类型。** Agent 能处理 53 个固定的类型，外加两个前缀族。

| 领域 | 类型 |
| --- | --- |
| Agent 与系统 | `agent.update`、`cli.install`、`inventory.scan`、`operation.interrupt` |
| Codex | `codex.turn.start`、`codex.turn.rewrite`、`codex.turn.steer`、`codex.turn.interrupt`、`codex.thread.fork`、`codex.thread.compact`、`codex.approval`、`codex.mcp.list`、`codex.models.list`、`codex.skills.list`、`codex.status.snapshot`、`codex.update`、`codex.credentials.configure` |
| Gemini | `gemini.turn.start`、`gemini.turn.rewrite`、`gemini.turn.steer`、`gemini.turn.interrupt`、`gemini.thread.fork`、`gemini.question.answer`、`gemini.skills.list`、`gemini.skills.manage`、`gemini.credentials.configure` |
| Claude | `claude.turn.start`、`claude.turn.steer`、`claude.turn.interrupt`、`claude.thread.fork`、`claude.question.answer`、`claude.skills.list`、`claude.credentials.configure` |
| 凭据与部署 | `credentials.configure`、`git.credentials.configure`、`deploy.start`、`deploy.rollback`、`deploy.container` |
| Git | `git.import`、`git.project.create`、`git.project.delete`、`git.workspace.clone`、`git.workspace.inspect`、`git.workspace.lifecycle`、`git.workspace.write`、`git.worktree.create`、`git.worktree.cleanup` |
| 工作区文件 | `workspace.files`、`workspace.file.preview`、`workspace.image.preview`、`workspace.file.write`、`workspace.changes`、`workspace.diff.preview` |

两个前缀族：`codex.goal.get`、`codex.goal.set`、`codex.goal.clear`（Agent 用 `strings.HasPrefix(kind, "codex.goal.")` 判断）；`deploy.container.<动作>`（Agent 和网关都识别这个前缀，但基线里控制面只入队不带后缀的 `deploy.container`，带后缀的形式只在一个测试里出现）。

**这些字符串在代码里的样子。**

- 只有 4 个常量：`protocol.ControlKindKeepalive`、`protocol.AgentKindOperationReconcile`、`store` 包里的 `gitProjectCreateKind` 和 `ProjectDeleteOperationKind`。其余全是字面量。
- 非测试 Go 代码里有 234 种带点号的双引号字面量。这个集合里除了操作类型和事件类型，还混有审计动作名、Git 子动作名（`branch.create`、`remote.add`）等别的标识。出现最多的文件：`agentgateway/gateway.go` 75 行、`agent/client.go` 59 行、`httpapi/resources.go` 55 行、`protocol/operation_reconcile.go` 23 行、`codexadapter/adapter.go` 22 行。
- `"claude.turn.start"` 作为 Go 字符串出现 15 次，另外在 SQL 文本里出现 12 次。
- `internal/store` 的 SQL 文本里嵌着 75 种、215 处单引号形式的类型。其中一部分出现在局部索引的条件里，例如 `events_collaboration_time_idx ... WHERE kind IN ('session.collaboration.request', ...)` 和 `operations_workspace_exclusive_write_unique ... AND kind NOT IN ('claude.turn.start', ...)`。查询里的类型如果改成绑定参数，数据库就无法确认查询条件落在索引条件之内，这些局部索引会失效。
- 有几处类型是拼出来的：`store/credential_profiles.go` 的 `scope + ".credentials.configure"`；网关的 `"operation." + result.Status`；`geminiadapter/steering.go` 的 `"gemini.steer." + kind`；`codexadapter/adapter.go` 的 `"codex." + strings.ReplaceAll(message.Method, "/", ".")`。最后一处意味着 Codex 的事件类型是开放集合，由 CLI 的通知方法名决定。
- 类型被持久化在 `agent_operations.kind` 和 `events.kind` 里，前端的 `realtimeScopesForEvent`（`web/src/App.tsx`）按前缀匹配事件类型来决定刷新范围。所以取值一个字符都不能变。
- 审计动作名是另一个命名空间（`Store.Audit` 的第三个参数），共 65 种，其中 10 种的取值恰好和操作类型相同，例如 `codex.turn.start`、`agent.update`、`workspace.file.write`。

### 分发

- **Agent 端。** `Client.handleOperation` 先做公共前置（去重、重放已保存的结果、自更新期间拦截、发送 `operation_started`），然后是一条 26 个分支的 `if / else if` 链，最后的 `else` 落到 `Client.execute` 的 `switch`（20 个 `case`）。`execute` 的 `default` 返回 `unsupported operation kind`，所以旧 Agent 收到不认识的操作会回一个失败结果，不会卡住。链之后是公共收尾：映射状态、裁剪结果、保存结果、发送 `operation_result`。
- **载荷解码。** Agent 里有 46 处 `json.Unmarshal(envelope.PayloadJSON, ...)`，各分支自己解码。没有使用 `DisallowUnknownFields`，未知字段被静默忽略；这正是新控制面能给旧 Agent 发带新字段的载荷的原因。
- **网关端。** `Gateway.handle` 是按信封类型分发的 `switch`（14 个 `case`），`default` 返回 `unsupported agent message kind`；调用方 `receiveWithTools` 只把错误记成一条警告日志，连接不断开。也就是说，旧控制面收到新 Agent 发来的未知信封类型会丢弃它。
- **入队。** `internal/store` 里有 29 处 `INSERT INTO agent_operations`，分布在 22 个文件，没有统一的入队函数。唯一的下发点是网关的 `flush`：读取待下发操作，解密载荷，发送，标记已下发。
- **没有线上格式测试。** 载荷结构体的字段名和 `omitempty` 改了，只有两端都跑起来才会发现。

### 能力判断

- `internal/buildinfo/version.go` 有 31 个 `Supports*` 函数，都是“Agent 版本不低于某个值”，其中 8 个还要求 CLI 版本。最老的门槛是 0.2.0（自更新），最新的是 0.3.06（会话协作）。完整清单见本文件末尾的“能力名清单”。
- `buildinfo` 之外有 86 处调用，分布在 25 个文件：`internal/httpapi` 42 处、`internal/store` 38 处、`internal/scheduler` 4 处、`internal/agent` 2 处。P2-02 会把会话相关的调用收进统一的能力解析函数，服务器级的调用（`httpapi/resources.go` 里的 `agent_update_supported`、`cli_management_supported` 等）不在 P2-02 的范围内。
- 控制面了解 Agent 的唯一途径是心跳里的版本号：`protocol.Heartbeat` 带 `agent_version` 和三种 CLI 的版本与就绪状态，`Store.Heartbeat` 把它们写进 `servers` 表和 `server_claude_cli` 表。
- Agent 升级是逐台手动触发的（`httpapi/agent_updates.go`），目标版本固定为控制面自己的版本。没有任何机制强制升级，旧 Agent 可以无限期在线。这就是门槛函数只增不减的原因。
- 用版本号推断能力有两个问题：一是每加一个功能就要加一个门槛函数并在各处调用；二是它表达不了“这台 Agent 版本够新，但这个功能在这台机器上不可用”。

## 目标

1. 操作类型和信封类型在 Go 代码里只有一处定义，写错名字在编译期就能发现。
2. Agent 在心跳里声明自己的能力，控制面按声明决定功能可用性；新增能力不再需要新增版本门槛函数。
3. 有一个明确的最低 Agent 版本。低于它的 Agent 能连上、能被升级，但不执行其他操作；高于它的历史门槛全部删除。
4. 每个操作类型的载荷都有线上格式夹具，字段改动由测试发现，而不是由线上故障发现。
5. 三种引擎的会话事件共用一种信封类型。

不在本阶段范围内：

- 改变任何已有类型字符串的取值或已有载荷字段的名字。
- 改用 protoc 生成的类型化消息。见总览的决策 D3，P4-04 完成后再评估。
- 改变传输方式、认证方式或操作的排队与重试语义。
- 给 Agent 上行消息加入新的投递保证。

## 设计

以下是方向性的草图，命名和文件位置以实现时的真实需要为准。

### 类型常量

放在 `internal/protocol`，因为控制面和 Agent 都依赖它，而它不依赖别的包。

```go
// internal/protocol/kinds.go
const (
    // 上行信封，沿用已有的 AgentKind 前缀
    AgentKindHeartbeat = "heartbeat"
    // 下行非操作信封，沿用已有的 ControlKind 前缀
    ControlKindSessionToolResponse = "session_tool_response"
    // 操作类型；前缀族单独命名
    OpClaudeTurnStart       = "claude.turn.start"
    OpCodexGoalGet          = "codex.goal.get"
    OpDeployContainerPrefix = "deploy.container."
    // 代码里会比较或发布的事件类型
    EventUserMessage = "user.message"
)
```

约定：

- 用无类型的字符串常量，不新建 `type OperationKind string`。信封结构体的字段类型不变，常量可以直接用在现有的比较、`switch` 和函数参数里，替换是逐处等价的。
- 取值由一份基准清单锁定：`internal/protocol/testdata/kinds.golden` 列出全部取值，测试断言常量集合与它完全一致且没有重复。清单在替换之前从基线代码生成，之后任何取值变化都会让测试失败。
- **SQL 文本里的类型保留为字面量。** 改成绑定参数会让局部索引失效，拼接常量又会让 SQL 难读。改用一个测试守住一致性：扫描 `internal/store` 和 `internal/agentgateway` 非测试源码里单引号形式的带点号标识，断言每一个要么是已定义常量的取值，要么在一份写明理由的白名单里（例如文件名 `compose.yaml`、Codex 快照类型 `models.list`）。
- **审计动作名不在范围内。** 它是另一个命名空间，即使取值和某个操作类型相同，也不要用操作类型常量替换 `Store.Audit` 的参数。
- **拼接出来的类型改为显式映射。** `scope + ".credentials.configure"` 改成一个按作用域返回常量的函数，不认识的作用域照旧报错。Codex 事件的 `"codex." + 方法名` 是开放集合，保持拼接，只给代码里实际比较的那几个取值定义常量。
- 测试代码里的字面量保留。测试断言的是线上取值，用字面量写更能防止常量被改错。
- 前端的字符串不在本阶段处理。

### 能力列表

Agent 在心跳里直接声明能力，控制面不再从版本号推断。

```go
type Heartbeat struct {
    // ...现有字段不变
    Capabilities []string `json:"capabilities,omitempty"`
}
```

- **能力名**是小写、带点号的字符串，例如 `claude.steer`、`workspace.file_write`。本文件末尾的“能力名清单”给出了 31 个现有门槛对应的建议名，由 P4-02 冻结。能力名一旦发布就不再改。
- **Agent 怎么算。** 只和 Agent 自身有关的能力是编译进去的固定列表。和 CLI 有关的能力（清单里“CLI 要求”一栏非空的 8 个）在每次心跳时根据实际探测到的 CLI 版本决定是否声明。这样“Agent 够新但 CLI 不够新”就由 Agent 如实表达，控制面不需要知道 CLI 的版本门槛。
- **存在哪里。** 新建以 `server_id` 为主键的附属表（建议名 `server_capabilities`），不往 `servers` 表加列。仓库里已有同样的做法（`server_claude_cli`、`server_metadata`）：附属表不影响旧版控制面读 `servers`，回滚更安全。心跳里没有能力列表时清空这一行，避免 Agent 降级后留下过期的声明。
- **怎么用。** P2-02 的能力解析函数增加一个输入：服务器声明的能力集合。有声明就按集合判断；没有声明（旧 Agent）就退回现有的版本门槛。服务器级的开关（`agent_update_supported`、`cli_management_supported` 等）也改为走同一个解析入口。
- **等价性测试。** 对当前版本的 Agent，在若干 CLI 版本组合下，“Agent 会声明的集合”和“按版本门槛推断出的集合”必须相同。这条测试保证上线能力列表不会改变任何功能的可用性。
- **此后的规则。** 新增能力只加一个能力名，由 Agent 声明；不再新增 `buildinfo.Supports*` 函数。控制面遇到不认识的能力名直接忽略。
- **反方向的声明。** 下行的 `keepalive` 载荷同样增加一个能力列表字段，表示控制面能接受什么。它每次连接都最先发送，旧 Agent 会忽略未知字段。P4-02 只加字段，第一个使用者是 P4-05。

### 最低版本

- `buildinfo` 增加 `MinimumAgentVersion`。取值由决策 D2 确定，默认是发布 P4-02 的那个版本。
- 低于最低版本的 Agent 仍然可以连接、上报心跳，界面上显示“需要升级”。控制面只向它下发 `agent.update`，其余操作一律以明确的错误结束，不让会话停在排队状态。
- **拦截点。** 入队分散在 29 处，没有统一入口，所以分两层：能力解析对低于最低版本的 Agent 返回空集合，现有处理器里的能力检查自然会拒绝大多数请求；网关的 `flush` 是唯一的下发点，在这里兜底，覆盖调度器、协作流程和升级前已经排队的操作。
- **时序。** `flush` 和接收心跳是并发的，数据库里的 `agent_version` 可能还是上一次连接留下的旧值。给连接对象加一个“本次连接已处理过心跳”的状态，在此之前不下发操作。Agent 每次连接都会立即发送心跳，等待很短。
- **保留的门槛。** `SupportsSelfUpdate`（0.2.0）不能删：低于它的 Agent 无法远程升级，需要保留“必须重新安装一次”的提示。其余不高于最低版本的门槛连同调用点全部删除，能力解析里“没有声明就按版本推断”的分支也一并删除。

### 载荷注册表与夹具

- `internal/protocol` 增加一张注册表：操作类型到载荷结构体（以及结果结构体，如果有）的映射。它首先服务于测试。
- **夹具**放在 `internal/protocol/testdata/wire/`，每个类型至少一个 JSON 文件，上行的 16 种信封同样处理。测试把夹具解码到注册的结构体（测试里开启 `DisallowUnknownFields`），再编码回去，与原文做语义比较。字段改名、删除、`omitempty` 变化都会在这里失败。
- **夹具只增不改。** 修改或删除已有夹具等于破坏协议，提交信息里必须说明它对最低版本的 Agent 为什么安全。
- 生产代码的解码保持宽松，不开启 `DisallowUnknownFields`，否则新控制面无法给旧 Agent 发带新字段的载荷。
- 加密的载荷（以 `v1:` 开头）在夹具里用解密后的明文形式。
- **Agent 端的分发表。** P3-04 的第三个提交已经把每个分支提取成具名方法。P4-04 在此基础上把 `if / else if` 链和 `execute` 的 `switch` 换成一张“操作类型到处理函数”的表，处理函数有统一的签名，返回结果数据、需要重启时的路径、是否刷新清单和错误。公共前置和公共收尾留在 `handleOperation` 里。
- 网关的 `handleOperationResult` 不改成表。它的各段判断不是互斥的（见 P3-04 的风险说明），一对一的表表达不了。

### 会话事件信封

- 新增上行类型 `session_event`，载荷是现有的 `StreamEvent` 加一个 `engine` 字段。`StreamEvent` 内部的事件类型（`claude.turn.completed` 等）不变，`events` 表和前端不受影响。
- 四种所有权信封同样收敛为一种带 `engine` 和 `active` 的 `session_ownership`。
- **兼容方式。** 旧控制面会丢弃不认识的信封类型，所以 Agent 只有在控制面通过 `keepalive` 声明能接受之后才使用新类型，否则继续发旧类型。控制面在下一次最低版本基线前移之前，同时接受新旧两种。
- 网关把三段事件处理里相同的步骤（解码、解析线程、校验服务器归属、脱敏、发布）提取出来，引擎特有的部分（Codex 的用量换算、审批、轮次收尾；Gemini 和 Claude 的操作号比对与事件类型校验）保留为各自的函数。

## 步骤

### P4-01 操作与事件类型常量化

可以按包分成多个提交。每个提交都保持可编译、测试通过。

**做法**

1. 先生成基准清单：从基线代码里取出“现状”一节列出的全部操作类型、信封类型，以及代码里会比较或发布的事件类型，写进 `internal/protocol/testdata/kinds.golden`。这个提交只加清单文件和读取它的测试骨架。
2. 新增 `internal/protocol/kinds.go`，定义全部常量，命名规则见“设计”。已有的 `ControlKindKeepalive`、`AgentKindOperationReconcile` 保持原名；`store` 包里的 `gitProjectCreateKind`、`ProjectDeleteOperationKind` 改为引用新常量。`AgentRestartSafeKinds`、`CanInterruptOperation` 里的字面量换成常量。
3. 逐包替换 Go 代码里的字面量，建议顺序：`protocol`、`agent`、`agentgateway`、`store`、`httpapi`、`scheduler` 与 `sessionservice`、三个适配器。只替换作为操作类型、信封类型、事件类型使用的字面量。
4. 不替换的东西：SQL 文本里的类型；`Store.Audit` 的动作名参数；Git 子动作名（`branch.create` 等）；测试代码。
5. 把拼接类型改成显式映射：凭据作用域到操作类型的函数。`deploy.container.` 前缀族用具名前缀常量，判断逻辑不变。
6. 新增 SQL 一致性测试，做法见“设计”。
7. 改动落在 Agent 依赖的包里的提交，同一提交里升级 `buildinfo.Version`。

**验收**

- 基准清单测试通过：常量取值集合与清单完全一致。
- 现有测试不需要修改任何期望值就能通过，包括 `make test-postgres`。
- 运行下面的命令，把剩余的字面量按“审计动作、Git 子动作、常量定义本身、其他”分类贴到“执行记录”。“其他”一类应当为空或逐条说明理由。

```bash
grep -rnoE '"[a-z_]+(\.[a-zA-Z_]+)+"' internal cmd --include=*.go --exclude=*_test.go \
  | grep -v 'internal/protocol/kinds.go'
```

**风险与回滚**

- 这一步会改到几乎所有引用操作类型的后端文件，会让 P3 的纯移动校验和发生变化，也容易和 P2-03 至 P2-05 冲突。不要和它们并行。P3 各步骤本来就要求以“本步骤开始前”的校验和为准，先后顺序不影响 P3 的做法。
- 最容易犯的错是把取值相同的审计动作名也替换掉。两者今后可能各自演变，必须分开。
- 只是等价替换，按提交回滚即可。

### P4-02 心跳携带能力列表

依赖 P1 完成（要加表）和 P2-02（要有统一的能力解析入口）。

**做法**

1. 冻结能力名：核对本文件末尾的“能力名清单”，把最终名字定义为 `internal/protocol` 里的常量，并更新清单。
2. Agent 端：`Heartbeat` 增加 `capabilities` 字段。`enqueueHeartbeat` 里组装列表，固定能力来自一张表，和 CLI 有关的能力根据当次探测到的 CLI 版本决定。CLI 版本的解析沿用 `buildinfo` 里现有的写法（Claude 的版本串取第一个空格之前的部分）。
3. 新迁移：建立附属表。按 P1-04 的规范编写，两种数据库都要有，并给 P1-01 的结构一致性测试增加夹具。
4. `Store.Heartbeat` 写入能力列表；心跳里没有列表时清空。这个函数目前是两条不在同一事务里的语句，新增写入后把三条放进同一个事务。
5. 能力解析函数增加“服务器声明的能力集合”这个输入，逻辑见“设计”。查询线程和服务器的函数相应地多读这一列。
6. `httpapi/resources.go` 里计算服务器级开关的那一段，以及其余不属于会话的调用点（工作区文件写入、操作中断、Agent 更新、协作），改为走能力解析入口。
7. `keepalive` 载荷增加控制面能力列表字段，暂时为空。
8. 测试：等价性测试；旧 Agent（心跳里没有列表）走版本推断的测试；Agent 降级后声明被清空的测试。
9. 同一提交里升级 `buildinfo.Version`。在 `docs/CLAUDE.md` 和 `docs/GEMINI.md` 的“兼容版本”一节注明：从这个版本起以 Agent 声明的能力为准。

**验收**

- 契约测试通过；接口响应里各个 `*_supported` 字段的取值与改动前一致，前端不需要改动。
- `buildinfo.Supports*` 的调用点只剩能力解析内部和 Agent 自己。用 `grep -rn 'buildinfo\.Supports' internal cmd --include=*.go --exclude=*_test.go` 核对，把结果贴到“执行记录”。
- 在 P0-07 的跨进程验收测试里增加一条用例：真实的 Agent 连上之后，通过接口读到的各个 `*_supported` 字段与这个版本应有的取值一致。它验证的是“Agent 声明、心跳上报、控制面入库、接口读出”的整条路径。
- 在一台真实服务器上升级 Agent 后，确认附属表里出现了这台服务器的能力列表，并且各功能入口的可用状态与升级前一致。这一步需要用户配合或授权；做不了时在“执行记录”里注明。

**风险与回滚**

- 迁移只新增表，属于“扩展”。控制面回滚到旧版本后，旧版本不读这张表，行为等同于改动前。
- 新控制面遇到旧 Agent 走版本推断，旧控制面遇到新 Agent 会忽略未知的心跳字段。两个方向都兼容。
- 风险集中在等价性：某个能力在 Agent 的声明逻辑和控制面的版本推断之间不一致，会让功能入口意外出现或消失。等价性测试必须覆盖清单里的每一行。

### P4-03 最低 Agent 版本基线，删除历史门槛

受决策 D2 约束。前置：P4-02 已随某个版本发布，并且所有未吊销的服务器都已升级到基线版本或更高。

**做法**

1. 确认前置。列出所有未吊销服务器的 `agent_version` 和 `last_seen_at`，逐台与基线版本比较（版本号不能按字符串比较，用 `buildinfo` 的比较函数）。长期离线的服务器由用户决定：吊销，或接受它重新上线后必须先升级。把核对结果写进“执行记录”。这一步需要用户配合或授权。
2. 先补特性测试，单独提交：让各类还没下发的操作在 `flush` 里以失败结束，断言会话、工作区和相关记录的状态是一致的。现有的 `agentgateway/decryption_failure_test.go` 走的是同一条失败路径，但只覆盖了部署和凭据类操作，没有覆盖三种引擎的发起轮次、追加、分叉，也没有覆盖 Git 和工作区操作。测试暴露出收尾不完整的类型时，登记到总览的“发现的问题”，修复单独提交，修完再继续。
3. 增加 `buildinfo.MinimumAgentVersion`。
4. 网关：连接对象记录“本次连接已处理过心跳”；在此之前 `flush` 不下发。对低于最低版本的 Agent，只下发 `agent.update`，其余操作以明确的错误结束（文案说明需要先升级 Agent）。
5. 能力解析：低于最低版本时只保留自更新能力。服务器的接口响应增加一个“需要升级”的标志，服务器列表和详情页显示它，并保留升级入口。
6. 删除不高于最低版本的全部 `Supports*` 函数、对应的 `Minimum*Version` 常量和调用点，删除能力解析里按版本推断的分支。`SupportsSelfUpdate`、`UpdateAvailable` 保留，原因见“设计”。
7. Agent 端 `client.go` 里对自身版本的门槛判断删除，CLI 版本的判断并入能力声明的计算。同一提交里升级 `buildinfo.Version`。
8. 更新文档：`docs/CLAUDE.md` 和 `docs/GEMINI.md` 的“兼容版本”表格改为只写最低版本和 CLI 要求；`docs/PRODUCTION_DEPLOYMENT.md` 的“更新、回滚与备份”一节写明“升级控制面之前，先把所有 Agent 升级到某个版本”。

**验收**

- 新增测试覆盖：低于最低版本的 Agent 能连接、心跳被记录、`agent.update` 被下发；发给它的轮次操作以明确的错误结束，会话回到可操作的状态。
- `grep -c '^func Supports' internal/buildinfo/version.go` 的结果从 31 降到个位数，把数字写进“执行记录”和总览的“基线数字”对比。
- 通用验收全部通过。

**风险与回滚**

- 被遗忘的旧服务器在基线之后重新上线，它的所有操作都会失败，直到升级。这是预期行为，但界面必须让原因一目了然。
- 这一步不改数据。回滚就是回退提交，旧门槛函数随之恢复。
- 如果第 1 步发现有服务器无法升级（例如无法自更新的极老版本），先停下来由用户决定，不要为了推进而降低基线的含义。

### P4-04 载荷注册表与线上格式夹具

依赖 P4-01（类型常量）和 P3-04（分支已提取成具名方法）。分两个提交。

**提交一：注册表与夹具，不改产品行为**

1. 在 `internal/protocol` 增加操作类型到载荷结构体、结果结构体的注册表，以及上行信封类型到载荷结构体的注册表。`agent_update_status` 目前没有结构体，为它补一个字段相同的结构体。
2. 为每个类型写夹具。夹具内容要来自真实的入队路径：优先从现有 Store 和 HTTP 测试里截取实际写入 `agent_operations.payload` 的 JSON，再补上可选字段都有值的变体。不要只凭结构体定义手写。
3. 测试：每个 `Op*` 常量和每个上行信封常量都有注册项和至少一个夹具；每个夹具都能严格解码并无损地编码回去。
4. 把“夹具只增不改”的规则写进 `internal/protocol` 的包注释和夹具目录下的说明文件，让改协议的人在动手的地方就能看到。

**提交二：Agent 端分发表**

1. 定义统一的处理函数签名和返回值，把 P3-04 提取出来的具名方法登记进一张表。两个前缀族显式登记：三个 `codex.goal.*` 常量逐个登记，`deploy.container.` 用前缀匹配。
2. `handleOperation` 缩减为公共前置、查表、公共收尾。查不到时返回与现在相同的错误文本（`unsupported operation kind`）。
3. 46 处各自的 `json.Unmarshal(envelope.PayloadJSON, ...)` 换成一个公共的泛型解码函数，保持宽松解码。
4. 测试：每个 `Op*` 常量在表里都有处理函数，表里没有常量之外的键。
5. 同一提交里升级 `buildinfo.Version`。

**验收**

- 夹具测试、注册完整性测试、Agent 包的全部测试通过。
- 故意改一个载荷字段的 JSON 名，确认夹具测试失败；改回去。把这次验证记到“执行记录”。
- `Client.handleOperation` 和 `Client.execute` 里不再有按操作类型写的分支；`execute` 可以删除或只剩查表。
- 在一台真实服务器上用新 Agent 各执行一次：一轮会话、一次 Git 状态读取、一次文件预览。这一步需要用户配合或授权；做不了时在“执行记录”里注明。

**风险与回滚**

- `if / else if` 链目前有顺序语义，查表没有。换成表之前先确认没有一个类型会同时命中两个分支；前缀族是唯一需要留意的地方。
- 有的分支带着专门的状态映射（例如三种轮次类型在取消时映射为 `interrupted`）。这些逻辑在公共收尾里，换表时不要动。
- 两个提交各自可回滚。提交一不影响产品行为。

**说明**

完成后回头评估决策 D3：如果夹具测试已经足以防住协议回归，就把结论写进“决策记录”并关闭；否则说明还缺什么。

### P4-05 统一会话事件信封

依赖 P4-02（控制面能力声明的字段）和 P2-09（Agent 端发出事件的位置已经收敛）。

**做法**

1. 定义常量 `session_event`、`session_ownership` 和对应的载荷结构体，补夹具。
2. 控制面先行：网关接受新类型，分发到与旧类型完全相同的处理逻辑；把三段事件处理的公共步骤提取出来。在 `keepalive` 里声明能接受新类型。
3. Agent 端：发出会话事件的统一入口根据“本次连接的控制面是否声明过”选择类型，默认用旧类型。同一提交里升级 `buildinfo.Version`。
4. 旧的三种事件信封和四种所有权信封继续接受。它们的删除不属于本步骤：等最低版本基线下一次前移、所有 Agent 都只发新类型之后再做，届时在总览里新增一个步骤。

**验收**

- 网关测试对每种引擎用新旧两种信封各跑一遍同一组事件用例，结果逐项相同。
- 混合版本测试：控制面没有声明时，新 Agent 发的是旧类型；声明之后发新类型。
- P0-07 的跨进程验收测试在新信封下通过，三种引擎的事件都能从事件接口读到。
- 在一台真实服务器上，三种引擎各完成一轮对话，事件顺序和内容正常。这一步需要用户配合或授权；做不了时在“执行记录”里注明。

**风险与回滚**

- 信封在入队时就已经封装好，并且会跨重连重发。控制面回滚到不认识新类型的版本后，Agent 队列里已经按新类型封装的少量事件会被丢弃（旧控制面只记一条警告）。在升级说明里写明这个窗口；要彻底消除它，可以在发送时而不是入队时决定类型，由实现时权衡。
- 事件的顺序由同一个出站队列保证，换类型不改变顺序。

**说明**

这一步单看收益不大：网关的 `handle` 少几个分支，Agent 少两处入队调用。它的价值在于今后增加引擎时不需要新增信封类型。如果 P2-09 完成后评估认为不值得承担兼容窗口的风险，可以把它改为 `已取消` 并写明原因。

## 阶段完成标准

- 进度总表里 P4 的步骤全部 `已完成` 或 `已取消`。
- 在非测试 Go 代码里，操作类型和信封类型只以常量形式出现，SQL 文本除外。
- 新增一种操作类型时需要做的事可以列成一份固定的清单：加常量和基准清单、加载荷结构体和夹具、登记 Agent 处理函数、（需要时）加能力名。把这份清单写进 `internal/protocol` 的包注释。
- `buildinfo` 里按版本号判断能力的函数只剩说明了保留原因的少数几个。

## 能力名清单

由 P4-02 核对并冻结。下表是基线提交里全部 31 个门槛和建议的能力名；“CLI 要求”非空的由 Agent 按实际 CLI 版本决定是否声明。

| 建议能力名 | 现有门槛函数 | 最低 Agent | CLI 要求 |
| --- | --- | --- | --- |
| `agent.self_update` | `SupportsSelfUpdate` | 0.2.0 | |
| `codex.cli_update` | `SupportsCodexUpdate` | 0.2.9 | |
| `deploy.rollback_exact` | `SupportsExactDeploymentRollback` | 0.2.45 | |
| `codex.steer` | `SupportsCodexSteer` | 0.2.51 | |
| `workspace.file_write` | `SupportsWorkspaceFileWrite` | 0.2.56 | |
| `codex.fork_at_message` | `SupportsMessageFork` | 0.2.59 | |
| `gemini.sessions` | `SupportsGemini` | 0.2.60 | |
| `cli.manage` | `SupportsCLIManagement` | 0.2.61 | |
| `gemini.credentials` | `SupportsGeminiCredentials` | 0.2.63 | |
| `gemini.fork` | `SupportsGeminiFork` | 0.2.68 | |
| `gemini.thinking` | `SupportsGeminiThinking` | 0.2.70 | |
| `gemini.yolo` | `SupportsGeminiYolo` | 0.2.72 | |
| `gemini.input` | `SupportsGeminiInput` | 0.2.73 | |
| `gemini.steer` | `SupportsGeminiSteering` | 0.2.79 | Gemini CLI 0.66.0 及以上 |
| `gemini.skills` | `SupportsGeminiSkills` | 0.2.83 | Gemini CLI 0.66.0 及以上 |
| `gemini.plan` | `SupportsGeminiPlan` | 0.2.83 | Gemini CLI 0.66.0 及以上 |
| `gemini.goal` | `SupportsGeminiGoal` | 0.2.84 | Gemini CLI 0.66.0 及以上 |
| `gemini.rewrite` | `SupportsGeminiRewrite` | 0.2.85 | |
| `gemini.steer_images` | `SupportsGeminiImageSteering` | 0.2.88 | Gemini CLI 0.66.0 及以上 |
| `gemini.skills_manage` | `SupportsGeminiSkillManagement` | 0.2.88 | Gemini CLI 0.66.0 及以上 |
| `claude.sessions` | `SupportsClaude` | 0.2.90 | |
| `claude.manage` | `SupportsClaudeManagement` | 0.2.91 | |
| `claude.effort` | `SupportsClaudeEffort` | 0.2.92 | |
| `claude.input` | `SupportsClaudeInput` | 0.2.93 | |
| `claude.skills` | `SupportsClaudeSkills` | 0.2.94 | |
| `claude.steer` | `SupportsClaudeSteering` | 0.2.95 | Claude CLI 2.1.286 及以上 |
| `operation.interrupt` | `SupportsOperationInterrupt` | 0.2.96 | |
| `claude.rewrite` | `SupportsClaudeRewrite` | 0.2.97 | |
| `claude.questions` | `SupportsClaudeQuestions` | 0.2.98 | Claude CLI 2.1.286 及以上 |
| `credentials.scoped` | `SupportsScopedCredentials` | 0.3.02 | |
| `session.collaboration` | `SupportsSessionCollaboration` | 0.3.06 | |

说明：

- 上表的门槛函数名和取值以基线提交的 `internal/buildinfo/version.go` 为准，P4-02 动手时逐行核对一次。
- 有些文档里写了最低版本、但代码里没有门槛函数的能力（例如 `docs/CLAUDE.md` 里的“上下文压缩事件”），不需要能力名：它们不影响控制面的任何判断。
- `operation.interrupt` 这个能力名和同名的操作类型是两个命名空间，取值相同只是巧合。

## 执行记录

按时间顺序追加，每条包含日期、步骤编号、会话标识、做了什么、验收结果、遗留事项。

（暂无）
