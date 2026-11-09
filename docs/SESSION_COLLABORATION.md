# 内置会话协作

Wio 为自己管理的 Codex、Claude、Gemini 进程自动注入会话协作工具，无需填写 MCP 地址或密钥。创建的目标是正式 Wio 会话，用户可以打开、接管、继续或归档。

## 开始使用

1. 升级控制端和参与协作的 Agent 到 **0.3.06 或更新版本**。
2. 打开任一会话，进入对话顶部的“会话协作设置”，开启“允许模型创建和管理子会话”。
3. 按需授权其他工作区、独立 worktree、跨模型执行权限、自动续聊和进一步委派。跨模型权限由用户选择，执行时还会检查它是否超过来源会话的权限。
4. 正常发送任务，例如：“新建 Claude 会话检查测试，使用独立 worktree，不提交；完成后让我查看结果。”

默认使用当前项目、服务器和工作区，沿用来源模型类型及执行设置。仅创建空会话不会调用模型。跨模型会按照目标服务器现有模型默认规则选择模型。`wio_create_session` 与 `wio_fork_session` 可选传入 `model`（模型 ID 或别名）和 `effort`（思考等级）覆盖默认值：Codex 为 `none`/`minimal`/`low`/`medium`/`high`/`xhigh`/`max`/`ultra`，Claude 为 `low`/`medium`/`high`/`xhigh`/`max`（模型可带 `[1m]`），Gemini 为 `minimal`/`low`/`medium`/`high` 且需明确指定支持思考等级的 Gemini 3 模型（`auto` 与 2.x 不支持）。只改模型时不沿用原思考等级；显式传入的不合法取值返回 `INVALID_ARGUMENT`。之后的续聊沿用子会话保存的设置。

共享工作区共享文件修改。独立 worktree 隔离 Git 工作目录，仍遵循 Agent 现有的文件与系统访问权限。

## 工作流程

支持创建、发消息、查询状态和消息、运行中补充指令、等待、读取结果、中断、修改标题/置顶、归档及恢复。入口卡片提供父子会话双向链接、请求状态、失败原因、共享目录提示和未读结果。会话被删除后保留来源记录。

原生分叉用于同类模型，未指定时默认使用被分叉会话的模型和工作区；跨模型需要明确选择上下文交接并提供交接说明，不会额外调用模型偷偷生成摘要。原生分叉遵循现有各 CLI 的历史边界限制：Codex 选择已结束轮次的最终回复，Claude 使用现有快照边界，Gemini 使用已有的历史分叉实现。Codex 指定消息边界也可与新 worktree 组合。

来源正在运行时，`after_turn` 会持久化“本轮结束后分叉”请求。模型应结束当前轮次，随后查询原请求，不能在阻止来源结束的同时等待该分叉。在自己的工作区里新建独立 worktree 子会话时，若写锁只由当前轮次持有，会立即创建 worktree（不占用来源工作区写锁，但会阻止对来源工作区的移动、复制和删除），因此可以在本轮内等待子会话结果；有其他会话占用工作区时仍排队。共享目录中的 Claude/Gemini 子会话仍需当前轮次结束才开始，Codex 子会话不受影响。worktree 准备及共享工作区排队也是持久化流程；控制端重启或来源 Agent 离线后仍会继续协调，目标 Agent 离线时操作保留在队列。

父会话默认收到页面通知。自动续聊需要设置授权，并在具体创建/分叉请求中选择 `auto_continue`。只有父会话空闲、未归档且授权有效时才排队；同一目标操作只续聊一次，与用户手动发送使用同一原子队列入口。撤销或修改授权会使原有待执行授权失效，已经提交的独立任务可通过中断停止。

## 工具协议

| 工具 | 用途 |
| --- | --- |
| `wio_get_context` | 当前会话、项目、工作区、授权及限制 |
| `wio_list_workspaces` | 授权范围内的工作区 |
| `wio_list_sessions` | 授权范围内的基本会话列表，可分页/过滤 |
| `wio_get_session` | 真实状态和会话链接 |
| `wio_read_messages` | 按事件序号分页读取用户、模型及工具事件 |
| `wio_create_session` | 创建空会话或附带第一条任务 |
| `wio_fork_session` | 原生分叉、结束后分叉或明确上下文交接 |
| `wio_send_message` | 发起后续轮次；忙碌时显式选择排队 |
| `wio_steer_session` | 在模型/Agent 支持时追加运行中指令 |
| `wio_wait_sessions` | 有界等待完成或等待用户处理 |
| `wio_get_result` | 按实际操作编号读取轮次状态、输出和证据 |
| `wio_interrupt_session` | 中断一个目标，不自动级联停止子任务 |
| `wio_update_session` | 标题和置顶 |
| `wio_archive_session` | 归档空闲会话 |
| `wio_restore_session` | 恢复归档会话 |
| `wio_get_request` | 根据原始请求编号对账 |

所有有副作用的请求都必须提供稳定的 `request_id`。同一来源会话重试相同编号与参数，返回原结果；编号相同而参数不同会返回 `IDEMPOTENCY_CONFLICT`。超时或断线后先查询原请求，不能换新编号重复创建任务。

创建示例：

```json
{
  "request_id": "review-tests-1",
  "engine": "claude",
  "directory_mode": "worktree",
  "prompt": "检查测试失败的原因，不提交修改",
  "auto_continue": false
}
```

工具身份由 Agent 绑定到当前 Wio 会话和实际轮次，调用参数不能选择来源身份。会话使用 `{ "engine": "claude", "id": "Wio 会话编号" }` 标识。请求编号、会话编号和执行操作编号分别返回。

请求状态包含 `accepted`、`preparing`、`queued`、`running`、`waiting_user`、`succeeded`、`failed`、`cancelled`。无法确认响应时返回 `unknown` 和原请求对账提示；执行记录仍存在时，后续原生事件可恢复请求状态。`wio_get_result` 单独返回实际操作状态、操作结果、指定轮次的事件，以及 `turn_status`、`turn_ended`。Codex 的启动操作成功只表示已接受任务，回合是否结束由原生事件及会话状态确认；模型文本声称完成不代表已经验证。用户/助手/工具信息保留原始事件种类，便于核对测试输出等证据。

常见错误码：`OUT_OF_SCOPE`、`SESSION_BUSY`、`SESSION_NOT_RUNNING`（中断未运行的会话）、`SOURCE_BUSY`、`CAPABILITY_UNSUPPORTED`、`REQUEST_PENDING`、`WAIT_DEPENDENCY`、`PERMISSION_MAPPING_REQUIRED`、`DELEGATION_LIMIT`、`PREPARATION_FAILED`。

## 范围与运行限制

- 默认只能创建/管理子会话，其他工作区需单独授权，本项目其他 Agent 的已有会话可通过一个开关统一授权，开启后同时视为授权本项目的全部工作区（包括 worktree 工作区，因此可原生分叉 worktree 子会话；不继承给子会话，执行权限仍不得超过来源会话）；本版本的额外范围限定在本项目，可选择本项目其他服务器上的工作区。
- 同时活动的子会话最多 2 个，已删除的子会话不占用名额；根任务累计创建的会话数量不设上限（`wio_get_context` 的 `limits.total_sessions` 为 `null`）。
- 进一步委派默认关闭，开启后最多 2 层；父授权撤销或版本变化会使继承授权失效。
- 单次等待最多 30 秒、最多 8 个会话，持久化等待图会拒绝循环依赖；等待不占用 Agent 接收循环或长事务。
- 自动续聊每个根任务最多 10 次，默认关闭。
- 不提供模型删除会话或“确认远端进程已停止”的工具；保留现有用户确认和运行丢失规则。

## 接入与生命周期

Agent 在回环地址创建带随机授权的进程级 HTTP MCP，仅通过现有已认证 gRPC 通道访问控制端。长期注册令牌不会传给模型进程。每次业务调用在控制端重新校验范围、当前轮次和授权版本。

Codex 在独立 app-server 的启动参数中注入；同一会话续聊时更新当前轮次绑定，进程退出、更新或空闲回收时关闭入口。Claude 使用进程级 `--mcp-config` 及精确工具白名单。Gemini 将协作服务作为额外 ACP MCP 服务，与原有提问、图片和目标服务共存。均不写用户全局 MCP 配置；桥接不可用时普通会话仍可使用。

本机真实 CLI 验证使用 Codex `0.167.0-alpha.3.2`、Claude Code `2.1.161`、Gemini CLI `0.66.0`。Codex 验证了无模型调用的工具发现；Claude/Gemini 通过隔离配置及本机模拟模型接口验证了工具发现、授权调用及结果回传。其他 CLI 版本仍需按各版本发布流程验证，运行中追加指令沿用现有能力门槛。

## 验证与发布

业务测试覆盖三种来源 × 三种目标，在共享工作区与独立 worktree 两种模式下检查创建、去重、首条任务、授权与恢复。另覆盖撤销继承授权、自分叉等待依赖、等待环、排队取消、历史轮次结果、未读通知及根任务续聊上限。数据库覆盖 SQLite 和 PostgreSQL。

```powershell
go test ./...
go test -tags postgresintegration ./internal/store -run TestPostgresMigrationAndStorage -count=1
# web 目录
npm test
npm run build
npm run e2e
```

真实 CLI 测试是显式开启的集成测试：`WIO_COLLABORATION_LIVE_CODEX`、`WIO_CLAUDE_TEST_EXECUTABLE`、`WIO_GEMINI_V066_CLI` 分别指向对应可执行文件。测试只使用临时目录和本机服务。

此代码变更不执行生产发布。发布时应先备份数据库、更新控制端，再升级测试 Agent，使用专用会话验证跨模型 worktree 委派、结果回传、授权自动续聊、旧 Agent 升级发现和重启恢复，随后逐步开放。关闭授权即可停止新的协作调用，普通会话不受影响。
