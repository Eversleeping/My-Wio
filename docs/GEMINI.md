# Gemini 会话

侧边栏 **Codex** 下方的 **Gemini** 页面通过目标服务器上的官方 Gemini CLI 处理项目任务。支持工作区会话、模型选择、流式回答、工具输出、历史续聊、中断、重命名、归档和删除；文件树、文件编辑与 diff 预览沿用工作区功能。Gemini 与 Codex 的会话、模型和事件独立保存。

## 安装和认证

1. 更新控制面及目标服务器的 Wio Agent 至 `0.2.60` 或以上版本。旧版 Agent 不识别 Gemini 操作，页面会要求先升级。
2. 从 `0.2.61` 起，可在 **服务器 → 管理 AI Agent** 中分别查看 Codex、Gemini 的安装状态和版本，选择最近五个官方稳定版进行安装、升级或切换至较早版本。目标 Wio Agent 也需为 `0.2.61` 或以上；通过现有 Agent 通道安装，无需重新输入 SSH 凭据。CLI 安装到 Agent 状态目录的 `gemini/versions/<版本>`，校验后切换，并在重启后恢复。运行中的对话会阻止切换，失败则保留原有版本。需要 Node.js 20+ 和 npm；缺失时可使用 SSH 修复补齐依赖。

   也可在目标 Linux 服务器上，以运行 `wio-agent` 的账号手动安装。服务的 `PATH` 必须能找到 `gemini`：

   ```bash
   npm install -g @google/gemini-cli
   gemini --version
   ```

3. 从 `0.2.63` 起，可在 **设置 → Gemini CLI 版本管理** 中检查最近五个正式版本、保存目标版本。API 配置统一放在 **服务器凭据预设**：新建预设时选择 Gemini API，下发时在 **服务器 → 修改服务器凭据 → Gemini API** 中选择预设，无需配置 Codex。目标版本会在服务器的「管理 AI Agent」中默认选中，实际安装仍由服务器页面执行。

   API 配置包括名称、基础地址、默认模型和密钥。默认地址为 `https://generativelanguage.googleapis.com`；自定义地址必须兼容 Gemini API，CLI 会追加 API 路径，不要填写 OpenAI 接口地址。密钥在控制面及操作队列中加密，响应只返回元数据；编辑时留空保留原密钥。编辑后需重新下发，各服务器在操作成功后才更新绑定。服务器需在线且 Wio Agent 至少为 `0.2.63`。

   从 `0.2.65` 起，地址末尾的 `/v1` 或 `/v1beta` 会统一转换为 Gemini 原生 `/v1beta` 请求，模型发现与会话调用使用相同路径；网关路径前缀保留。Agent 升级重启时也会修正已下发的旧配置，避免出现 `/v1/v1beta` 路径。

   Agent 将配置写入状态目录下受限权限的 `gemini/api.json`，重启后恢复。运行 Gemini CLI 时独立注入 `GEMINI_API_KEY`、`GOOGLE_GEMINI_BASE_URL` 和 API 密钥认证设置，不修改 Codex/Git 配置或全局进程环境。运行中的 Gemini 任务及配置/安装操作会阻止切换。

   也可选择以下认证方式：

   - 以同一 Agent 账号运行 `gemini`，完成 Google 登录。无头模式会使用该账号已缓存的认证。
   - 将 Google AI Studio 的 `GEMINI_API_KEY` 配置到 Agent 的服务环境或该账号的 `~/.gemini/.env` 中。服务环境变化后需重启 Agent；不要将真实密钥写入项目仓库。
   - Vertex AI 用户可按官方文档配置 ADC、项目和区域等环境变量。

   systemd 服务不会自动读取交互式终端的 `.bashrc`。CLI 的账号、`HOME`、凭据及 `PATH` 必须与 Agent 服务一致。内置控制机 Agent 使用控制面进程的环境；官方控制面镜像从 `0.2.60` 起已包含 Gemini CLI，认证目录位于持久化的 Agent 状态卷中。外部 Agent 可通过服务器页面安装 CLI；网页安装的版本优先于系统全局版本。

4. 在实际工作区内，以相同账号和环境验证无头调用：

   ```bash
   printf '请概述当前项目，不修改文件\n' | gemini --output-format stream-json --model auto --approval-mode default
   ```

   应返回 `init`、`message`、`result` 等 JSONL 事件。Wio 从 `init.session_id` 保存原生会话 ID，之后使用 `--resume` 恢复；请保留该账号的 Gemini 本地会话文件。

## 使用方式

1. 打开 **Gemini**，侧栏与 Codex 一样只显示已有会话的项目。通过项目右侧的 **⋮** 或右键菜单选择 **新建会话**，直接沿用该项目当前会话的模型和权限创建独立会话；没有会话的项目可通过列表顶部的 **＋** 选择工作区。当前对话顶部也提供快捷新建入口，预选当前项目、模型与权限。首次选择工作区时会自动选择上游返回的首个模型，也可自行切换。
2. 打开会话时自动使用该服务器绑定的 Gemini API 凭据从上游获取模型列表；新建会话选择工作区后也会自动获取，可通过模型旁的刷新按钮手动更新。下拉列表只展示上游实际返回的模型，不补充固定别名；选择“自定义模型”后可输入账号有权访问的模型名称。使用网页下发的 API 配置时，会话选择 `auto` 会使用该配置中的默认模型；配置本身为 `auto` 时由 CLI 自动选择。切换的模型在发送下一条消息时生效。未配置 API 凭据或上游不可用时，可通过自定义名称继续使用；Google 登录和 Vertex AI 认证的模型自动发现暂未提供。
3. 输入任务，按 Enter 发送，Shift+Enter 换行。页面显示回答、工具参数与结果、输入输出 token 数。运行期间可中断；服务器离线时保留历史，恢复连接后可继续。
4. 在左侧查看项目文件和变更；项目分组可折叠，搜索框可按项目、服务器、路径、会话标题或 ID 筛选。会话可重命名、归档、恢复或删除。归档会话只读，删除仅清除 Wio 会话记录，保留项目文件和 Gemini 本地会话。
5. **新建会话**从空上下文开始，不携带其他会话的原生 ID；在原会话中继续发送会使用官方 `--resume <session-id>` 恢复上下文。同一项目可创建多个独立会话，但同一工作区的写任务仍串行执行。
6. 用户消息靠右显示，与 Codex 保持一致。点击用户消息下方的 **编辑并重新发送**，修改输入后发送，会保留该消息之前的上下文，并替换这条消息、舍弃之后的全部对话；发送前可以 **取消编辑**。此功能需要目标 Wio Agent `0.2.66` 或以上版本及 Gemini CLI `0.65.0` 或以上版本。运行中、离线或归档会话不可编辑。通过官方 `--session-file` 导入该消息之前的原生历史，保留已有工具调用；新上下文载入成功后才提交会话替换，准备失败保留原对话；原生会话文件已被清理时无法回退。仅回退对话，已经修改的项目文件不会撤销。
7. 对话顶部的 **导出对话**会将完整 Wio 历史导出为 Markdown，包含早期分页记录、工具输出与用量。输入/输出 token 计数按 CLI 实际统计显示；旧版本已经脱敏且无法恢复的计数显示为 **—**，不会将缺失计数显示成零。

## 权限及限制

- 默认权限遵循服务器的 Gemini 策略；无头模式无法弹出交互审批，需要确认且未被策略允许的操作会被拒绝。
- “允许编辑文件”映射至官方 `auto_edit` 模式，允许自动编辑，命令执行仍遵循 Gemini 策略。Wio 暂不接入 Gemini 的交互式审批，不启用 `yolo`。
- 从 `0.2.64` 起，Agent 校验用户所选工作区的规范路径属于配置允许的根目录后，通过官方 `GEMINI_CLI_TRUST_WORKSPACE=true` 为本次无头调用声明工作区信任，避免 CLI 因无法弹出信任对话框而以退出码 55 结束。这会允许加载该项目的 Gemini 配置、`GEMINI.md`、MCP、Skills 等；不修改全局信任文件，工具权限仍遵循所选审批模式。
- 同一会话同时只运行一个任务；运行中的 Gemini 任务会占用工作区写操作锁。单次执行上限 10 分钟，超时后可在原会话继续。
- 当前列表展示最近 200 个活动或归档会话；对话记录以每批 500 条事件加载，可加载更早记录。页面支持会话链接 `?view=gemini&thread=<Wio 会话 ID>`。
- 此接入使用真实 CLI，不通过 OpenAI 兼容接口转换 Gemini 请求；需要服务器可访问相应 Google 服务。图片输入和跨服务器迁移原生会话暂未提供。安装 CLI 不会自动配置模型认证。

## 项目、分支与定时任务

- 项目菜单提供置顶、重命名、创建永久 worktree、归档项目全部 Gemini 任务和隐藏项目。置顶、名称和隐藏状态属于共享项目，因此 Codex 页面同步生效；批量归档仅处理该项目的 Gemini 会话，跳过运行中的任务。
- 任务菜单提供置顶、重命名、创建定时任务、继续到新任务/新 worktree、归档恢复、复制工作目录、Wio/Gemini 会话 ID、深度链接和新窗口打开。归档会话保留历史和复制入口。
- **继续到新任务**保留原聊天，为新聊天创建独立的原生 Gemini 历史。回答下方菜单的**分支到新聊天**仅支持已成功完成一轮的最终回答，只保留到该轮的历史；工具调用过程中的中间回答不可分支。分支会复制原始 JSONL 历史及补丁记录，交给官方 CLI 的恢复逻辑重建模型上下文，不调用模型生成摘要来替代历史。原生历史缺失时会失败并保留原聊天。需要 Agent **0.2.67+** 和 Gemini CLI **0.65.0**。
- **创建永久 worktree**和**继续到新 worktree**沿用现有 Git 管理，选择新分支、基准引用和可选绝对路径。worktree 从基准提交建立，不复制原工作区未提交或忽略的文件；后者同时复制会话上下文。创建失败时清理本次创建的 worktree；原工作区保留。
- **创建定时任务**可在 Gemini 任务菜单或设置页使用。支持每天、工作日、每周、每月、固定间隔和自定义表达式，按指定 IANA 时区执行。模型留空跟随会话设置，也可从实际上游列表选择或输入自定义模型。权限仅提供 Gemini 的 default / auto_edit，不使用 Codex 的推理强度或权限值。
- 设置页统一管理 Codex/Gemini 定时任务，可编辑、停用、恢复和删除，展示下次执行时间及最近执行状态。任务在原 Gemini 会话继续；离线、归档、会话忙碌或工作区不可用时跳过本次，到下一计划时间再尝试，避免重叠运行。无头模式不会等待网页中的交互授权，需要确认的操作可能被 CLI 拒绝并显示失败结果。

## 官方参考

- [无头模式与流式 JSON 协议](https://geminicli.com/docs/cli/headless/)
- [官方对话回退与代码回退说明](https://geminicli.com/docs/cli/rewind/)
- [会话文件导入实现（v0.65.0）](https://github.com/google-gemini/gemini-cli/blob/v0.65.0/packages/cli/src/gemini.tsx)
- [官方会话管理、保存与恢复](https://geminicli.com/docs/cli/session-management/)
- [CLI 参数及模型别名](https://geminicli.com/docs/cli/cli-reference/)
- [模型列表 API](https://ai.google.dev/api/models)
- [认证配置](https://geminicli.com/docs/get-started/authentication/)
- [工作区信任与无头环境](https://geminicli.com/docs/cli/trusted-folders/)
- [官方 JSONL 事件类型](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/output/types.ts)

- [Gemini CLI API key 与基础地址实现（v0.65.0）](https://github.com/google-gemini/gemini-cli/blob/v0.65.0/packages/core/src/core/contentGenerator.ts)
