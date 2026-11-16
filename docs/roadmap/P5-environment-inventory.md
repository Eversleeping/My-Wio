# P5-01 环境与权限盘点

返回 [P5 执行隔离](P5-execution-isolation.md)。盘点基线为 `c35a390` / Agent 0.3.27；本文件只记录资源类别、变量名和结论，不包含真实部署的配置或凭据。

## 证据范围

| 检查 | 已取得的证据 | 不能据此认定的事项 |
| --- | --- | --- |
| 内置 Agent 的进程链路 | 给现有 `TestCrossProcessAcceptance` 加临时 Go overlay，让三种假引擎仅记录变量名；三个适配器均继承 `WIO_MASTER_KEY`、`WIO_DATABASE_URL`、`WIO_SETUP_TOKEN` | 没有使用生产控制面、生产配置或真实模型接口 |
| 原生 CLI 与工具环境 | Codex CLI 0.169.0、Claude Code 2.1.323、Gemini CLI 0.68.0；使用临时 HOME、合成密钥和回环模型接口，通过 Wio 适配器触发真实 shell 工具，仅保存变量名 | 不等于三种真实部署形态已验收；不验证服务环境中的真实认证 |
| 权限脚本 | 合成文件、不可读文件、两个临时 UNIX socket、同账号进程；验证不输出内容、变量取值或路径，且文件摘要不变 | 目录的写权限检查不等于一次真实写入成功；不证明沙箱阻止了写入 |
| 服务限制 | 对照 `deploy/agent.service`、`agentServiceUnit` 和控制面镜像/Compose 源码 | 不等于现有服务器正使用同一配置 |

原生 Claude 的对话、追加和编辑分叉检查随环境探测通过。Gemini 首轮的 shell、回环追加和图片检查通过；随后续聊仍报 `Gemini CLI cannot synchronize restored history`，与总览已登记的问题相同。本轮把环境探测限于已经成功的首轮，保留完整测试失败记录，不将续聊计为通过。Codex 通过真实 app-server 发起一轮并执行 shell 工具。

## 变量名与传递结果

下表中的“CLI”指适配器刚启动的进程，“工具”指该 CLI 按本地模型接口响应实际启动的命令。密钥仅使用合成值。

| 变量或类别 | Codex CLI / 工具 | Claude CLI / 工具 | Gemini CLI / 工具 | P5-02 处理 |
| --- | --- | --- | --- | --- |
| `WIO_MASTER_KEY`、`WIO_DATABASE_URL`、`WIO_SETUP_TOKEN` | 均出现 / 均出现 | 均出现 / 均出现 | 均出现 / 均出现 | 全部禁止继承 |
| `WIO_CODEX_API_KEY` | 出现 / 出现 | 本次未注入；合成跨进程基线会继承 | 本次未注入；合成跨进程基线会继承 | 只允许 Codex 适配器显式传入；自身凭据仍是残余风险 |
| `ANTHROPIC_API_KEY`（托管） | 不适用 | 不出现 / 不出现 | 不适用 | 保留 Unix 管道描述符传递 |
| `CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR` | 不适用 | 出现 / 不出现 | 不适用 | 保留 CLI 启动描述符及其释放顺序 |
| `GEMINI_API_KEY`（托管） | 不适用 | 不适用 | 出现 / 出现 | 只放行 Gemini 凭据命名空间；自身凭据仍是残余风险 |
| 其他引擎的探测命名空间（合成标记） | 出现 / 出现 | 出现 / 出现 | 出现 / 出现 | 按引擎筛选父进程的变量 |
| `PATH`、`HOME`、`LANG`、`TMPDIR`、代理变量 | 出现 / 出现 | 出现 / 出现 | 出现 / 出现 | 通用运行环境 |
| `CODEX_HOME`、`CODEX_CI` | 出现 / 出现 | 不适用 | 不适用 | Codex 命名空间 |
| `CLAUDE_CONFIG_DIR`、`ANTHROPIC_BASE_URL`、`CLAUDE_CODE_*` | 不适用 | 出现 / 部分由 CLI 清除，另有 CLI 生成项 | 不适用 | Claude 命名空间 |
| `GEMINI_CLI_HOME`、`GEMINI_CLI_SYSTEM_SETTINGS_PATH`、`GEMINI_CLI_SYSTEM_DEFAULTS_PATH`、`GOOGLE_GEMINI_BASE_URL`、`GEMINI_PTY_INFO` | 不适用 | 不适用 | 出现 / 出现 | Gemini 命名空间 |
| `NODE_OPTIONS` | 不适用 | 本次未注入 | 出现 / CLI 加载后移除 | 保留运行器显式注入的兼容 loader |
| `WIO_GEMINI_LOOP_MODEL`、`WIO_GEMINI_STEER_SCRIPT` | 不适用 | 不适用 | 出现或由 CLI loader 添加 / 出现 | 只允许运行器显式传入；不能从父进程泛继承 |
| `GIT_CONFIG_PARAMETERS`、`GIT_CONFIG_COUNT`、`GIT_CONFIG_KEY_*`、`GIT_CONFIG_VALUE_*` | 前者为 Wio 显式配置；其余本次未注入 | 前者为 Wio 显式配置；其余本次未注入 | shell 增加后一组变量 | 保留显式 Git 身份/凭据配置；不能依赖全局环境修改 |
| `GOCACHE`、`GOMODCACHE`、`GOPATH` 以及测试开关 | 本地测试父进程提供 | 本地测试父进程提供 | 本地测试父进程提供 | 不因探测出现就加入默认白名单；确需时通过追加变量名配置 |

工具自行添加的变量也属于实测结果，但不由 Agent 的继承规则产生：Codex 增加 `CODEX_SESSION_ID`、`CODEX_THREAD_ID`、`CODEX_TOOL_CALL_ID`、`CODEX_VERSION` 和 `CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS`；Claude 增加 `CLAUDE_CODE_SESSION_ID`、`CLAUDE_CODE_CHILD_SESSION`、`CLAUDE_CODE_ENTRYPOINT`、`CLAUDE_CODE_EXECPATH`、`CLAUDE_CODE_MESSAGING_SOCKET`、`CLAUDE_CODE_MESSAGING_TOKEN`、`CLAUDE_CODE_SESSION_ATTENDED`、`CLAUDE_EFFORT`、`CLAUDE_PID` 和 `GIT_EDITOR`；Gemini 增加 `GEMINI_CLI`、`GEMINI_CLI_NO_RELAUNCH`、`GIT_ASKPASS`、`GIT_CONFIG_NOSYSTEM`、`GIT_CONFIG_SYSTEM`、`GIT_PAGER`、`GIT_TERMINAL_PROMPT` 及上表的 Git 参数组。shell 还会添加 `PWD`、`SHLVL`、`_` 等自身运行变量。

CLI 可以从自己的配置、登录缓存和工作区 `.env` 再加载变量。Agent 的白名单只约束启动时从父进程继承的环境，不能阻止 CLI 或 shell 此后自行增加变量。

## 白名单的输入

实现时以公共构造函数为唯一的继承入口。匹配变量名时统一处理大小写，避免小写代理和大小写变体绕过 `WIO_` 禁止继承规则；覆盖顺序保留“显式配置优先”。

| 范围 | 要保留的变量名或命名空间 | 原因 |
| --- | --- | --- |
| 通用账号、执行与临时目录 | `PATH`、`HOME`、`USER`、`LOGNAME`、`SHELL`；Windows 的 `USERPROFILE`、`HOMEDRIVE`、`HOMEPATH`、`SYSTEMROOT`、`WINDIR`、`COMSPEC`、`PATHEXT`、`APPDATA`、`LOCALAPPDATA`；`TMPDIR`、`TMP`、`TEMP` | CLI 查找、账号配置与临时文件；`PWD` 应反映命令的工作目录 |
| 通用语言与显示 | `LANG`、`LANGUAGE`、`LC_*`、`TZ`、`TERM`、`COLORTERM`、`NO_COLOR`、`FORCE_COLOR`、`CLICOLOR`、`CLICOLOR_FORCE`、`CI`、`PAGER` | 语言、终端、无头执行和分页 |
| 通用代理与证书 | `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY`、`NO_PROXY` 及其小写形式；`SSL_CERT_FILE`、`SSL_CERT_DIR`、`REQUESTS_CA_BUNDLE`、`CURL_CA_BUNDLE`、`NODE_EXTRA_CA_CERTS`、`NODE_USE_SYSTEM_CA` | 服务环境已有代理、自定义证书和回环旁路 |
| 通用配置与 Node 运行时 | `XDG_CONFIG_HOME`、`XDG_CACHE_HOME`、`XDG_DATA_HOME`、`XDG_STATE_HOME`、`XDG_RUNTIME_DIR`、`XDG_CONFIG_DIRS`、`XDG_DATA_DIRS`；`NODE_OPTIONS`、`NODE_PATH`、`NODE_ENV`、`NODE_NO_WARNINGS`、`NODE_ICU_DATA` | 自定义 CLI 状态位置；Gemini loader 与 Node CLI 运行 |
| Git 使用者（Git、部署和三个引擎） | `GIT_*`、`GCM_*`、`SSH_AUTH_SOCK`、`SSH_AGENT_PID` | 保留服务环境 Git 配置、身份及已有 SSH/Git 认证；这些变量也可能携带凭据，环境收紧不能代替凭据文件隔离 |
| Codex | `CODEX_*`、`OPENAI_*` | 原生状态与服务环境 API 认证；`WIO_CODEX_API_KEY` 由 Wio 显式提供 |
| Claude | `CLAUDE_*`、`ANTHROPIC_*`；Bedrock 的 `AWS_*`；Vertex 的 `GOOGLE_*`、`GCLOUD_*`、`CLOUDSDK_*`；Foundry 的 `AZURE_*`；`IS_SANDBOX`、`DISABLE_AUTOUPDATER`、`DISABLE_TELEMETRY`、`DISABLE_ERROR_REPORTING`、`ENABLE_TOOL_SEARCH` | 原生登录、API、云供应商认证与已有执行开关；托管预设覆盖相应认证选择；保留管理员的工具发现设置 |
| Gemini | `GEMINI_*`、`GOOGLE_*`、`GCLOUD_*`、`CLOUDSDK_*` | API Key、Google 登录、ADC/Vertex 与自定义原生设置；供应商客户端读取这些命名空间 |
| 部署及依赖安装 | `DOCKER_*`、`COMPOSE_*`、`NPM_CONFIG_*`；大小写形式同样识别 | 保留 Docker/Compose 与 npm 安装配置；不得由三个 AI 引擎继承 Docker 或安装专用环境 |
| 追加配置 | Agent 配置中的变量名列表；不接受通配符、带 `=` 的条目或 `WIO_` 名字 | 为项目工具链及自定义供应商认证保留出口；不允许用出口重新引入控制面变量 |
| 自更新重新执行 Agent | 全部原环境 | 这是 Agent 替换自身，不是模型或辅助子进程，不能套用子进程白名单 |

不默认放行数据库、Vault、部署应用密钥或任意供应商自定义认证名。部署 Vault 变量和适配器托管变量属于调用方显式提供的配置，仍可覆盖默认环境；追加配置要在实际命令启动时读取，不能把值持久化到新的配置字段或修改 Agent 的全局环境。

## 凭据通道的查证

| 通道 | 已查证的原生接口 | 下一步结论 |
| --- | --- | --- |
| Claude 托管 API 密钥 | 已有 `CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR`；真实 CLI 会在启动时读取并清除该环境名 | 保持现有 Unix 描述符方案；Windows 原有环境回退明确记录 |
| Codex API 密钥 | 官方 0.169.0 源码的 provider 支持 `env_key`、配置中的 `experimental_bearer_token` 和命令式认证；没有查到直接读取 API Key 管道描述符的接口 | 不能把配置文件或新版本命令式认证直接当作现有 FD 替代方案；P5-02 记录兼容性与凭据可见性限制 |
| Gemini API 密钥 | 官方 0.68.0 发行源码/认证文档使用 `GEMINI_API_KEY`、`GOOGLE_API_KEY` 或原生凭据缓存；发行 bundle 未发现 API Key 描述符选项 | 保留显式托管变量，不声称模型工具看不到该密钥 |
| Claude 回环 MCP 令牌 | 真实 CLI `--help` 明确支持 `--mcp-config` 读取 JSON 文件，也支持内联 JSON；本地协作往返的匿名管道 `/dev/fd` 探测通过 | P5-02 可接入 Unix 匿名描述符配置，避免令牌出现在参数里；其他平台保留并说明回退 |
| Codex 回环 MCP 令牌 | 官方配置 schema 支持 `env_http_headers`、`bearer_token_env_var` | 当前实际工具会继承凭据环境名；只把参数换成环境会产生新的工具可见通道，不能当作已经保护令牌 |

查证来源：[Codex provider](https://github.com/openai/codex/blob/rust-v0.169.0/codex-rs/model-provider-info/src/lib.rs)、[Codex 配置 schema](https://github.com/openai/codex/blob/rust-v0.169.0/codex-rs/core/config.schema.json)、[Gemini 认证](https://geminicli.com/docs/get-started/authentication/)、[Claude MCP 配置](https://code.claude.com/docs/en/mcp)。原生源码与帮助文本属于产品公开资料，没有读取已安装 CLI 的真实登录或凭据文件。

Claude 描述符实验使用同一回环 MCP 桥接和模型接口：内联配置对照通过（0.728 秒），匿名管道配置通过（0.740 秒）。最新 CLI 默认延迟发现 MCP 工具，原有伪模型用例未处理工具搜索，因此实验用临时 overlay 设置 `ENABLE_TOOL_SEARCH=false` 后比较两个通道；没有改产品的工具发现行为。Codex 官方 `ShellEnvironmentPolicy` 默认 `inherit=All`、`ignore_default_excludes=true`，与实际 shell 出现 `*KEY*`、`*TOKEN*` 名字一致，不能假定 CLI 会自动过滤凭据环境。

## 宽权限用法的影响

| 现有用法 | 依据 | 可选隔离模式的影响 |
| --- | --- | --- |
| 模型自己提交、推送 | 三个引擎运行 shell/Git；Wio 已显式给 Claude/Gemini 提供 Git 身份和凭据配置 | 保留工作区提交；worktree 需放行主仓库 Git 元数据。受限凭据不提供给模型，推送改走现有 Git 面板 |
| 模型使用 Docker | 默认外部 Agent 的附加组以及控制面 Compose socket 挂载 | 模型不能连接 socket；Agent 本身的部署操作继续保留权限 |
| 读取工作区外文件、安装全局技能 | CLI 原生路径和技能管理入口；不同工作区共享 Agent 账号 | 需要明确允许当前引擎的状态和安装目录；跨项目文件通过目标工作区子会话交接 |
| sudo 或系统安装 | SSH 注册的免密 sudo 选项；部署依赖助手的固定动作 | sudo 与隔离互斥；依赖安装由 Agent 完成，会话不能借用助手 socket |
| 网络发送工作区内容、同引擎原生历史共享 | 威胁模型中的残余风险 | 用户在 2026-11-16 已确认接受；P5-05 文档如实说明 |

以上是代码和公开功能的依赖盘点，未读取真实会话内容来推断用户正在采用哪些工作流。

## 权限探测的复用

在目标会话的工具进程中运行 [probe-session-reach.py](../../scripts/probe-session-reach.py)。默认路径对应默认外部 Agent；安装不同或内置控制机需显式提供 `--config-dir`、`--state-dir`、`--home-dir`、`--codex-key` 等路径。Git 同时检查 Wio 状态目录及 HOME 的凭据文件，自定义托管文件可用 `--git-credentials` 指定。原生状态目录被覆盖时提供 `--codex-home`、`--claude-home`、`--gemini-home`；测试另一工作区需提供 `--other-workspace`。检查其他进程时可提供 `--peer-pid`、`--control-plane-pid`，报告不会输出 PID 或路径。

不提供目标路径的项输出 `untested`；不存在的资源输出 `absent`。文件/目录的读检查实际打开并立即关闭，不读取内容；`write_permission` 只检查现有访问权限，不创建或修改文件。socket 检查只连接并关闭，不发送请求。`--environment-names` 仅增加合法变量名，不输出取值。隔离验收另外需要真实写入失败测试，不能把 `write_permission` 结果当作沙箱验收。

环境分组的 `name_present` 只表示变量名存在；用于清除认证的空变量也会显示存在，不据此认定有非空密钥。实际托管密钥的传递结论来自上表中使用合成密钥的 CLI/工具对照。

本轮未在外部 Agent 默认、外部 Agent sudo、真实内置控制机三种部署形态中执行该脚本。按 P5-01 的规则记录为未能测试；本地合成进程与真实 CLI 的结果单独列出，不填成生产实测。
