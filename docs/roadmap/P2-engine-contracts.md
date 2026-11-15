# P2-01 引擎行为与持久化字段清单

返回 [P2](P2-session-kernel.md)。基线 `be2c2d1`，只记录当前行为；“偶然”表示待统一的实现差异，旧接口在 P2-05 仍保留它。线上操作名、事件名和 JSON 字段不可改名。

## 公共约束

三种创建均要求 ready 工作区和未撤销服务器；离线仍可创建。成功 201，返回各自线程对象，初始 idle。缺失工作区 400 `workspace not found`；其他 Store 错误见文末映射。所有旧路由均经过原认证和 CSRF 中间件。非法 JSON / 未知字段均 400 `invalid JSON request`。

下表“写锁”指 `agent_operations.workspace_write` 和工作区排他条件，不是短期数据库行锁。操作提交、线程认领、事件、定时执行关联仍须在同一原事务中完成。Gemini/Claude 的 interrupted 若关联操作仍活动，不能当作已安全停止。

## 操作清单

| 操作 | Codex | Gemini | Claude | 差异性质与处理 |
| --- | --- | --- | --- | --- |
| 创建 | workspace_id；兼容 title 但忽略，默认 New session | workspace_id/model/approval_mode/thinking_level；默认 auto/default | workspace_id/model/permission_mode/effort；默认服务器凭据模型或 sonnet/default | 有意：模型/模式字段；均不占写锁，无基础 Agent 门槛 |
| 发起 | prompt/images/model/reasoning_effort/approval_mode/request_id/edit_event_id；202 `{operation_id}` | prompt/images/model/approval_mode/thinking_level/goal；202 `{ok:true}` | prompt/images/model/permission_mode/effort；202 `{ok:true}` | 有意：CLI 输入；偶然：响应与长度/校验次序。idle→queued；Agent 接受后 running；Codex 写锁 0，另两者 1，保留 |
| 编辑重发 | 路由 eventID 或 edit_event_id；冲突目标 400；无效目标 400 | eventID；无原生 rewind 边界 409；重发暂不改历史 | eventID；UUID 消息边界和已有原生会话；无效目标 409；暂不改历史 | 有意：原生历史能力；偶然：错误码。均 queued；锁沿用发起；原生加载成功才提交历史裁剪 |
| 运行中追加 | prompt/images/request_id；按活动 native turn 定位 | prompt/images/operation_id/UUID request_id；要求 steer.ready | ClaudeSteerCommand，含 prompt/images/operation_id/request_id；要求 ready 状态匹配 | 有意：原生 turn 与 operation 定位，Gemini/Claude 幂等；202 `{operation_id}`；全部写锁 0，不切换当前生命周期 |
| 中断 | queued 直接取消，202 `{operation_id,cancelled:true}`；已交付则精确原生 turn 中断，202 `{operation_id}` | queued 本地取消；已运行按版本走 operation interrupt / 旧取消；202 `{ok:true}` | queued 本地取消；已运行按版本走 operation interrupt / 旧取消；202 `{ok:true}` | 有意：进程模型。中断不另占写锁；旧 Agent 的 interrupted 可能仍有活动操作，保留确认流程 |
| 确认停止 | 无路由 | confirmed=true 与 operation_id；200 `{ok:true}`；匹配待确认操作后取消/清理目标 | 相同请求和响应；匹配待确认操作后取消 | 有意：Codex 无此能力；不占新写锁，必须比较操作 ID，不能停止后来轮次 |
| 分叉 | event_id 可选；要求已初始化、idle、在线；202 `{operation_id,target_thread_id}`；写锁 0，源不认领 | event_id 可选；要求独立原生上下文；202 同左；源 idle→queued，写锁 1 | event_id 可选；只允许最新 assistant 消息边界；快照用于首次发送；202 同左；源→queued，写锁 1 | 有意：CLI 历史机制。目标 ID 新生成，源历史不能被改写 |
| 分叉到 worktree | branch/path/base_ref；只续完整会话；202 附 workspace_id | 同左，另 event_id；202 附 workspace_id | 同左，另 event_id；202 只返回 operation_id/target_thread_id | 有意：边界能力；偶然：Claude 少 workspace_id。均 `git.worktree.create`，写锁 1，创建新工作区 |
| 归档/更新 | PATCH title/pinned/archived，返回更新后的线程；空对象 400；标题空/过长分别报错 | 相同公共字段，200 `{ok:true}`；空对象成功；标题统一错误 | 同 Gemini；置顶在 management 表 | 偶然：响应、校验与空更新。归档拒绝活动会话；另两者连重命名也拒绝活动；不占写锁。保留各自分开更新 pin 的原边界 |
| 删除 | DELETE→200 `{ok:true}`；queued/running 拒绝 | 同响应；还拒绝 interrupting、仍有活动 op 的 interrupted | 同 Gemini | 有意：安全停止判定；不排 Agent 删除原生文件操作；数据库事务内清理关联数据 |
| 事件分页 | before/after 互斥，默认最近 500，上限1000；limit 可调；默认 ConversationEvents，view=raw 原始流 | before/after 互斥，固定500；全部原始事件；先检查线程存在 | 同 Gemini | 偶然：窗口参数/错误文本/存在性检查；有意：Codex 会话投影。200 事件数组；不占锁，无版本限制 |
| 用量 | 200 SessionUsage；running 时 partial | 同结构；running/interrupting 时 partial | 同结构；Claude 缓存/用量事件解释独立 | 有意：计数源；空历史均 null 计数、partial=false；无写锁/版本要求 |

所有请求的具体参数验证和逐条 HTTP 错误表达式在下方索引；状态冲突映射不应因抽象层而丢失。工作区冲突由 Store 的 `ErrWorkspaceWriteActive` 统一映射 409；队列、原生目标、凭据配置和 Agent 更新并发均沿用现有判断。

## Agent / CLI 版本要求

版本门槛集中在 `internal/buildinfo/version.go`，P2-02 用边界测试固定解析。CLI 门槛不等同于 CLI 已安装/ready 状态。

| 能力 | Codex | Gemini | Claude |
| --- | --- | --- | --- |
| 基础轮次 | 无单独版本门槛 | Agent 0.2.60 | Agent 0.2.90 |
| 图片输入 | 无单独门槛 | Agent 0.2.73 | Agent 0.2.93（含快照首次继续） |
| 追加 | Agent 0.2.51 | Agent 0.2.79 且 CLI 0.66.0 | Agent 0.2.95 且 CLI 2.1.286（允许空格后的描述） |
| 追加图片 | 同追加 | Agent 0.2.88 且 CLI 0.66.0 | 同追加 |
| 编辑重发 | 无单独门槛 | Agent 0.2.85 | Agent 0.2.97 |
| 消息分叉 | Agent 0.2.59；完整续聊无门槛 | Agent 0.2.68 | Agent 0.2.93 |
| effort / thinking | Codex reasoning_effort 枚举 | Agent 0.2.70；另按模型校验 | Agent 0.2.92 |
| 不审批 | 原有 never 模式 | yolo：Agent 0.2.72 | bypassPermissions：基础 Claude 门槛 |
| 计划/技能 | 原有 Codex 原生 API | Agent 0.2.83 且 CLI 0.66.0 | skills：Agent 0.2.94 |
| 目标/技能管理 | 原有 Codex API | goal：Agent 0.2.84；技能管理：0.2.88，均 CLI 0.66.0 | 无相应公共功能 |
| 问题回答 | Codex 审批/输入专门接口 | 原有 Gemini 题目回答 | Agent 0.2.98 且 CLI 2.1.286 |
| 操作级中断 | 原有 Codex 轮次中断 | Agent 0.2.96 后优先操作级中断 | 同 Gemini |

## 线程列用途（含启动迁移添加列）

标记：W = WHERE/关联/子查询条件；C = 条件认领/比较后更新；S = UPDATE 里原子子查询取值；U = 主键/唯一约束。排序/展示字段同样需要进入统一读模型。三张表没有原生会话 ID 唯一约束，不可擅自增加。

| 表 | 列 | 用途与标记 |
| --- | --- | --- |
| 三张线程表 | id | W/C/U；主键，事件流、操作/协作/定时任务关联 |
| 三张线程表 | workspace_id | W；外键 ON DELETE CASCADE，工作区/项目/服务器筛选与锁归属 |
| 三张线程表 | title | W/C；列表搜索、默认名条件替换、分叉标题；Codex 默认名来自引擎 |
| 三张线程表 | status | W/C；排队认领、活动/归档/删除/中断安全性；不能塞进无条件 JSON 更新 |
| 三张线程表 | archived_at | W/C；活动列表与归档列表、发起/分叉只读校验 |
| 三张线程表 | created_at | 展示、保留历史来源时间；分叉新建时间 |
| 三张线程表 | updated_at | W/C；列表排序、凭据配置回填的时间比较、轮次更新 |
| codex_threads | codex_thread_id | W/C；原生启动/分叉/导入关联与条件判断 |
| codex_threads | last_sequence | C；事件流序号及重发/分叉前缀复制，不是客户端 offset |
| codex_threads | pinned_at | 列表置顶时间与排序（可空） |
| gemini_threads | session_id | W/C；原生 resume/重发/分叉来源比较，独立上下文校验 |
| gemini_threads | model | W；展示/调度/协作模型回填与运行输入 |
| gemini_threads | approval_mode | 展示与运行输入，保留持久化枚举 |
| gemini_threads | thinking_level | 运行输入、分叉继承与定时/协作设置 |
| gemini_threads | operation_id | W/C；最新执行匹配、幂等、完成/确认停止防旧结果覆盖 |
| gemini_threads | model_credential_revision | S/C；入队时从服务器凭据关联子查询原子保存；启动回填受 updated_at 限制 |
| gemini_threads | pinned_at | 列表置顶时间与排序（可空） |
| claude_threads | session_id | W/C；原生 resume/重发匹配与分叉来源比较 |
| claude_threads | model | W；展示/运行、凭据默认模型同步 |
| claude_threads | permission_mode | 展示/运行输入、分叉继承 |
| claude_threads | operation_id | W/C；与 agent_operations 子查询共同验证 interrupted 是否安全；完成/重发条件更新 |
| claude_threads | model_credential_revision | S/C；迁移添加；入队时关联凭据/模型修订原子保存，回填受时间限制 |
| claude_thread_options | thread_id | W/U；主键及级联外键，一线程一个选项行 |
| claude_thread_options | effort | 运行继承/显式清空，分叉从源子查询复制 |
| claude_thread_management | thread_id | W/U；主键及级联外键 |
| claude_thread_management | pinned_at | 列表置顶/展示；ON CONFLICT(thread_id) 更新 |
| claude_thread_management | fork_snapshot_id | C；首次发送原生会话未初始化时使用，重发成功后清空 |
| claude_thread_management | fork_session_id | C；快照来源会话，首次继续输入，重发成功后清空 |

`steer_state` 是 Claude 查询从 events 取出的派生列，并非线程物理列。Codex 的最新 operation 来自 agent_operations；不能因为另两张表有 operation_id 就把它解释为同一种外键。唯一性还涉及工作区活动写操作的部分唯一索引、幂等操作 key 和 (stream_id,sequence)，它们在原表/事务中保持不变。

## 错误索引

以下由基线处理器的显式错误调用整理，保留表达式以包含版本号和 Store 错误文本。`writeStoreError`：ErrNoRows→404 `resource not found`；状态/活动/写锁/凭据冲突→409 原 err.Error()；非法输入或编辑目标→400 原 err.Error()；唯一/外键冲突→409 `resource conflicts with existing data`；NOT NULL/CHECK→400 `invalid resource data`；其他→500 传入说明。`writeSessionCreateError` 对 ErrNoRows 改用400 `workspace not found`。未显式给状态的 Store 调用按此映射。

| 处理器 | 失败响应/映射表达式 |
| --- | --- |
| `codex_threads.go / forkThreadToWorktree` | `writeError(w, http.StatusNotFound, "Codex session not found")` |
| `codex_threads.go / forkThreadToWorktree` | `writeError(w, http.StatusInternalServerError, "could not load Codex session")` |
| `codex_threads.go / forkThreadToWorktree` | `writeError(w, http.StatusConflict, "Codex session cannot be continued to a worktree")` |
| `codex_threads.go / forkThreadToWorktree` | `writeError(w, http.StatusInternalServerError, "could not load workspace")` |
| `codex_threads.go / forkThreadToWorktree` | `writeStoreError(w, err, "could not load server")` |
| `codex_threads.go / forkThreadToWorktree` | `writeError(w, http.StatusConflict, "server is offline")` |
| `codex_threads.go / forkThreadToWorktree` | `writeError(w, http.StatusBadRequest, err.Error())` |
| `codex_threads.go / forkThreadToWorktree` | `writeStoreError(w, err, "could not queue worktree continuation")` |
| `codex_threads.go / threads` | `writeError(w, http.StatusBadRequest, "archived must be true or all")` |
| `codex_threads.go / threads` | `writeError(w, http.StatusBadRequest, err.Error())` |
| `codex_threads.go / threads` | `writeError(w, http.StatusBadRequest, err.Error())` |
| `codex_threads.go / threads` | `writeError(w, http.StatusInternalServerError, "could not list Codex sessions")` |
| `codex_threads.go / threads` | `writeError(w, http.StatusInternalServerError, "could not list Codex sessions")` |
| `codex_threads.go / thread` | `writeError(w, http.StatusNotFound, "Codex session not found")` |
| `codex_threads.go / thread` | `writeError(w, http.StatusInternalServerError, "could not load Codex session")` |
| `codex_threads.go / updateThread` | `writeError(w, http.StatusBadRequest, "at least one thread field is required")` |
| `codex_threads.go / updateThread` | `writeError(w, http.StatusBadRequest, "thread title is required")` |
| `codex_threads.go / updateThread` | `writeError(w, http.StatusBadRequest, "thread title is too long")` |
| `codex_threads.go / updateThread` | `writeError(w, http.StatusNotFound, "Codex session not found")` |
| `codex_threads.go / updateThread` | `writeError(w, http.StatusInternalServerError, "could not load Codex session")` |
| `codex_threads.go / updateThread` | `writeError(w, http.StatusConflict, "active Codex session cannot be archived")` |
| `codex_threads.go / updateThread` | `writeError(w, http.StatusNotFound, "Codex session not found")` |
| `codex_threads.go / updateThread` | `writeError(w, http.StatusInternalServerError, "could not update Codex session")` |
| `codex_threads.go / archiveProjectThreads` | `writeError(w, http.StatusNotFound, "project not found")` |
| `codex_threads.go / archiveProjectThreads` | `writeError(w, http.StatusInternalServerError, "could not load project")` |
| `codex_threads.go / archiveProjectThreads` | `writeError(w, http.StatusInternalServerError, "could not archive project sessions")` |
| `codex_threads.go / forkThread` | `writeError(w, http.StatusNotFound, "Codex session not found")` |
| `codex_threads.go / forkThread` | `writeError(w, http.StatusInternalServerError, "could not load Codex session")` |
| `codex_threads.go / forkThread` | `writeError(w, http.StatusConflict, "archived Codex session is read-only")` |
| `codex_threads.go / forkThread` | `writeError(w, http.StatusConflict, "Codex session must be initialized before it can be continued")` |
| `codex_threads.go / forkThread` | `writeError(w, http.StatusConflict, "active Codex session cannot be continued yet")` |
| `codex_threads.go / forkThread` | `writeError(w, http.StatusInternalServerError, "could not load server")` |
| `codex_threads.go / forkThread` | `writeError(w, http.StatusConflict, "server is offline")` |
| `codex_threads.go / forkThread` | `writeJSON(w, http.StatusConflict, map[string]string{"code": "fork_agent_update", "error": "Branching from a message requires Agent 0.2.59 or newer"})` |
| `codex_threads.go / forkThread` | `writeError(w, http.StatusInternalServerError, "could not resolve fork boundary")` |
| `codex_threads.go / forkThread` | `writeError(w, http.StatusInternalServerError, "could not queue Codex session continuation")` |
| `codex_threads.go / createThread` | `writeError(w, http.StatusBadRequest, "workspace_id is required")` |
| `codex_threads.go / createThread` | `writeSessionCreateError(w, err, "could not create Codex session")` |
| `codex_threads.go / deleteThread` | `writeError(w, http.StatusNotFound, "Codex session not found")` |
| `codex_threads.go / deleteThread` | `writeError(w, http.StatusInternalServerError, "could not load Codex session")` |
| `codex_threads.go / deleteThread` | `writeError(w, http.StatusConflict, "active Codex session must be interrupted before deletion")` |
| `codex_threads.go / deleteThread` | `writeError(w, http.StatusNotFound, "Codex session not found")` |
| `codex_threads.go / deleteThread` | `writeError(w, http.StatusInternalServerError, "could not delete Codex session")` |
| `codex_threads.go / threadEvents` | `writeError(w, http.StatusBadRequest, "before and after cannot be used together")` |
| `codex_threads.go / threadEvents` | `writeError(w, http.StatusBadRequest, "after must be a non-negative sequence number")` |
| `codex_threads.go / threadEvents` | `writeError(w, http.StatusBadRequest, "after must be a non-negative sequence number")` |
| `codex_threads.go / threadEvents` | `writeError(w, http.StatusBadRequest, "before must be a non-negative sequence number")` |
| `codex_threads.go / threadEvents` | `writeError(w, http.StatusBadRequest, "before must be a non-negative sequence number")` |
| `codex_threads.go / threadEvents` | `writeError(w, http.StatusInternalServerError, "could not load session events")` |
| `codex_turns.go / steerTurn` | `writeError(w, http.StatusBadRequest, "prompt or image is required")` |
| `codex_turns.go / steerTurn` | `writeError(w, http.StatusBadRequest, "invalid or oversized turn images")` |
| `codex_turns.go / steerTurn` | `writeError(w, http.StatusBadRequest, "request_id is too long")` |
| `codex_turns.go / steerTurn` | `writeError(w, http.StatusNotFound, "Codex session not found")` |
| `codex_turns.go / steerTurn` | `writeError(w, http.StatusInternalServerError, "could not load Codex session")` |
| `codex_turns.go / steerTurn` | `writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_inactive", "error": "Codex turn is not running; keep your correction and send it after the session starts or finishes"})` |
| `codex_turns.go / steerTurn` | `writeStoreError(w, err, "could not load server")` |
| `codex_turns.go / steerTurn` | `writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_offline", "error": "Agent must be online to steer a running turn"})` |
| `codex_turns.go / steerTurn` | `writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_agent_update", "error": "Steering requires Agent 0.2.51 or newer; update it after the current task finishes"})` |
| `codex_turns.go / steerTurn` | `writeError(w, http.StatusInternalServerError, "could not identify active Codex turn")` |
| `codex_turns.go / steerTurn` | `writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_inactive", "error": "the target Codex turn is no longer active; send the correction as a new message"})` |
| `codex_turns.go / steerTurn` | `writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_inactive", "error": "the target Codex turn is no longer active; send the correction as a new message"})` |
| `codex_turns.go / steerTurn` | `writeStoreError(w, err, "could not queue Codex steering")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusBadRequest, "conflicting edit target")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusBadRequest, "prompt or image is required")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusBadRequest, "invalid or oversized turn images")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusBadRequest, "model is too long")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusBadRequest, "invalid reasoning_effort")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusBadRequest, "invalid approval_mode")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusBadRequest, "request_id is too long")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusNotFound, "Codex session not found")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusInternalServerError, "could not load Codex session")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusConflict, "archived Codex session is read-only")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusConflict, "active Codex session must finish before editing an earlier message")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusBadRequest, "message is no longer available to edit")` |
| `codex_turns.go / handleTurn` | `writeStoreError(w, err, "could not rewrite Codex session")` |
| `codex_turns.go / handleTurn` | `writeError(w, http.StatusConflict, "active Codex session must finish before starting another turn")` |
| `codex_turns.go / handleTurn` | `writeStoreError(w, err, "could not queue Codex turn")` |
| `codex_turns.go / interruptTurn` | `writeError(w, http.StatusNotFound, "Codex session not found")` |
| `codex_turns.go / interruptTurn` | `writeError(w, http.StatusInternalServerError, "could not load Codex session")` |
| `codex_turns.go / interruptTurn` | `writeError(w, http.StatusConflict, "archived Codex session is read-only")` |
| `codex_turns.go / interruptTurn` | `writeError(w, http.StatusInternalServerError, "could not identify active Codex turn")` |
| `codex_turns.go / interruptTurn` | `writeError(w, http.StatusInternalServerError, "could not identify queued Codex turn")` |
| `codex_turns.go / interruptTurn` | `writeError(w, http.StatusInternalServerError, "could not cancel queued Codex turn")` |
| `codex_turns.go / interruptTurn` | `writeError(w, http.StatusInternalServerError, "could not identify active Codex turn")` |
| `codex_turns.go / interruptTurn` | `writeError(w, http.StatusConflict, "active Codex turn is not ready to interrupt")` |
| `codex_turns.go / interruptTurn` | `writeStoreError(w, err, "could not queue interrupt")` |
| `gemini.go / geminiThreads` | `writeError(w, 500, "could not list Gemini sessions")` |
| `gemini.go / createGeminiThread` | `writeError(w, 400, "invalid Gemini model, thinking level or approval mode")` |
| `gemini.go / createGeminiThread` | `writeSessionCreateError(w, err, "workspace not found")` |
| `gemini.go / createGeminiThread` | `writeStoreError(w, err, "could not load server")` |
| `gemini.go / createGeminiThread` | `writeError(w, 409, "Gemini plan mode requires Wio Agent 0.2.83 and Gemini CLI 0.66.0 or newer")` |
| `gemini.go / createGeminiThread` | `writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiYoloVersion+" or newer to use Gemini without approvals")` |
| `gemini.go / createGeminiThread` | `writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiThinkingVersion+" or newer to configure Gemini thinking")` |
| `gemini.go / createGeminiThread` | `writeSessionCreateError(w, err, "workspace not found or unavailable")` |
| `gemini.go / loadGeminiThread` | `writeError(w, 404, "Gemini session not found")` |
| `gemini.go / loadGeminiThread` | `writeError(w, 500, "could not load Gemini session")` |
| `gemini.go / geminiEvents` | `writeError(w, 400, "before and after cannot be used together")` |
| `gemini.go / geminiEvents` | `writeError(w, 400, "invalid event cursor")` |
| `gemini.go / geminiEvents` | `writeError(w, 500, "could not load Gemini events")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 400, "invalid Gemini thinking level for this model")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 400, "invalid prompt, images, model or approval mode")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 409, "Gemini requires an online Agent; check the server connection")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 500, "could not load Gemini Agent")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 409, "update the Agent to version "+buildinfo.MinimumGeminiVersion+" or newer to use Gemini")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiYoloVersion+" or newer to use Gemini without approvals")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 409, "Gemini plan mode requires Wio Agent 0.2.83 and Gemini CLI 0.66.0 or newer")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiThinkingVersion+" or newer to configure Gemini thinking")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiInputVersion+" or newer to send Gemini images")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 400, "invalid Gemini goal objective or limits")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 409, "Gemini goals require Wio Agent "+buildinfo.MinimumGeminiGoalVersion+" and Gemini CLI 0.66.0 or newer")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 409, "update the Agent to version "+buildinfo.MinimumGeminiRewriteVersion+" or newer to edit Gemini history")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 409, "this Gemini message has no saved native turn to rewind; original conversation was kept")` |
| `gemini.go / startGeminiTurn` | `writeError(w, 409, "active or archived Gemini session cannot accept a turn")` |
| `gemini.go / startGeminiTurn` | `writeStoreError(w, err, "could not queue Gemini turn; check active workspace operations")` |
| `gemini.go / interruptGeminiTurn` | `writeStoreError(w, err, "Gemini turn is no longer running")` |
| `gemini.go / confirmGeminiStopped` | `writeError(w, 400, "confirmation and operation ID are required")` |
| `gemini.go / confirmGeminiStopped` | `writeStoreError(w, err, "Gemini task changed or no longer needs confirmation")` |
| `gemini.go / updateGeminiThread` | `writeError(w, 400, "invalid session title")` |
| `gemini.go / updateGeminiThread` | `writeStoreError(w, err, "finish or interrupt the Gemini turn first")` |
| `gemini.go / updateGeminiThread` | `writeError(w, 500, "could not pin Gemini session")` |
| `gemini.go / deleteGeminiThread` | `writeStoreError(w, err, "finish or interrupt the Gemini turn before deleting it")` |
| `claude.go / claudeThreads` | `writeError(w, 500, "could not list Claude sessions")` |
| `claude.go / createClaudeThread` | `writeSessionCreateError(w, err, "workspace not found or unavailable")` |
| `claude.go / createClaudeThread` | `writeStoreError(w, err, "server not found or unavailable")` |
| `claude.go / createClaudeThread` | `writeError(w, 400, "invalid Claude model, permission mode or effort")` |
| `claude.go / createClaudeThread` | `writeSessionCreateError(w, err, "workspace not found or unavailable")` |
| `claude.go / loadClaudeThread` | `writeError(w, 404, "Claude session not found")` |
| `claude.go / loadClaudeThread` | `writeError(w, 500, "could not load Claude session")` |
| `claude.go / claudeEvents` | `writeError(w, 400, "before and after cannot be used together")` |
| `claude.go / claudeEvents` | `writeError(w, 400, "invalid event cursor")` |
| `claude.go / claudeEvents` | `writeError(w, 500, "could not load Claude events")` |
| `claude.go / startClaudeTurn` | `writeError(w, 400, "invalid prompt, Claude model, permission mode or effort")` |
| `claude.go / startClaudeTurn` | `writeError(w, 409, "Claude requires an online Agent")` |
| `claude.go / startClaudeTurn` | `writeError(w, 409, "update the Agent to version "+buildinfo.MinimumClaudeVersion+" or newer to use Claude")` |
| `claude.go / startClaudeTurn` | `writeError(w, 409, "update the Agent to version "+buildinfo.MinimumClaudeEffortVersion+" or newer to select Claude effort")` |
| `claude.go / startClaudeTurn` | `writeError(w, 409, "update Agent to "+buildinfo.MinimumClaudeInputVersion+" or newer to use Claude images and session forks")` |
| `claude.go / startClaudeTurn` | `writeError(w, 409, "update Agent to "+buildinfo.MinimumClaudeRewriteVersion+" or newer to edit Claude messages")` |
| `claude.go / startClaudeTurn` | `writeError(w, http.StatusConflict, err.Error())` |
| `claude.go / startClaudeTurn` | `writeStoreError(w, err, "could not queue Claude turn; check session and active workspace operations")` |
| `claude.go / interruptClaudeTurn` | `writeStoreError(w, err, "Claude turn is no longer running")` |
| `claude.go / confirmClaudeStopped` | `writeError(w, 400, "confirmation and operation ID are required")` |
| `claude.go / confirmClaudeStopped` | `writeStoreError(w, err, "Claude task changed or no longer needs confirmation")` |
| `claude.go / updateClaudeThread` | `writeError(w, 400, "invalid session title")` |
| `claude.go / updateClaudeThread` | `writeStoreError(w, err, "finish or interrupt the Claude turn first")` |
| `claude.go / updateClaudeThread` | `writeError(w, 500, "could not pin Claude session")` |
| `claude.go / deleteClaudeThread` | `writeStoreError(w, err, "finish or interrupt the Claude turn before deleting it")` |
| `gemini_steering.go / steerGeminiTurn` | `writeError(w, 400, "invalid Gemini steering instruction")` |
| `gemini_steering.go / steerGeminiTurn` | `writeError(w, 409, "Gemini steering requires Agent 0.2.79 and Gemini CLI 0.66.0 or newer")` |
| `gemini_steering.go / steerGeminiTurn` | `writeError(w, 409, "running image updates require Agent 0.2.88 and Gemini CLI 0.66.0 or newer")` |
| `gemini_steering.go / steerGeminiTurn` | `writeStoreError(w, err, "could not queue request")` |
| `claude_steering.go / steerClaudeTurn` | `writeError(w, 400, err.Error())` |
| `claude_steering.go / steerClaudeTurn` | `writeError(w, 409, "Claude steering requires Agent 0.2.95 and Claude Code 2.1.286 or newer")` |
| `claude_steering.go / steerClaudeTurn` | `writeStoreError(w, err, "could not queue request")` |
| `gemini_management.go / archiveGeminiProject` | `writeStoreError(w, err, "project not found")` |
| `gemini_management.go / archiveGeminiProject` | `writeError(w, 500, "could not archive Gemini sessions")` |
| `gemini_management.go / queueGeminiFork` | `writeError(w, 409, "Gemini session must be initialized, online and unarchived")` |
| `gemini_management.go / queueGeminiFork` | `writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiForkVersion+" or newer")` |
| `gemini_management.go / queueGeminiFork` | `writeStoreError(w, err, "workspace not found")` |
| `gemini_management.go / queueGeminiFork` | `writeError(w, 400, err.Error())` |
| `gemini_management.go / queueGeminiFork` | `writeStoreError(w, err, "could not branch Gemini conversation; finish the current turn and select a completed final reply")` |
| `claude_management.go / archiveClaudeProject` | `writeStoreError(w, err, "project not found")` |
| `claude_management.go / archiveClaudeProject` | `writeError(w, 500, "could not archive Claude sessions")` |
| `claude_management.go / queueClaudeFork` | `writeError(w, 409, "Claude fork requires an initialized online session and Agent "+buildinfo.MinimumClaudeInputVersion+" or newer")` |
| `claude_management.go / queueClaudeFork` | `writeStoreError(w, err, "workspace not found")` |
| `claude_management.go / queueClaudeFork` | `writeError(w, 400, err.Error())` |
| `claude_management.go / queueClaudeFork` | `writeStoreError(w, err, "finish the current turn and other workspace operations before continuing")` |
| `usage.go / geminiUsage` | `writeError(w, http.StatusInternalServerError, "could not load session usage")` |
| `usage.go / claudeUsage` | `writeError(w, http.StatusInternalServerError, "could not load session usage")` |
| `usage.go / threadUsage` | `writeError(w, http.StatusNotFound, "session not found")` |
| `usage.go / threadUsage` | `writeError(w, http.StatusInternalServerError, "could not load session")` |
| `usage.go / threadUsage` | `writeError(w, http.StatusInternalServerError, "could not load session usage")` |
