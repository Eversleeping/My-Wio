# Claude 会话

侧边栏的 **Claude** 页面通过目标服务器上的官方 Claude Code CLI 执行工作区任务。Wio 支持独立会话、模型和工具权限选择、流式回答、工具调用与结果、续聊、中断、重命名、归档、删除以及工作区文件和变更预览。Claude、Codex 和 Gemini 的会话彼此独立。

## 安装、版本和认证

1. 控制端和目标服务器的 Wio Agent 更新至 **0.2.91 或以上**，即可管理 Claude 凭据和版本。0.2.90 仅支持基础会话；旧 Agent 的管理按钮会提示升级。
2. **设置 → Claude CLI 版本管理**可检查官方最近五个正式版本并选择目标版本。在 **服务器 → 管理 AI Agent** 中安装或切换 Claude CLI；目标版本会默认选中。安装使用官方 npm 包 `@anthropic-ai/claude-code`，先校验版本，再切换受管路径；安装失败保留原来的选择。运行中的 Claude 任务、待确认停止的进程、凭据下发和版本维护会阻止冲突操作。
3. SSH 注册和修复会安装并验证 Claude Code 的最新正式版。官方控制端镜像也已内置 Claude CLI。已有服务器可通过上述版本管理入口安装，需要 Node.js/npm；缺少依赖时先运行 SSH 修复。
4. **设置 → 新增凭据 → Claude API** 填写名称、API 基础地址、模型和密钥。官方地址为 `https://api.anthropic.com`，兼容 Anthropic Messages API 的代理填写其基础地址。Wio 通过 `ANTHROPIC_API_KEY` 和 `ANTHROPIC_BASE_URL` 向 CLI 提供配置，密钥在控制端加密保存，在 Agent 的受保护配置文件中保存，不写入项目仓库。
5. 在 **服务器 → 更改凭据 → Claude API** 选择预设并应用，等待成功后才会更新绑定。编辑预设后须重新应用；新会话默认模型取自最后一次成功应用的配置快照，可在会话中另行调整。Claude 配置独立于 Codex、Gemini 和 Git。
6. 未应用托管预设时，CLI 可继续使用 Agent 运行账号已有的 Claude Code 登录或服务环境认证。请确保 `HOME`、`PATH`、文件权限和认证均属于同一运行账号。托管预设生效后，CLI 使用该预设的 API 密钥和地址。Wio 禁止 CLI 自动更新，版本由管理入口控制；此处“已就绪”表示可找到 CLI，不代表密钥已通过模型调用验证。

## 使用方式和权限

1. 在 **Claude** 页面点击 **新建 Claude 会话**，选择工作区、模型名称和工具权限；输入任务并发送。模型可输入 Claude Code 支持且当前账号有权使用的别名或模型 ID。每次发送前可调整模型、思考程度和权限。思考程度支持默认、低、中、高、超高、最高，对应 Claude Code 的 `--effort low|medium|high|xhigh|max`；默认不传该参数，由 CLI 决定。选定档位会保存在会话中，实际支持程度取决于模型。此功能要求 Wio Agent **0.2.92 或以上**，以及支持 `--effort` 的 Claude Code（已验证 2.1.318）。
2. 对话显示文本、工具调用参数和结果；需要时可中断任务。侧栏按工作区分组，支持折叠、搜索、项目置顶和隐藏，以及会话菜单中的重命名、归档和恢复；可删除会话，打开工作区文件及变更预览。
3. 继续同一会话时，Wio 使用官方 `--resume <session-id>` 恢复 CLI 的原生历史。原生会话文件保存在 Agent 账号的 Claude Code 目录中；删除 Wio 会话只删除 Wio 记录，不删除这些文件或项目文件。请保留该目录，否则可能无法续聊。
4. CLI 运行在无头 print 模式。**默认**、**允许编辑**和**计划模式**分别对应官方 `default`、`acceptEdits` 和 `plan` 权限模式；需要交互审批的工具请求无法在 Wio 网页批准，可能被 CLI 拒绝。**跳过权限检查**对应 `bypassPermissions`，会跳过 Claude Code 的工具审批，请仅在可信环境使用。具体行为仍受 Claude Code 配置、策略和 Agent 账号的系统权限约束。
5. 同一会话同时只能运行一轮。Claude 任务与使用 Wio 工作区写锁的 Gemini 和 Git 等操作互斥；Codex 当前不使用该写锁，应自行避免同时修改同一工作区。
6. Agent 失联且无法确认原进程状态时，Wio 不会自动重放任务或释放工作区写锁。请先在目标服务器核实原 Claude 进程确实已退出，再使用页面上的 **确认进程已停止**。历史每批加载最多 500 条事件，可继续加载更早记录。

Claude 页面目前不提供图片输入、交互审批、Goal、运行中追加指令或定时任务。

## 官方参考

- [Claude Code 安装](https://code.claude.com/docs/en/setup)
- [认证](https://code.claude.com/docs/en/authentication)
- [无头与程序化运行](https://code.claude.com/docs/en/headless)
- [CLI 参数](https://code.claude.com/docs/en/cli-reference)
- [权限模式](https://code.claude.com/docs/en/permissions)
