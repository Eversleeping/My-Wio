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
2. 对话显示文本、工具调用参数和结果；用户消息、Claude 回复和错误提示右上角显示消息时间，使用浏览器本地时区，与 Codex 一致。流式回复保留首段时间，缺少有效历史时间时不补造时间。需要时可中断任务。侧栏按工作区分组，支持折叠、搜索、项目置顶和隐藏，以及会话菜单中的重命名、归档和恢复；可删除会话，打开工作区文件及变更预览。
   会话标题下显示“本会话累计”：输入 tokens、输出 tokens 和平均输出 tokens/秒，手机端使用紧凑数字。统计读取完整已保存历史，不受消息分页影响；输入包含缓存创建和缓存读取，按 Claude 原生 `result.usage` 的主会话各轮用量累计，运行中追加指令产生的多轮结果分别计入。该字段不包含子代理或辅助模型调用；恢复会话时不叠加包含历史的 `modelUsage`。平均速度按输出 tokens 除以已记录任务执行时长（包含工具等待）；缺少用量、时间或中断后的完整记录时显示记录不完整及不可用数值，不以零代替未知。旧 Agent 已保存的原生用量也可统计，运行期间定时刷新。
3. 继续同一会话时，Wio 使用官方 `--resume <session-id>` 恢复 CLI 的原生历史。原生会话文件保存在 Agent 账号的 Claude Code 目录中；删除 Wio 会话只删除 Wio 记录，不删除这些文件或项目文件。请保留该目录，否则可能无法续聊。
4. CLI 运行在无头 print 模式。**默认**、**允许编辑**和**计划模式**分别对应官方 `default`、`acceptEdits` 和 `plan` 权限模式；需要交互审批的工具请求无法在 Wio 网页批准，可能被 CLI 拒绝。**跳过权限检查**对应 `bypassPermissions`，会跳过 Claude Code 的工具审批，请仅在可信环境使用。具体行为仍受 Claude Code 配置、策略和 Agent 账号的系统权限约束。
5. 同一会话同时只能运行一轮。Claude 任务与使用 Wio 工作区写锁的 Gemini 和 Git 等操作互斥；Codex 当前不使用该写锁，应自行避免同时修改同一工作区。
6. Agent 失联且无法确认原进程状态时，Wio 不会自动重放任务或释放工作区写锁。请先在目标服务器核实原 Claude 进程确实已退出，再使用页面上的 **确认进程已停止**。历史每批加载最多 500 条事件，可继续加载更早记录。

## 项目、会话菜单和输入（Agent 0.2.93+）

- 项目菜单支持新建会话、置顶、重命名、创建永久 worktree、归档项目全部任务和隐藏。项目名称、置顶、隐藏及批量归档均按服务器隔离；批量归档跳过正在运行或待确认停止的任务。
- 会话菜单支持置顶、重命名、创建定时任务、继续到新任务／新 worktree、归档、复制工作目录、Wio ID、Claude 原生 ID、深度链接和在新窗口打开。永久 worktree 复用现有 Git 创建、失败清理和工作区写锁。
- 继续会话先在 Agent 的 `claude/forks/` 保存当前原生 transcript 快照并复制 Wio 历史；快照期间锁住源任务。首次发送时使用官方 `--resume <snapshot.jsonl> --fork-session` 创建独立原生会话，之后按新 ID 续聊。仅创建分支不会调用模型。原生 ID 在首次发送后生成；原任务之后的内容不会加入快照。保留 Agent 状态目录和 Claude 配置目录（默认 `~/.claude`，可由 `CLAUDE_CONFIG_DIR` 指定）才能恢复历史。快照不包含未提交文件的副本；新 worktree 的文件来自所选 Git 引用。
- 两次回复之间连续的命令和工具调用默认合并折叠，显示执行数量；展开后可查看每次调用的参数、结果和失败状态。按原生工具 ID 配对结果，不重复计数；不同回复、用户消息及错误提示会分开显示。
- 图片可通过粘贴添加，支持 PNG、JPEG、WebP，每次最多 4 张；浏览器复用现有压缩逻辑，可预览和删除，允许只发送图片。服务端校验 MIME 和实际文件类型，单张上限 2 MiB、合计 4 MiB，传输使用 `--input-format stream-json` 的 `image` / `base64` 内容块。历史中保留图片预览。
- 每次进入或切换会话时自动刷新一次当前服务器已应用凭据对应的可用模型，使用官方 `GET /v1/models` 并处理分页；模型下拉框和 `/model` 菜单同步展示结果。实时消息和定期刷新会话记录不会重复请求模型列表，也不会覆盖当前模型。下拉框末尾提供“自定义模型…”及独立输入框；刷新失败仍可选择自定义模型输入；未应用托管凭据、仅使用 CLI 登录时保留默认别名和自定义输入。编辑凭据预设后须重新应用，避免查询的凭据与 Agent 实际使用的凭据不一致。
- 输入 `/` 可使用键盘选择 `/model`、`/effort`、`/permissions`、`/skill`、`/plan`、`/new`、`/fork`、`/schedule`、`/help`。选项命令在网页生效；进入会话时主动通过 CLI SDK initialize 协议读取当前账号、项目和插件的原生技能与提示命令（要求 Agent **0.2.94+**），不发送用户消息、不调用模型、不创建持久会话。菜单仅展示 `/skill`，手动输入 `/skills` 仍可打开同一列表，显示描述和参数提示，支持插件命名空间及手动刷新。新发现的空列表会清除历史缓存；发现失败或旧 Agent 使用最近一次初始化的技能作为后备，并显示状态或升级提示。选择后可补充参数再发送，也可手动输入 `/skill-name `。技能由 Claude Code 原生展开。交互式终端专用命令不保证在 print 模式可用。
- 定时任务使用控制端现有调度器，支持时区、重复频率、模型覆盖、思考程度和工具权限；可在设置中编辑、启停、删除并查看最近执行状态。Agent 离线、会话归档、忙碌或待恢复时跳过执行；旧版 Agent 要求升级。定时器保存在独立 Claude 表中，保留旧控制端读取兼容性。

Claude 页面目前不提供交互审批或 Goal。上述新增功能已使用官方 Claude Code 2.1.318 对隔离的本地模拟 API 验证图片请求、同目录／新 worktree 的原生分支和技能展开；不会因此调用付费模型。

## 消息分支与编辑重发（Agent 0.2.97+）

- 最后一次 Claude 回复下方的 **… → 分支到新聊天** 会复制当前对话到独立聊天，继承模型、思考程度和权限；完成后自动打开新聊天。源聊天保持原样，首次在新聊天发送消息时才生成新的 Claude 原生会话。更早的回复或已有更新用户输入的回复不提供此入口。
- 用户消息下方的铅笔按钮可编辑并重新发送，原文字和图片会回填到输入框，支持删除、补充图片或取消编辑。发送后保留此前对话，并重新生成该消息及之后的回复；任务运行、离线、归档或尚未生成原生会话时不能编辑。旧 Agent 会提示升级至 0.2.97。
- 编辑前先在目标 Agent 保存不可变原生快照，按用户消息 UUID 找到前一条助手消息，使用 `--resume <snapshot.jsonl> --fork-session --resume-session-at <assistant-uuid>` 恢复；编辑首条消息时从空历史开始。Wio 在收到新原生会话初始化后才更新网页历史，准备失败或启动前取消会保留原记录。重复初始化和旧操作的迟到输出不会恢复已丢弃内容。
- 新消息使用操作 UUID 作为原生输入 UUID。旧消息仅在原文及该轮时间范围可唯一定位时兼容编辑；记录缺失、匹配歧义或工具调用边界无法安全恢复时会拒绝操作并保留历史。原生文件不被改写，项目文件也不会随消息编辑回退。

已使用官方 Claude Code 2.1.318 和隔离的模拟 API 验证首条／中间消息恢复、旧会话继续使用及发送给模型的历史边界。

## 运行中调整方向（Agent 0.2.95+）

控制端与目标服务器的 Wio Agent 均更新至 **0.2.95+**，并安装 **Claude Code 2.1.286+**，即可在任务运行且输入流就绪时继续输入，发送补充文字或粘贴图片。有内容时按钮变为“追加指令”，清空草稿后仍可停止任务；运行中模型、思考程度和权限保持原设置。旧 Agent 或旧 CLI 保留原有会话与停止功能。

Wio 保持同一个 Claude 进程的双向 stream-json 输入，使用官方 `SDKUserMessage` 的 `priority: "now"` 和 `origin: {kind: "human"}`，不会通过重启或新建会话模拟调整方向。可后台执行的 shell、子代理和 MCP 工具会移至后台并读取新指令；纯文本生成或不能后台执行的工具可能被原生 CLI 中断后继续处理。2.1.287+ 也支持 WebFetch/WebSearch。后台工具尚未结束时，Wio 保留该任务及工作区写锁；停止任务会终止所属进程组并等待退出。

补充指令显示排队、已接收、已用于回复、未发送或未确认等状态。CLI 回显只表示已接收；原生回复或结果引用该消息 UUID 后才标记已用于回复，并作为一条用户消息加入对话。请求 ID 在控制端和 Agent 两侧去重，网络失败时保留草稿且用同一个 ID 重试，避免重复输入；任务已结束的失败草稿不会自动变成新一轮。无法确认的指令不会自动重发。每轮最多追加 32 次，文字累计 64 KiB、图片 data URL 累计 6 MiB；图片仍遵守原有单次校验限制。CLI 返回结果后仍有未确认指令时等待后续处理，空闲超过 45 秒则结束并标记未确认。

已使用官方 Claude Code 2.1.318 与隔离的本地模拟 API 验证文本生成期间调整、运行中 shell 后台处理、原生消息回执与多次结果收尾，无需调用付费模型。

## 会话内提问（Agent 0.2.98+）

- 目标 Agent 需更新至 **0.2.98+**，Claude Code 需 **2.1.286+**。Claude 调用原生 `AskUserQuestion` 时，会话内显示与 Codex 相同的提问卡片，支持一次多题、单选、多选和自定义文字；手机端沿用自适应问答布局。
- 任务在同一 CLI 进程内等待回答。网页通过操作中心分派答案，Agent 使用官方 SDK 的 `can_use_tool` 控制请求与 `control_response`，将原始问题及按问题原文映射的 `updatedInput.answers` 回传；不创建新聊天、不通过追加普通消息模拟回答。默认、计划、允许编辑和跳过权限检查四种模式已用真实 CLI 2.1.318 与隔离模拟 API 验证。
- 问题及已提交答案保存在会话事件中，刷新可恢复。每次回答绑定原会话、问题和任务；重复提交同一答案返回原操作，不重复发送，修改已提交答案会被拒绝。CLI 取消问题、任务结束、停止、Agent 离线或会话归档后不能继续回答。等待期间仍可停止任务，工作区写锁保持到原任务结束。
- 此功能只处理提问。其他需要审批的工具继续拒绝，不会自动批准工具或持久化权限规则。原有 Agent 保留原先行为；升级后从下一轮任务启用提问。

## 官方参考

- [Claude Code 安装](https://code.claude.com/docs/en/setup)
- [认证](https://code.claude.com/docs/en/authentication)
- [无头与程序化运行](https://code.claude.com/docs/en/headless)
- [CLI 参数](https://code.claude.com/docs/en/cli-reference)
- [权限模式](https://code.claude.com/docs/en/permissions)

- [会话与分支](https://code.claude.com/docs/en/sessions)
- [Agent SDK 会话与分支恢复](https://code.claude.com/docs/en/agent-sdk/sessions)
- [Agent SDK resumeSessionAt 与 forkSession](https://code.claude.com/docs/en/agent-sdk/typescript)
- [技能与斜杠命令](https://code.claude.com/docs/en/skills)
- [结构化图片输入](https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode)

- [Models API 与官方 SDK](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/resources/models.py)

- [工作期间消息队列](https://code.claude.com/docs/en/interactive-mode#queue-messages-while-claude-works)
- [SDKUserMessage 与 priority](https://code.claude.com/docs/en/agent-sdk/typescript#sdkusermessage)

- [Agent SDK 会话内提问与答案格式](https://code.claude.com/docs/en/agent-sdk/user-input)
