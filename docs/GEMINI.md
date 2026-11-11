# Gemini 接入参考

日常会话、Goal、技能、分支与定时任务见[用户手册](USER_GUIDE.md#9-gemini)。本文补充 CLI 认证、兼容版本及运行限制。Wio 使用官方 Gemini CLI 和 ACP，不通过 OpenAI 兼容接口转换请求。

## 安装与兼容版本

SSH 注册/修复会安装最新正式版；已有服务器可在 **设置 → Gemini CLI 版本管理** 选择目标版本，再到 **服务器 → 管理 AI Agent** 安装。网页安装依赖 Node.js/npm，校验后才切换受管版本，失败保留旧选择。

官方控制面镜像已内置 Gemini CLI，但当前 [Dockerfile](../deploy/controlplane.Dockerfile) 基线为 **0.65.0**，使用需要 0.66.0 的能力前须单独更新。Wio 控制面、目标 Agent 和 CLI 三者都应满足门槛。

| 能力 | 最低 Wio Agent | CLI 要求 |
| --- | --- | --- |
| 基础 Gemini 会话 | 0.2.60 | 官方无头 CLI |
| 通用 CLI 安装管理 | 0.2.61 | Node.js/npm |
| 托管 API 凭据 | 0.2.63 | 支持 API Key 与基础地址 |
| 原生分支 | 0.2.68 | 0.65.0+ |
| 思考等级 | 0.2.70 | 0.65.0+ 模型配置 |
| yolo 权限 | 0.2.72 | 支持对应审批模式 |
| ACP 交互输入/审批 | 0.2.73 | 支持 ACP |
| 运行中追加文字 | 0.2.79 | 0.66.0+ |
| 计划与技能发现 | 0.2.83 | 0.66.0+ |
| Goal | 0.2.84 | 0.66.0+ |
| 编辑重发 | 0.2.85 | 0.65.0+ |
| 追加图片、技能管理、Goal 重复保护 | 0.2.88 | 0.66.0+ |
| Wio 会话协作 | 0.3.06 | 支持额外 ACP MCP 服务 |

具体开关以 [buildinfo](../internal/buildinfo/version.go) 和运行时能力检查为准。

## 托管 API 配置

1. **设置 → 服务器凭据预设 → Gemini API**：填写名称、原生 API 基础地址、模型和密钥。官方地址为 `https://generativelanguage.googleapis.com`，模型可用 `auto`。
2. **服务器 → 修改服务器凭据 → Gemini API**：选择预设并应用；等待成功回执，编辑预设后重新应用。
3. 打开 Gemini 会话，刷新模型或填写自定义模型，然后验证实际调用。

Agent 将配置保存为受限权限的 `gemini/api.json`，启动 CLI 时单独注入 `GEMINI_API_KEY`、`GOOGLE_GEMINI_BASE_URL` 和 API 密钥认证设置，不改 Codex/Git 配置或全局进程环境。

基础地址末尾的 `/v1`、`/v1beta` 统一转换为原生 `/v1beta`，保留网关路径前缀，避免 `/v1/v1beta`。模型发现与调用使用同样路径。模型列表只使用最后成功应用的配置；预设更新未下发时要求重新应用，发现失败不会清除当前自定义模型。

## 其他认证方式

未使用网页托管配置时，可按官方 CLI 支持方式配置：

- 以 Agent 运行账号启动 `gemini`，完成 Google 登录；无头任务使用该账号缓存的认证。
- 在 Agent 服务环境或账号的 `~/.gemini/.env` 中配置 Google AI Studio API Key。
- Vertex AI 使用对应 ADC、项目、区域及环境配置。

外部 Agent 默认 Home 为 `/var/lib/wio-agent`；内置 Agent 使用控制面进程环境。systemd 不自动读取 `.bashrc`，服务环境修改需重启，账号、Home、PATH 和文件权限须一致。托管 API 预设的模型自动发现不适用于 Google 登录/Vertex AI，可手工输入可用模型。

以相同账号、环境和实际工作区验证无头调用时，可执行以下命令；它会调用模型：

```bash
printf '请概述当前项目，不修改文件\n' | gemini --output-format stream-json --model auto --approval-mode default
```

CLI 已就绪只说明可找到程序，不代表认证已验证。

## 思考等级与自定义网关

明确等级通过 CLI 原生模型配置别名和每进程环境传入 `thinkingConfig`，不使用 Codex 的推理参数或提示词模拟。Agent 账号的设置保留已有配置和 JSONC 注释，不在项目目录写入生成设置。默认模式不传等级覆盖。

模型名自带 `-minimal/-low/-medium/-high` 等后缀时显示为模型预设，不冒充响应返回的实际等级。Gemini 3 Pro 仅提供 low/high；2.5 使用 thinkingBudget，auto/图片模型不提供同样等级覆盖。

自定义网关的长任务可能因 CLI 内部循环检测调用网关未提供的辅助模型而出现 503。对自定义 API 地址及明确模型，Wio 将 `loop-detection` 和 `loop-detection-double-check` 别名指向当前模型，保留检测和原有生成参数；已有同名别名不覆盖。其他辅助模型和管理员策略仍按 CLI 配置处理，已运行进程不热加载。

## 权限、追加与技能

默认、允许编辑、永不审批对应 `default`、`auto_edit`、`yolo`，仍受系统账号与管理员策略约束。Agent 校验实际工作区在允许根目录内后，为本次无头进程设置 `GEMINI_CLI_TRUST_WORKSPACE=true`，允许加载项目 Gemini 配置、MCP 和技能；不改全局信任文件。

文字追加使用官方 ACP 与 `BeforeModel` hook，在 Agent 账号的 `~/.gemini/settings.json` 添加 `wio-steering`，保留已有 hooks。任务临时脚本和回调凭据结束后清理；管理员禁用 hooks 或尚未就绪时不开放追加。

官方 hook 只支持文字，追加图片通过本轮临时 `wio_read_images` 工具交给模型。只有实际读取才标记送达；本轮累计最多 4 张/4 MiB，单张最多 2 MiB。已读取图片保存为原生图片工具结果；未送达内容不自动成为下一轮任务。

技能管理通过官方 `gemini skills` 执行并重新读取列表核验。HTTPS 安装来源不允许内嵌凭据；本地路径解析后须在当前项目内。账号范围影响该 Agent 账号其他项目。修改期间与同服务器 Gemini 任务、凭据/安装等操作互斥，关闭页面不会撤销已排队操作。

## 原生历史与恢复

Wio 保存原生 session ID 并通过 CLI 恢复上下文；应保留 Agent 账号的 `~/.gemini` 原生会话文件和 Agent 状态目录。原生分支复制 JSONL 历史及补丁记录，编辑重发使用消息前的历史，不通过模型摘要替代。

分支仅支持成功结束轮次的最终回复；原生记录缺失时失败并保留原会话。编辑仅回退对话，不回退文件。跨服务器迁移原生会话尚未提供。

同一会话一次一轮，执行时持有工作区写锁。停止需等待原进程退出；失联且旧进程状态无法确认时，不自动重放或释放锁。Goal 沿用同样的审批、提问和退出确认规则。

## 官方参考

- [无头模式](https://geminicli.com/docs/cli/headless/)
- [认证](https://geminicli.com/docs/get-started/authentication/)
- [CLI 参数](https://geminicli.com/docs/cli/cli-reference/)
- [会话管理](https://geminicli.com/docs/cli/session-management/)
- [工作区信任](https://geminicli.com/docs/cli/trusted-folders/)
- [模型配置](https://geminicli.com/docs/reference/configuration/#modelconfigs)
- [原生模型列表 API](https://ai.google.dev/api/models)
