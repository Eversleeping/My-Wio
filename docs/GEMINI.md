# Gemini 会话

侧边栏 **Codex** 下方的 **Gemini** 页面通过目标服务器上的官方 Gemini CLI 处理项目任务。支持工作区会话、模型选择、流式回答、工具输出、历史续聊、中断、重命名、归档和删除；文件树、文件编辑与 diff 预览沿用工作区功能。Gemini 与 Codex 的会话、模型和事件独立保存。

## 安装和认证

1. 更新控制面及目标服务器的 Wio Agent 至 `0.2.60` 或以上版本。旧版 Agent 不识别 Gemini 操作，页面会要求先升级。
2. 在目标 Linux 服务器上，以运行 `wio-agent` 的账号安装 CLI。需要 Node.js 20 或以上版本，且服务的 `PATH` 能找到 `gemini`：

   ```bash
   npm install -g @google/gemini-cli
   gemini --version
   ```

3. 选择一种认证方式：

   - 以同一 Agent 账号运行 `gemini`，完成 Google 登录。无头模式会使用该账号已缓存的认证。
   - 将 Google AI Studio 的 `GEMINI_API_KEY` 配置到 Agent 的服务环境或该账号的 `~/.gemini/.env` 中。服务环境变化后需重启 Agent；不要将真实密钥写入项目仓库。
   - Vertex AI 用户可按官方文档配置 ADC、项目和区域等环境变量。

   systemd 服务不会自动读取交互式终端的 `.bashrc`。CLI 的账号、`HOME`、凭据及 `PATH` 必须与 Agent 服务一致。内置控制机 Agent 使用控制面进程的环境；官方控制面镜像从 `0.2.60` 起已包含 Gemini CLI，认证目录位于持久化的 Agent 状态卷中。外部 Agent 主机仍需单独安装 CLI。

4. 在实际工作区内，以相同账号和环境验证无头调用：

   ```bash
   printf '请概述当前项目，不修改文件\n' | gemini --output-format stream-json --model auto --approval-mode default
   ```

   应返回 `init`、`message`、`result` 等 JSONL 事件。Wio 从 `init.session_id` 保存原生会话 ID，之后使用 `--resume` 恢复；请保留该账号的 Gemini 本地会话文件。

## 使用方式

1. 打开 **Gemini → 新建 Gemini 会话**，选择现有工作区。
2. 选择模型和工具权限。支持官方别名 `auto`、`pro`、`flash`、`flash-lite`，也可输入账号有权访问的具体模型名称；别名由已安装的 CLI 解析。
3. 输入任务，按 Enter 发送，Shift+Enter 换行。页面显示回答、工具参数与结果、输入输出 token 数。运行期间可中断；服务器离线时保留历史，恢复连接后可继续。
4. 在左侧查看项目文件和变更；会话菜单可重命名、归档、恢复或删除。归档会话只读，删除仅清除 Wio 会话记录，保留项目文件和 Gemini 本地会话。

## 权限及限制

- 默认权限遵循服务器的 Gemini 策略；无头模式无法弹出交互审批，需要确认且未被策略允许的操作会被拒绝。
- “允许编辑文件”映射至官方 `auto_edit` 模式，允许自动编辑，命令执行仍遵循 Gemini 策略。Wio 暂不接入 Gemini 的交互式审批，不启用 `yolo`。
- 工作区信任、`GEMINI.md`、MCP、Skills 和策略由目标服务器上的 Gemini CLI 管理。Wio 只在 Agent 配置允许的工作区根目录中启动 CLI。
- 同一会话同时只运行一个任务；运行中的 Gemini 任务会占用工作区写操作锁。单次执行上限 10 分钟，超时后可在原会话继续。
- 当前列表展示最近 200 个活动或归档会话；对话记录以每批 500 条事件加载，可加载更早记录。页面支持会话链接 `?view=gemini&thread=<Wio 会话 ID>`。
- 此接入使用真实 CLI，不通过 OpenAI 兼容接口转换 Gemini 请求；需要服务器可访问相应 Google 服务。自动安装 CLI、网页管理 Gemini 凭据、图片输入和跨服务器迁移原生会话暂未提供。

## 官方参考

- [无头模式与流式 JSON 协议](https://geminicli.com/docs/cli/headless/)
- [CLI 参数及模型别名](https://geminicli.com/docs/cli/cli-reference/)
- [认证配置](https://geminicli.com/docs/get-started/authentication/)
- [官方 JSONL 事件类型](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/output/types.ts)
