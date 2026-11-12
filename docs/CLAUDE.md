# Claude 接入参考

日常会话、图片、分支、编辑重发、提问与定时任务见[用户手册](USER_GUIDE.md#10-claude)。本文补充执行环境、认证及兼容限制；版本门槛以 [buildinfo](../internal/buildinfo/version.go) 和目标 CLI 实际能力为准。

## 安装与执行账号

Wio 通过官方 Claude Code CLI 的无头模式执行任务，不转换为其他模型接口。官方控制面镜像已包含 CLI；外部服务器的 SSH 注册/修复会安装最新正式版，也可通过 **设置 → Claude CLI 版本管理** 选择目标版本，再在 **服务器 → 管理 AI Agent** 安装。

网页安装依赖 Node.js/npm，使用 `@anthropic-ai/claude-code`，版本验证通过后才切换受管路径。运行中的任务、待确认停止的进程、凭据应用或版本维护会阻止冲突操作。CLI 自动更新由 Wio 禁用，版本通过管理入口控制。

外部 Agent 默认账号为 `wio-agent`，Home 为 `/var/lib/wio-agent`。CLI、认证、PATH 和文件权限必须属于同一运行账号，systemd 不会读取交互式 `.bashrc`。

## 认证与模型

托管配置步骤：

1. 在 **设置 → 服务器凭据预设** 创建 Claude API 预设，填写基础地址、模型/别名和密钥。
2. 官方基础地址为 `https://api.anthropic.com`；代理必须兼容 Anthropic Messages API。
3. 在 **服务器 → 修改服务器凭据 → Claude API** 应用，等成功后检查绑定。修改 API 地址或密钥后须重新应用；仅修改默认模型时，重新进入会话即可自动刷新。

Agent 将配置保存在受保护的状态文件，启动模型进程时注入 `ANTHROPIC_API_KEY` 和 `ANTHROPIC_BASE_URL`，不写入项目。未应用托管预设时，可使用 Agent 账号已有 CLI 登录或服务环境认证。

可用模型从已应用预设的官方 `GET /v1/models` 接口获取并分页，进入/切换会话时刷新，实时消息刷新不重复发现模型。列表失败保留当前选择，可手动输入模型；仅使用 CLI 登录时使用默认别名/自定义模型。已应用过的预设仅修改默认模型时，确认 API 地址和密钥未变后自动刷新列表，新会话和旧会话重新进入时采用新的默认模型。配置未变时保留会话已选择的模型，运行中的任务不切换模型；修改 API 地址或密钥仍须重新应用。

思考程度使用 `--effort low|medium|high|xhigh|max`，默认不传参数，实际支持程度取决于 CLI 和模型。

## 兼容版本

| 能力 | 最低 Wio Agent | CLI 要求 |
| --- | --- | --- |
| 基础 Claude 会话 | 0.2.90 | 可用的 Claude Code CLI |
| 凭据与版本管理 | 0.2.91 | 可运行对应管理命令 |
| 思考程度 | 0.2.92 | 支持 `--effort` |
| 图片、续聊快照、定时任务及菜单 | 0.2.93 | 支持相应 stream-json / resume 参数 |
| 主动发现技能 | 0.2.94 | 支持 SDK initialize |
| 运行中追加 | 0.2.95 | 2.1.286+ |
| 消息编辑与回复分支 | 0.2.97 | 支持 `--resume-session-at` 等参数 |
| 会话内提问 | 0.2.98 | 2.1.286+ |
| 上下文压缩事件 | 0.3.03 | 上报原生压缩事件 |
| Wio 会话协作 | 0.3.06 | 支持进程级 MCP 配置 |

当前镜像的 Claude CLI 基线见 [Dockerfile](../deploy/controlplane.Dockerfile)，不表示其他 CLI 版本都已验证。

## 权限与运行状态

`default`、`acceptEdits`、`plan`、`bypassPermissions` 对应页面的默认、允许编辑、计划、跳过权限检查。网页不提供通用工具审批，需要交互批准的工具可能被拒绝。`AskUserQuestion` 由独立的 SDK 控制请求转发到网页，用户回答不授予其他工具权限。

Claude Code 通常拒绝 root 使用 bypassPermissions。内置控制机在容器内以 root 运行，镜像设置 `IS_SANDBOX=1` 使此模式可用；该变量不提供文件系统隔离，实际权限由容器挂载、Docker socket 和系统账号决定。

同一会话一次一轮；同一工作区的多个 Claude/Gemini 会话可以并行，也可与 Codex 会话同时运行。Claude/Gemini 仍与 Git、文件写入等工作区写锁操作互斥；Codex 不使用该写锁。并行会话共享目录中的文件，需要隔离修改时使用独立 worktree。中断会终止所属进程组并等待退出。连接丢失且无法确认原进程时，不自动重放任务或释放工作区；先核实再在页面确认停止。

追加指令使用同一进程的双向 stream-json 输入，传入原生 `priority: "now"`。CLI 可将支持的工具移至后台，或中断当前生成后继续处理；后台工具未结束时保留任务和工作区锁。每轮最多追加 32 次，文字累计 64 KiB、图片 data URL 累计 6 MiB；每次图片仍限 4 张、单张 2 MiB、总计 4 MiB。无法确认的追加不自动重发。

## 原生历史与恢复

原生会话默认位于 Agent 账号的 `~/.claude`，也可由 `CLAUDE_CONFIG_DIR` 指定。继续到新会话会在 Agent 状态目录的 `claude/forks/` 保存不可变 transcript 快照，首次发送使用 `--resume <snapshot.jsonl> --fork-session`，之后按新原生 ID 续聊。快照与操作回执按同一 **7 天**保留清理（Agent 启动时及运行中定期执行）；分支建立后长期未发送首条消息时快照可能已被清理，需重新分支。

编辑重发按用户消息 UUID 找到上一条助手消息，再通过 `--resume-session-at` 恢复；首条消息从空历史开始。Wio 收到新原生初始化后才替换网页历史。旧记录匹配有歧义或边界不安全时拒绝编辑，保留原聊天。

消息分支只对最后一次助手回复、且没有更新用户输入的位置开放。分支/编辑均不恢复项目文件；新 worktree 的文件来自所选 Git 引用，不包含源目录未提交文件。备份时同时保留 Wio 数据库、Agent 状态目录和实际 Claude 配置目录。

## 自定义代理与流式等待

非 Anthropic 官方基础地址默认经过 Agent 本机回环流式转发代理。部分中转在 `message_stop` 后仍保持心跳连接，CLI 会继续等待；Wio 在收到该终止事件后结束流，减少每次工具调用和轮次结束的额外等待。

代理不保存或添加凭据，回环地址加入 `NO_PROXY`。如需禁用，在 Agent 服务环境设置 `WIO_CLAUDE_STREAM_PROXY=off` 后重启；已运行的任务不会热切换。

## 官方参考

- [安装与认证](https://code.claude.com/docs/en/setup)
- [无头模式](https://code.claude.com/docs/en/headless)
- [CLI 参数](https://code.claude.com/docs/en/cli-reference)
- [权限](https://code.claude.com/docs/en/permissions)
- [会话与分支](https://code.claude.com/docs/en/sessions)
- [技能](https://code.claude.com/docs/en/skills)
- [Agent SDK 会话内提问](https://code.claude.com/docs/en/agent-sdk/user-input)
