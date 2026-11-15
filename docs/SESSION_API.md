# 统一会话 API

M3 增加 `/api/sessions`。旧 `/api/threads`、`/api/gemini/threads`、`/api/claude/threads` 保留。所有新接口沿用登录认证和写入时的 CSRF 校验；操作类型、审计动作和实时事件名称沿用原值。

## 列表和详情

`GET /api/sessions?engine=&archived=&q=&limit=&cursor=` 返回：

```json
{"items": [], "next_cursor": null, "groups": []}
```

- `engine` 省略时混合列出三种引擎，指定时只能为 `codex`、`gemini`、`claude`。
- `archived` 默认/`false` 为活动会话，`true` 为归档，`all` 为两者。
- `q` 是不区分大小写的字面量子串，匹配工作区显示名、项目显示名、路径、会话标题、服务器名称、Wio ID 或原生 ID；先筛选，再分页。
- `limit` 默认为50，上限100；使用上次响应的 `next_cursor` 获取下一页，结束时为 `null`。游标绑定 `engine`、`archived`、`q`；更换筛选时清空游标。分页没有 `offset` 参数。
- 排序依次为项目置顶标志/时间、会话置顶标志/时间、更新时间、引擎和ID，全部降序。游标保存排序值，删除上一页最后一条会话不会使后续页移位。
- `groups` 包含筛选后的全部匹配工作区，独立于当前页。每项为 `workspace_id`、`matched_count`、`latest_updated_at`、`project_pinned_at`。未匹配的空工作区由前端与 `/api/workspaces` 合并。

列表、`GET /api/sessions/{engine}/{threadID}`、创建和更新响应都包含统一公共字段：引擎和ID，工作区/项目/服务器ID，路径、名称、服务器状态，标题/生命周期，原生ID，model/mode/effort，操作ID，项目/会话置顶、项目隐藏、归档及创建/更新时间、`capabilities`。原生旧字段也保留，包括各引擎已有的 `*_supported`、原生状态/就绪字段等。能力只表示版本支持，在线状态和原生就绪状态须分别判断。

## 操作

| 方法与路径（相对 `/api/sessions`） | 用途 |
| --- | --- |
| `POST /{engine}` | 创建 |
| `PATCH /{engine}/{threadID}` | 标题、置顶、归档/恢复 |
| `DELETE /{engine}/{threadID}` | 删除 |
| `POST /{engine}/{threadID}/turns` | 发起轮次 |
| `POST /{engine}/{threadID}/events/{eventID}/rewrite` | 编辑重发 |
| `POST /{engine}/{threadID}/steer`、`/interrupt`、`/confirm-stopped` | 追加、中断、确认停止 |
| `POST /{engine}/{threadID}/fork`、`/fork-worktree` | 原生续聊与工作树续聊 |
| `POST /{engine}/{threadID}/questions/{requestID}/answer` | Gemini/Claude 的问题回答 |
| `GET /{engine}/{threadID}/usage` | 原生会话用量 |

动作请求沿用原生字段与验证，例如 Codex/Gemini 使用 `approval_mode`，Claude 使用 `permission_mode`；effort 分别是 `reasoning_effort`、`thinking_level`、`effort`。除统一的创建/详情/更新读取结果外，动作成功响应保留原格式；原生能力的请求参数由引擎模块处理。Codex 的目标读取/设置/清除/刷新、压缩、状态读取/刷新，以及 Gemini 目标读取/控制，也在 `/{engine}/{threadID}` 下提供对应的 `goal`、`goal/refresh`、`compact`、`status`、`status/refresh`、`goals/{goalID}/{action}` 路径。

没有对应动作或版本不支持时，新路由统一返回 HTTP409：

```json
{"code":"CAPABILITY_UNSUPPORTED","error":"session action is not supported"}
```

旧路由继续保留原错误、字段和成功响应，包括 Codex 数组/分页对象两种列表形状、旧 `archived` 语义及各引擎不同的错误文本。

## 事件窗口

`GET /api/sessions/{engine}/{threadID}/events` 对三引擎统一支持 `after`、`before`、`limit` 和 `view=raw`。`after` 与 `before` 互斥且为非负序号；省略时返回最近窗口。默认500条，最多1000条，按序号升序返回。

Codex 默认保留已有对话事件投影，`view=raw` 返回完整原生事件；Gemini/Claude 返回原生事件。返回的 `sequence` 可继续作为游标。旧事件路由仍保持其原有默认窗口和参数验证行为。
