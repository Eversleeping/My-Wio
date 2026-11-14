# P0 工程基线

返回[路线图总览](README.md)。状态以总览的“进度总表”为准，本文件只写步骤细节和执行记录。

## 现状

以下是基线提交 `0247c1c` 时的状况。各步骤完成后的变化见“执行记录”：P0-02 新增了 CI 工作流，P0-06 新增了 `make verify`。

- 仓库没有 `.github/workflows`，没有 lint 配置。验证靠本地手动运行 `make test`。
- `make test` 依次运行 PostgreSQL 集成测试、前端构建、`go test ./...`、Vitest 和类型检查，但不含 `go vet` 和 E2E。
- Go 构建通过 `go:embed` 嵌入 `web/dist`，干净检出必须先构建前端，否则 `go vet` 和 `go build` 都会失败。
- 版本号写在 `internal/buildinfo/version.go` 的 `Version` 变量里，手动维护。Agent 自更新依赖它判断新旧，所以改了 Agent 代码却没升版本号，服务器就不会更新。
- 提交直接推送到 `main`，没有合并请求流程。CI 在这种流程下是事后告警，本地的 `make verify` 才是推送前的门槛。
- `gofmt -l internal cmd` 当前输出为空，可以直接把格式检查设为门槛。
- 现有测试在进程边界处都是断开的，没有任何测试让真实的 Agent 客户端连上真实的网关。详见 P0-07 的“背景”。

## 目标

1. 每次推送自动运行完整验证，失败有通知。
2. 本地一条命令完成与 CI 相同的检查。
3. “改 Agent 代码必须升版本号”由脚本检查，不再靠记忆。
4. 有一组自动化测试覆盖“控制面 → Agent → 引擎进程”的整条链路，后续改动跨进程行为的步骤不再只靠人工验收。

不在本阶段范围内：自动发布、镜像构建、自动部署。

## 步骤

### P0-01 记录测试基线

**做法**

1. 在干净工作区依次运行“通用验收”里的全部命令，加上 `make test-postgres` 和 `npm --prefix web run e2e`。
2. 把每条命令的结果（通过或失败、耗时、失败用例名）记到本文件的“执行记录”。
3. 如果有既存的失败或不稳定用例，登记到总览的“发现的问题”，不在这一步修。

**验收**

- “执行记录”里有一份完整的基线，包含日期和提交号。

**说明**

后续所有步骤都以这份基线判断“有没有弄坏东西”。既存失败必须先被记录，否则会被误算到后续步骤头上。

### P0-02 GitHub Actions：前端、Go、PostgreSQL 三个作业

**做法**

新增 `.github/workflows/ci.yml`，在推送到 `main`、推送到 `ci/**` 临时验收分支和合并请求时触发，包含三个作业。仓库平时直接推送到 `main`，协调者的验收环境只有 `git push`、没有创建合并请求所需的工具和令牌，因此保留 `ci/**` 触发器，用临时分支完成“故意让测试失败、确认 CI 变红”的验收：

| 作业 | 内容 |
| --- | --- |
| `web` | `npm --prefix web ci`、`npm --prefix web run build`、`npm --prefix web test`、`npm --prefix web run typecheck`；把 `web/dist` 作为制品上传 |
| `go` | 下载 `web/dist` 制品；`go vet ./...`、`go test ./...`、`go build ./cmd/controlplane ./cmd/agent` |
| `postgres` | 下载 `web/dist` 制品；`make test-postgres` |

要点：

- Go 版本取自 `go.mod`（当前 1.22），Node 版本用 22，与 `deploy/controlplane.Dockerfile` 保持一致。
- `web/package-lock.json` 里的包地址指向腾讯云镜像。在 GitHub 的运行器上要先确认 `npm ci` 能否正常安装；如果不能，二选一：给 `npm ci` 加上 registry 相关参数，或者把 lockfile 的地址规范化为官方源。选定后把原因写进工作流注释。
- PostgreSQL 集成测试用 embedded-postgres 下载并启动临时的 PostgreSQL 16，需要非 root 账号和可用的下载源。GitHub 运行器默认满足；给下载目录加缓存以缩短时间。
- Git 相关测试会创建临时仓库并设置身份。如果在 CI 里失败，先检查运行环境是否注入了会覆盖 Git 配置的环境变量。

**验收**

- 一次推送触发三个作业且全部通过。
- 故意提交一个会失败的测试到临时分支，确认 CI 变红。

**风险与回滚**

只新增文件，删除工作流文件即可回滚。

### P0-03 E2E 作业

**做法**

在同一个工作流里增加 `e2e` 作业：安装 Chromium（`npx playwright install --with-deps chromium`），运行 `npm --prefix web run e2e`，失败时上传 Playwright 报告和截图。

E2E 使用模拟 API，不连接真实服务器或模型。如果耗时明显长于其他作业，改为只在 `main` 上运行。

**验收**

- 作业通过；失败时能在制品里看到截图。

### P0-04 格式与静态检查门槛

**做法**

1. 在 `go` 作业里增加 `gofmt -l` 检查，输出非空即失败。调用 `Makefile` 的 `fmt-check` 独立目标，文件范围只在 `Makefile` 里写一处。当前输出为空，不需要先做格式化提交。
2. 评估 `staticcheck`：先在本地跑一遍，把现有告警数量记到“执行记录”。告警少就修完后设为门槛；告警多就只启用不产生告警的检查集，其余留作后续。
3. 评估在 `web/tsconfig.app.json` 里开启 `noUnusedLocals` 和 `noUnusedParameters`：先数一遍现有的违例。数量少就清理后开启；数量多就把数字记到“执行记录”，留到 P3-07 之后再开。目前已知的一处残留见总览的“发现的问题”。
4. 前端暂不引入 ESLint 和 Prettier，原因见总览的决策 D6。

**验收**

- 提交一个未格式化的 Go 文件到临时分支，CI 变红。

### P0-05 版本号护栏脚本

**做法**

新增脚本（建议放在 `scripts/check-agent-version.sh`）：

1. 用 `go list -deps ./cmd/agent` 得到 Agent 依赖的本仓库包目录。这样清单会随代码自动更新，不需要手工维护。
2. 取本次推送范围内改动的文件。合并请求用目标分支作为基准，直接推送用推送前的提交作为基准。
3. 如果改动的非测试 Go 文件落在第 1 步的目录里，而 `internal/buildinfo/version.go` 的 `Version` 没有变化，就失败并打印涉及的文件。
4. 只改测试文件或文档时不触发。

脚本同时接入 CI 和 `make verify`。

**验收**

- 构造三种改动分别验证：只改 `internal/httpapi`（通过）、改 `internal/agent` 但不升版本号（失败）、改 `internal/agent` 且升版本号（通过）。

**说明**

`internal/protocol` 被控制面和 Agent 共同依赖，改它会触发检查，这符合预期。如果出现确实不需要升版本号的情况（例如只改注释），允许在提交信息里写明豁免标记来跳过，标记的具体写法在实现时定下并写进脚本头部注释。

### P0-06 本地统一入口 `make verify`

**做法**

在 `Makefile` 增加 `verify` 目标，内容与 CI 的 `web`、`go` 两个作业一致，并通过独立的 `fmt-check` 目标增加格式检查。版本号护栏由 P0-05 在实现脚本时接入。保留现有的 `make test`。

更新 `README.md` 的“开发与验证”表格，加上 `make verify` 一行。

**验收**

- 干净检出后 `make verify` 一条命令通过。
- README 的命令表与 `Makefile` 一致。

### P0-07 跨进程验收测试

**背景**

| 层 | 现有做法 | 没有覆盖到的 |
| --- | --- | --- |
| 浏览器 E2E | `web/e2e/support/mock-api.ts` 拦截全部 `/api` 请求和 `/api/ws` | 控制面的真实响应 |
| `internal/httpapi` 测试 | 不连接 Agent：用 `PendingOperations` 读出排队的操作，再用 `CompleteOperation` 直接写入结果 | 操作的下发、执行和回传 |
| `internal/agentgateway` 测试 | 用手写的假流（`keepaliveStream` 等）调用 `Gateway.Connect` | 真实的 Agent 客户端和 gRPC 传输 |
| 适配器测试 | Claude 和 Gemini 把测试程序自身当作假 CLI（`WIO_CLAUDE_TEST_CHILD`、`WIO_GEMINI_TEST_CHILD`）；另有默认跳过的“真实 CLI 加本地接口桩”测试 | 适配器之上的整条链路 |

测试代码里没有任何地方启动 gRPC 服务，所以 `agent.Client` 的连接、收发和重连从未和真实网关一起运行过。P2、P3-04、P4 改的正是这条链路，而这些步骤原先的跨进程验收全部是“在一台真实服务器上”人工完成。

**做法**

1. 把 `cmd/controlplane/main.go` 的 `run` 里组装处理器的那一段（gRPC 服务、`httpapi.New`、按内容类型分流的 `h2c` 处理器）提取成一个函数，`run` 改为调用它。这是纯移动，目的是让测试和生产用同一份装配代码。
2. 写一个假引擎程序，三种引擎共用一个可执行文件，按被调用时的程序名决定扮演哪一种。它只实现最小的协议：Claude 的 stream-json 一轮对话，Gemini 适配器当前使用的模式，Codex 的 app-server。先看 Claude 和 Gemini 适配器测试里已有的假 CLI 能复用多少；Codex 没有找到可以直接复用的假 app-server，这是这一步里工作量最大的部分。
   - 假引擎做成独立的小程序，由测试在开始时编译，不要做成“测试程序自身”的模式。原因有两个：P0-08 要让控制面的可执行文件也能用它；P5-02 之后子进程不再继承 `WIO_` 开头的变量，靠环境变量切换模式的假 CLI 会失效。
   - 假引擎的行为由提示词里的约定关键字决定，例如“正常回复”“挂起等待中断”“记录自己的环境变量名到指定文件”。
3. 新增测试（建议放在 `cmd/controlplane`，可以直接调用第 1 步的函数）。每个测试在临时目录里启动：SQLite 数据库、第 1 步的处理器（监听 `127.0.0.1:0`）、一个真实的 `agent.NewClient(...).Run`（接入方式与内置控制机 Agent 相同：`EnsureControlPlaneServer` 加 `controlPlaneAgentConfig` 的做法），以及一个临时 Git 仓库作为工作区。Agent 的状态目录、克隆目录、扫描目录和三种引擎会使用的用户级配置目录全部指向临时目录。
4. 让 Agent 找到假引擎：Codex 通过 `Config.CodexPath`；Claude 和 Gemini 在没有受管版本时按名字从 `PATH` 查找，把假引擎所在目录放到 `PATH` 最前面。动手前确认这一点，如果查找顺序与此不同，以代码为准。
5. 用例全部只通过 HTTP 接口操作和断言，不直接读写数据库：
   - 完成首次设置和登录，内置服务器显示在线。
   - 三种引擎各一次：创建会话、发起一轮、从事件接口读到假引擎的回复、会话回到空闲。
   - 三种引擎各一次：假引擎挂起时中断，轮次以中断状态结束。Claude、Gemini 的单轮 CLI 进程已退出；Codex 的长驻 app-server 保持运行并可继续处理下一轮，但其中不再有活动 turn。
   - 一个非会话操作：读取工作区文件列表或 Git 状态。
   - 断线重连：停掉 Agent 再启动一个新的，断线期间排队的操作在重连后执行并返回结果。
6. 测试只在 Linux 上运行，其他平台跳过并写明原因。不访问网络，不使用任何真实凭据或模型。
7. 尽量不为了可测试性去改 Agent 依赖的包。确实需要时，按改动纪律在同一提交里升级 `buildinfo.Version`。

**验收**

- 新测试包含在 `go test ./...` 里并通过；用 `-count=20` 连续运行没有偶发失败。把单次耗时记到“执行记录”，目标是 30 秒以内。
- 做一次反向验证：临时把 Agent 一侧某个操作类型字符串改错，确认新测试失败，而原有测试不失败。验证后还原，把结果记到“执行记录”。
- CI 的 `go` 作业通过。

**说明**

- 它和 P2-01 契约测试的分工：契约测试用现有的方式（直接写入操作结果）覆盖三种引擎在 HTTP 层的全部行为，求广；这一步只覆盖少数几条路径，但每条都真的穿过 gRPC、Agent 和子进程，求深。
- 它不能替代真实服务器上的验收。假引擎只能证明 Wio 自己的链路是通的，证明不了真实 CLI 的行为。后续步骤里“在一台真实服务器上”的验收项保留；改动跨进程行为的步骤（P2-09、P4-02、P4-05、P5-02、P5-04）另外写明了要在这里增加的用例。
- 过程中发现的缺陷登记到总览的“发现的问题”，不在这一步修。

**风险与回滚**

第 1 步改了 `cmd/controlplane` 的非测试代码，单独一个提交，可以单独回滚。其余都是新增的测试文件。主要风险是测试不稳定：所有等待都用“轮询到条件满足或超时”，不要用固定时长的睡眠。

### P0-08 浏览器冒烟测试接入真实链路

2026-11-15 执行前盘点（基线 `962262e`）：P0-03 的本地实现已提交，托管 CI 仍待推送授权；依用户持续推进 M1 的指示，先实现并本地验证本步骤，远端验收继续保留。控制面已支持 `127.0.0.1:0` 并输出实际监听地址，因此使用 Playwright global setup 启动服务并把地址传给 worker，返回 teardown 负责退出与临时目录清理，不预占固定端口。两组浏览器报告使用不同目录，避免后运行的测试覆盖前一组失败诊断。

**做法**

1. 写一个启动脚本：编译控制面和 P0-07 的假引擎，在临时目录里启动控制面的可执行文件。控制面自带内置 Agent，所以只需要通过环境变量把数据库、Agent 的状态目录、克隆目录、扫描目录指到临时目录，把 `WIO_CONTROL_AGENT_CODEX_PATH` 和 `PATH` 指到假引擎。需要的变量以 `cmd/controlplane/main.go` 为准，全部使用测试专用的值。
2. 新增一份独立的 Playwright 配置和目录（建议 `web/playwright.real.config.ts`、`web/e2e/real/`），`webServer` 指向第 1 步的脚本，不加载 `support/mock-api.ts`。在 `web/package.json` 增加 `e2e:real` 脚本。现有的 `e2e` 不变。
3. 用例只要一条冒烟路径：首次设置、登录、看到内置服务器在线、在临时工作区创建一个会话、发送消息、看到假引擎的回复。三种引擎都跑一遍；如果某种引擎的界面前置条件太多，先覆盖能覆盖的，并在“执行记录”里写明缺口。
4. 在 CI 的 `e2e` 作业里增加这一组，失败时同样上传报告和截图。

**验收**

- `npm --prefix web run e2e:real` 在本地和 CI 通过；原有的 `e2e` 结果不变。
- 把耗时记到“执行记录”。明显拖慢 CI 时改为只在 `main` 上运行。

**说明**

界面细节仍然由模拟 API 的 E2E 负责，这一组只保持在冒烟的规模。它证明的是前端使用的接口形状和控制面真实返回的一致，这一点模拟 API 的测试证明不了。P0-07 是必须的，这一步是在它之上的延伸：如果实际成本明显超出预期，记录原因并与用户确认是否改为 `已取消`。

## 阶段完成标准

- 进度总表里 P0 的八个步骤全部 `已完成` 或 `已取消`。
- 连续三次真实推送的 CI 结果与本地 `make verify` 一致。

## 执行记录

按时间顺序追加，每条包含日期、步骤编号、会话标识、做了什么、验收结果、遗留事项。

### 2026-11-13 P0-01

- 会话：`11-13 Gemini` 完成第一遍取样后会话中断，未能写记录；`11-13 Codex` 在独立干净工作区重跑并记录。
- 提交：`7be5a96`。相对路线图基线 `0247c1c`，`git diff --name-only 0247c1c HEAD` 只有文档和 `.gitignore`，没有产品代码、测试或构建配置变化，因此本记录同样适用于 `0247c1c`。
- 工具版本：Go 1.22.12、Node.js 22.20.0、npm 10.9.3。
- 运行方式：4 核机器上按表中顺序串行运行。耗时是命令的墙钟时间，不用于设置 CI 超时。

| 命令 | `11-13 Gemini` 第一次取样 | `11-13 Codex` 第二次取样 | 关键统计 |
| --- | --- | --- | --- |
| `npm --prefix web ci` | 通过，7 秒 | 通过，6 秒 | 安装 552 个包 |
| `npm --prefix web run build` | 通过，21 秒 | 通过，23 秒 | 两次 `check:bundle` 均通过；最大 JavaScript chunk 321.74 kB，小于 500.00 kB 限制 |
| `go vet ./...` | 通过，5 秒 | 通过，6 秒 | 无输出 |
| `go test ./...` | 失败，226 秒；相同环境立即复跑仍失败，3 秒（缓存命中） | 通过，236 秒 | 第一次：28 个包通过、3 个包无测试文件、2 个包失败；第二次：30 个包通过、3 个包无测试文件、0 个包失败。第一次失败完全由 Git 配置环境引起，见下文 |
| `npm --prefix web test` | 通过，58 秒 | 通过，61 秒 | 两次均为 61 个测试文件、449 个用例全部通过 |
| `npm --prefix web run typecheck` | 通过，13 秒 | 通过，13 秒 | 无诊断 |
| `make test-postgres` | 通过，16 秒 | 通过，17 秒 | 两次 `TestPostgresMigrationAndStorage` 均通过 |
| `npm --prefix web run e2e` | 通过，62 秒 | 通过，60 秒 | 两次均为 44 个 project 用例：33 通过、0 失败、11 跳过、0 不稳定 |

Go 全量测试中耗时最久的包如下。第一次依次为 `internal/httpapi` 210.522 秒、`internal/store` 194.439 秒、`internal/agentgateway` 117.579 秒、`internal/scheduler` 17.610 秒；第二次依次为 221.609 秒、204.845 秒、123.908 秒、19.045 秒。

E2E 按 project 的结果两次相同：`desktop-chromium` 共 22 个，21 通过、1 跳过；`mobile-chromium` 共 22 个，12 通过、10 跳过。Playwright 配置明确声明桌面和移动两个 project；用例代码按 project 显式跳过 1 个仅移动端覆盖的响应式导航用例，以及 10 个仅桌面端覆盖的导航、部署、服务器、工作区和 Codex 流程用例。因此 11 个跳过均为有意跳过，不是运行条件缺失。

测试规模基线：

| 测试集 | 数量 | 统计方式 |
| --- | --- | --- |
| Go 默认构建 | 852 个顶层测试函数 | `go test -list '^Test' ./...`，只统计以 `Test` 开头的列举结果 |
| `internal/store` | 默认 168 个；`-tags=postgresintegration` 时 170 个，增加 2 个 | 分别列举该包的默认和 PostgreSQL 标签构建 |
| Vitest | 61 个测试文件、449 个用例 | 取 Vitest 报告 |
| Playwright | 44 个 project 用例 | 取 Playwright 报告；22 个源用例分别在两个 project 下展开 |

总览的“约 846 个 / 约 336 个”与实际统计口径不一致：Go 实际为 852 个，比近似值多 6 个；Vitest 实际执行 449 个，比近似值多 113 个。Vitest 报告会展开参数化和生成的用例，不能用源码中的测试调用次数替代。总览是否改为精确的 runner 数字由协调者处理。

Git 配置环境导致的失败：

1. Wio Agent 为 Claude 和 Gemini 会话构造的 `GIT_CONFIG_PARAMETERS` 会注入 Git 身份和凭据助手配置。清除全部 `GIT_CONFIG*` 后，仅加入一份假的 Git 身份，`go test -count=1 ./internal/gitrepository ./internal/scanner` 可重复得到以下结果：`internal/scanner` 通过，`internal/gitrepository` 的 `TestCreateREADMEUsesConfiguredGitIdentityWhenOptionsAreEmpty`、`TestCreateREADMERejectsMissingGitIdentity`、`TestListCommitsPaginatesAndReturnsStructuredMetadata` 失败。原因是进程级配置覆盖了测试创建的身份或“缺少身份”前置条件。第二次取样的 shell 没有 `GIT_CONFIG*` 注入，Go 全量测试通过。
2. Gemini CLI 的 shell 还会注入 `GIT_CONFIG_COUNT`、`GIT_CONFIG_KEY_n`、`GIT_CONFIG_VALUE_n`、`GIT_CONFIG_NOSYSTEM`、`GIT_CONFIG_SYSTEM` 等变量。第一次取样已经单独清除 `GIT_CONFIG_PARAMETERS`，但仍有三个用例稳定失败：`internal/gitrepository.TestInspectionDoesNotRunRepositoryPrograms`、`internal/gitrepository.TestMutationsKeepHooksOnlyForOwnedRepositories`、`internal/scanner.TestDiscoverDoesNotRunRepositoryPrograms`；立即复跑结果相同。清除全部 `GIT_CONFIG*` 后，两个受影响包通过。另用只覆盖 fsmonitor 和 hooks 路径的 `GIT_CONFIG_COUNT` 配置做机制验证，也准确复现这三个失败；这是失败机制验证，不是对 Gemini CLI 完整环境的还原。

后续在 Wio 启动的 Claude、Gemini 会话中运行 Go 测试前，应清除全部 `GIT_CONFIG*` 变量，至少包括 `GIT_CONFIG_PARAMETERS`、`GIT_CONFIG_COUNT`、所有 `GIT_CONFIG_KEY_n` / `GIT_CONFIG_VALUE_n`、`GIT_CONFIG_NOSYSTEM` 和 `GIT_CONFIG_SYSTEM`。只清除 `GIT_CONFIG_PARAMETERS` 不足以得到干净测试环境。

既存失败或不稳定用例：干净 Git 配置环境下没有。两次前端测试和 E2E 结果一致；Go 的差异均已由可重复的环境注入解释。

遗留事项：Git 相关测试应在测试进程内隔离或显式清除继承的 Git 配置环境，避免开发会话和 CI 的外部配置改变测试前置条件；本步骤只记录，不修改测试。协调者还需在总览“发现的问题”登记该项，并决定是否把总览的测试规模近似值更新为上述 runner 数字。

### 2026-11-13 P0-02

- 会话：`11-13 Codex`，分支 `roadmap/p0-02` 的独立 worktree。
- 新增 `.github/workflows/ci.yml`，触发范围为推送到 `main`、推送到 `ci/**` 临时验收分支和所有合并请求。根级权限只有 `contents: read`。合并请求按源分支分组，新提交取消同一合并请求的旧运行；推送按每次运行的 ID 独立分组，确保 `main` 上每次推送都运行完成，不会遗漏 P0-05 后续按推送范围执行的检查。
- `web` 使用 Node 22 和 `web/package-lock.json` 对应的 npm 缓存，依次安装、构建、运行 Vitest 和类型检查，再上传保留 1 天的 `web-dist` 制品。lockfile 当前 538 条 `resolved` 指向腾讯云镜像、64 条指向官方源；GitHub 托管运行器已确认普通 `npm ci` 可以安装，耗时 53 秒，因此不加 registry 参数也不改写 lockfile。如果以后镜像在运行器上不可达，备选是指定官方源并加 `--replace-registry-host=always`。
- `go` 和 `postgres` 都显式依赖 `web`，把 `web-dist` 下载到 `web/dist` 后再验证；Go 版本由 `go.mod` 读取。`go` 运行 vet、全量测试和两个命令的构建；`postgres` 保持运行现有的 `make test-postgres`，没有修改 Makefile。
- 从 embedded-postgres v1.34.0 源码确认，默认压缩包缓存目录实际是 `~/.embedded-postgres-go`，当前 `V16` 是 16.9.0。缓存键包含运行器 OS、架构、PostgreSQL 精确版本和 `go.sum` 哈希，避免跨平台、跨二进制版本或依赖版本复用。
- 通过 `git ls-remote --tags` 核实并采用当时可用的最新主版本：`actions/checkout@v7`、`actions/setup-node@v7`、`actions/setup-go@v7`、`actions/upload-artifact@v7`、`actions/download-artifact@v8`、`actions/cache@v6`。超时按 P0-01 基线和冷缓存余量设置为 `web` 15 分钟、`go` 30 分钟、`postgres` 15 分钟。
- GitHub 托管运行器不会注入 P0-01 所述的 `GIT_CONFIG*`，工作流不做防御性清理，避免掩盖测试对外部环境隔离不足的问题。

本地从没有 `web/node_modules` 和 `web/dist` 的状态开始，使用 Go 1.22.12、Node 22 和 npm 10，按作业顺序串行验证。

| 命令 | 结果 | 耗时与说明 |
| --- | --- | --- |
| `actionlint .github/workflows/ci.yml` | 通过 | 不足 1 秒；语法、表达式和工作流结构无诊断 |
| `npm --prefix web ci`（本机会话指定的安装参数） | 通过 | 7 秒；安装 552 个包 |
| `npm --prefix web run build` | 通过 | 35 秒；`web/dist` 正常生成，bundle 检查通过 |
| `npm --prefix web test` | 最终通过 | 无其他重任务并发时 62 秒；61 个文件、449 个用例全部通过。前两次完整运行分别在 114 秒和 110 秒后因 2 个、1 个用例触发 5 秒超时；两次都观察到同机另一个会话正在运行 Go 全量测试或 Vitest，4 核负载明显超过核数。两个超时文件定向复跑 26 秒全部通过 |
| `npm --prefix web run typecheck` | 通过 | 21 秒；无诊断 |
| `go vet ./...` | 通过 | 8 秒；无输出 |
| `go build ./cmd/controlplane ./cmd/agent` | 通过 | 2 秒；随后检查确认仓库里没有生成 `controlplane` 或 `agent` 文件 |
| `make test-postgres` | 通过 | 25 秒；按现有目标只运行 `TestPostgresMigrationAndStorage` |

GitHub 托管运行器上的验收由协调者推送三个 `ci/` 临时分支完成，运行记录保留在仓库的 Actions 页面。本地没有运行 `go test ./...`，它由 P0-01 的基线和下面的托管运行覆盖。

| 运行编号 | 分支与内容 | 结论 | 各作业结果与耗时 |
| --- | --- | --- | --- |
| 43435112844 | `ci/p0-02-probe`：第一版工作流，与最终版只差并发分组和 lockfile 注释 | 通过 | `web` 通过，143 秒（`npm ci` 53 秒、构建 17 秒、Vitest 55 秒、类型检查 10 秒）；`go` 通过，121 秒（`go vet` 44 秒、`go test` 61 秒、构建 1 秒）；`postgres` 通过，56 秒（`make test-postgres` 39 秒） |
| 43437776090 | `ci/p0-02-fail`：在第一版上加入一个必定失败的 Go 测试 | 失败 | `go` 失败，120 秒：`go vet` 通过，“Test Go packages”一步失败，其后的构建步骤被跳过；`web` 通过，133 秒；`postgres` 通过，66 秒 |
| 43443972352 | `ci/p0-02-final`：本步骤最终提交的工作流 | 通过 | `web` 通过，157 秒（`npm ci` 59 秒、构建 16 秒、Vitest 58 秒、类型检查 10 秒）；`go` 通过，124 秒（`go vet` 39 秒、`go test` 66 秒、构建 1 秒）；`postgres` 通过，61 秒（`make test-postgres` 40 秒） |

两条验收项都满足：一次推送触发三个作业且全部通过；临时分支上的失败测试让 CI 变红。三个临时分支在验收后删除，失败测试没有进入 `main`。托管运行器上 `go test ./...` 约 1 分钟，明显短于 P0-01 在本机测得的 4 分钟左右，`go` 作业 30 分钟的超时余量充足。

合入后 `main` 上的第一次运行是 43458889003（提交 `8508e27`），三个作业通过：`web` 121 秒，`go` 102 秒，`postgres` 56 秒。

### 2026-11-14 P0-07

- 会话：`11-13 Codex`。
- 把 `cmd/controlplane/main.go` 中 gRPC 服务配置与注册、`httpapi.New` 和 h2c 分流原样移入 `controlPlaneHandler`，`run` 改为调用它。新增独立编译的三引擎假程序和 `TestCrossProcessAcceptance`；测试从 HTTP 完成首次设置、登录和全部业务操作，真实穿过 h2c/gRPC、`agent.Client`、网关与子进程。
- 假引擎只实现用例所需的最小协议：Claude stream-json 的 init、消息和 result；Gemini ACP 的 initialize、session new/load、历史回放栅栏、prompt、消息更新和结果；Codex app-server 的 initialize、thread start/resume、turn start/interrupt、消息和完成通知。三者都支持挂起后中断；Gemini 未被用例覆盖的非 ACP 分支已删除。
- 用例覆盖三种引擎的创建会话、正常回复、事件读取、回到空闲和挂起后中断；Claude、Gemini 验证单轮进程退出，Codex 验证当前 turn 清除、长驻 app-server 存活且可继续完成下一轮。另覆盖工作区文件列表操作和 Agent 断线期间排队后的重连执行。
- 假引擎编译完成后，测试把 `HOME`、三种引擎的用户配置位置、XDG 配置/缓存/数据/状态/运行时目录和临时目录全部指向测试目录。清理顺序是 Agent、HTTP/gRPC、数据库、环境恢复、测试根目录。Agent 操作 goroutine 可能迟到写入回执，网关连接清理和未 join 的接收/处理 goroutine 也可能迟到写入 SQLite；根目录清理因此会在 15 秒截止前重试 `Stat` 和 `RemoveAll`，中间错误只记录，直到目录连续 500 毫秒不存在才成功，仅在截止时报告最后一次错误。
- 断线重连的最后一次结果等待放宽为 40 秒：排队操作通常在新连接上立即执行；如果它竞态地在旧连接上被标记为已下发，`PendingOperations` 在 30 秒后才会使其可重发，网关轮询最多再等约 2 秒。`/api/servers` 的 90 秒心跳宽限不能作为旧连接已移除的信号。
- 第二轮曾把测试 HTTP 客户端超时从 3 秒放宽为 10 秒，但这只是对锁等待的误判，没有解决根因，本轮已恢复为 3 秒。实际原因是 modernc.org/sqlite v1.31.1 在查询取得首行后遇到 context 取消时可以丢弃结果集而不关闭，遗留的读事务在 rollback-journal 模式下会永久阻塞后续写入。HTTP 没有暴露 Agent 出站队列或网关接收序号，而心跳和指标可在任意 15 秒边界再入队，因此不存在只靠 HTTP 就能确定消息已静默的栅栏。
- 测试库改用 WAL 模式隔离上述已登记缺陷。WAL 读者读取快照，写者追加 WAL，因此即使驱动遗留了读事务，也不会阻塞断线后的写入；这是锁模型上的确定性隔离，不依赖时序概率、POST 重试或更长超时。代价是这条跨进程用例不再覆盖生产默认 rollback-journal 模式下的取消行为；该行为属于另行跟踪的驱动缺陷，P0-07 仍专注 HTTP、gRPC、Agent 和子进程链路。
- 计划修正：P0-07 做法补充了用户级配置目录隔离；Codex `app-server` 是会话池持有的长驻进程，中断只结束当前 turn，因此原来“三种引擎进程都退出”的表述已改为 Claude、Gemini 进程退出，Codex app-server 无活动 turn 且可复用。

| 验收项 | 结果 | 墙钟耗时 |
| --- | --- | --- |
| `gofmt -l cmd/controlplane` 与 `git diff --check` | 退出码 0，均无输出；两个未跟踪 Go 文件的 `git diff --no-index --check` 亦无空白错误输出 | 小于 1 秒 |
| `go vet ./...` | 退出码 0，无输出 | 2 秒 |
| `go test ./cmd/controlplane -count=1` | 退出码 0；包内 4.257 秒 | 6 秒 |
| `go test -v ./cmd/controlplane -run '^TestCrossProcessAcceptance$' -count=20` | 退出码 0；20 次单次 2.11–2.42 秒 | 47 秒 |
| 外部空 `HOME` 下运行新测试 | 退出码 0；外部主目录仍为空，真实 Gemini 设置文件修改时间未变 | 5 秒 |
| 注入无害 Git 配置变量后运行新测试 | 退出码 0；包内 3.487 秒 | 5 秒 |
| `go test ./...` | 退出码 0；`cmd/controlplane` 4.475 秒，其余部分包显示缓存命中 | 9 秒 |
| 独立空临时目录下第一轮 `go test -race -v ./cmd/controlplane -run '^TestCrossProcessAcceptance$' -count=40` | 退出码 0，无 data race；40 次单次 4.87–5.92 秒，断线排队失败和 401 均为 0，结束后临时目录为空 | 212 秒 |
| 独立空临时目录下第二轮同上命令 | 退出码 0，无 data race；40 次单次 4.79–5.56 秒，断线排队失败和 401 均为 0，结束后临时目录为空 | 206 秒 |
| overlay 反向验证 | 退出码 1，只在 Claude 中断状态断言处失败；无根目录清理错误，结束后临时目录为空、假引擎进程数为 0 | 墙钟 358 秒（含等锁），测试自身 11.750 秒 |
| 网关已下发操作重连定向测试 | 退出码 0；用回拨 31 秒的已下发时间确定性覆盖重发 | 2 秒 |

- 反向验证：用 `go test -overlay` 临时把 Agent 分发侧的 `"claude.turn.interrupt"` 改成错误值。新测试只在 Claude 中断状态断言处按预期失败，没有 `remove acceptance root` 或截止超时错误；结束后专用临时目录为空，没有假引擎残留进程，overlay 文件已清理。
- Agent 依赖边界：没有修改 `go list -deps ./cmd/agent` 中的包，因此没有升级 `internal/buildinfo/version.go`。
- 遗留事项：一是 Agent 连接循环以 goroutine 执行操作，`Client.Run` 返回时不等待仍在执行的操作 goroutine，连接取消后仍可能在 `saveOperationResult` 中写入 `StateDir/operation-results`。二是控制面收尾不等待网关处理函数和接收 goroutine：`Gateway.Connect` 启动 `receiveWithTools` 后不 join，`removeConnection` 又用后台 context 写离线状态，因而 `Store.Close()` 返回后仍可能有 SQLite 写入落盘。三是 modernc.org/sqlite v1.31.1 在已取得首行时遇到 context 取消，可能不关闭结果集就丢弃它，导致 rollback-journal 模式的读锁在进程生命期内不释放。测试用可重试的根目录清理和 WAL 模式隔离这些已登记缺陷，本步骤不改 Agent、控制面或 SQLite 驱动行为。
- 协调者复验（`11-14 Claude`，在 `main` 加本步骤改动的独立副本上运行）：`gofmt`、`go vet ./...` 和两个命令包的编译通过；`go test ./... -count=1` 30 个包通过，其中 `cmd/controlplane` 5.5 秒；`-count=20` 全部通过，单次 2.09 到 2.30 秒；在空的 `HOME` 和专用临时目录下运行后两个目录都是空的，运行账号真实的 Gemini 设置没有被改动；反向验证只在 Claude 中断状态断言处失败，`internal/agent` 原有测试不失败；`-race -count=40` 全部通过，没有 data race，临时目录为空。改用 WAL 之前，同一条命令三轮共 110 次迭代失败 15 次。

### 2026-11-14 P0-06

- 会话：`11-13 Gemini` 完成实现和第一次验收；`11-14 Codex` 基于提交 `0e427dc` 接手，修正格式检查的错误处理并重新完成验收。Gemini 的一次完整取样通过，耗时 444 秒。
- 做了什么：
  - `Makefile` 新增 `fmt-check` 和 `verify` 两个伪目标。`verify` 先运行 `fmt-check`，再依次运行 CI `web` 作业的 `npm ci`、前端构建、Vitest、类型检查，最后运行 CI `go` 作业的 `go vet ./...`、`go test ./...` 和控制面及 Agent 的编译检查；保留原有的 `test`、`test-postgres` 等目标。
  - `fmt-check` 检查 `cmd`、`internal` 和 `web/embed.go`。它只调用一次 `gofmt -l`，先检查命令状态；命令成功且输出非空时再打印文件名并失败。因此未格式化文件、`gofmt` 自身出错、找不到 `gofmt` 三种情况都会失败，同时不会重复维护文件范围。
  - `README.md` 的命令表新增推送前门槛，并逐行核对已有说明；`make test` 等各行与对应目标一致。`docs/roadmap/P0-engineering-baseline.md` 明确 P0-04 复用 `fmt-check`，P0-05 在实现版本号护栏时把它接入 `verify`。
- 验收环境：4 核；完整运行前 `web/node_modules` 和 `web/dist` 均不存在，环境中没有 `GIT_CONFIG*` 变量。
- 完整验收：`make verify` 一条命令通过，退出码 0，墙钟时间 361.63 秒。`npm ci` 安装 552 个包；前端构建转换 1914 个模块并通过 bundle 检查；Vitest 的 61 个文件、449 个用例全部通过，runner 报告 64.57 秒；类型检查和 `go vet` 无诊断。Go 测试为 30 个包通过、3 个包无测试文件；最慢的包是 `internal/httpapi` 227.143 秒、`internal/store` 209.352 秒、`internal/agentgateway` 126.090 秒、`internal/scheduler` 19.163 秒。最后对两个 Go 命令包的编译检查通过，没有在仓库中留下可执行文件。
- 格式检查反向验证：
  - 临时加入语法正确但未格式化的探测文件，`make fmt-check` 打印 `internal/buildinfo/zz_gofmt_unformatted_probe.go`，退出码 2，耗时 0.27 秒。
  - 临时加入有语法错误的探测文件，`gofmt` 打印 `internal/buildinfo/zz_gofmt_syntax_error_probe.go:3:34: expected '}', found 'EOF'`，`make fmt-check` 退出码 2，耗时 0.22 秒。
  - 在裁剪后的 `PATH` 下先运行 `command -v gofmt`，确认退出码 127 且无输出；随后 `make fmt-check` 打印 `gofmt: not found`，退出码 2，耗时不足 0.01 秒。
  - 删除探测文件后，`make fmt-check` 退出码 0，耗时 0.20 秒。另在未格式化探测文件存在时运行 `make verify`，它打印 `internal/buildinfo/zz_gofmt_verify_stop_probe.go` 并在首个目标以退出码 2 结束，耗时 0.18 秒；没有执行 npm 或后续 Go 命令，也没有生成 `web/node_modules` 或 `web/dist`。所有探测文件均已删除。
- 其他检查：`git diff --check` 和 `make -n verify` 均以退出码 0 在不足 0.01 秒内完成；展开的命令顺序符合上述定义。`git ls-files '*.go'` 中不在 `cmd/`、`internal/` 下的文件只有 `web/embed.go`，因此范围覆盖全部被跟踪的 Go 文件，又不会扫描 `web/node_modules`。
- 遗留事项：P0-05 实现版本号护栏后仍需接入 `make verify`；P0-04 在 CI 中调用 `fmt-check`。高负载下 Vitest 用例可能超时的问题已经登记在路线图总览的“发现的问题”中。

### 2026-11-15 P0-07 最终验收

- 会话：`11-15 Codex`，续验用户指定的 Codex 会话 `01a1d50b-3a21-7482-80c3-0b136bb342e5`。会话查询返回资源不存在，因此依据仓库执行记录、实现提交和实际 CI 结果续验。
- 验收对象：已在 `main` 的 `7ece208`（装配函数纯移动）和 `65b5753`（跨进程测试、假引擎及记录）；独立副本基于当前 HEAD `42c16ad`，从实现提交到该 HEAD 只有文档变化。未把主工作区其他未提交的产品改动带入副本。
- 代码审阅：装配函数与生产共用，三种引擎的正常回复和中断、工作区文件操作、断线排队后重连均经过真实 HTTP、h2c/gRPC、Agent 与假引擎进程。未发现新的阻塞项；原有 WAL 隔离与退出清理限制仍按上一条 P0-07 记录和总览“发现的问题”跟踪。

| 验收项 | 实际结果 |
| --- | --- |
| 独立副本 `npm --prefix web run build` | 退出码 0，25.02 秒；类型检查、构建和 bundle 检查通过 |
| 独立副本 `go test -race -v ./cmd/controlplane -run '^TestCrossProcessAcceptance$' -count=20` | 退出码 0，20 次全部通过，无 data race；单次 4.89–5.71 秒，包内总耗时 108.418 秒，含初次编译的墙钟时间 171.65 秒；结束后专用临时目录为空 |
| 实现提交 `65b5753` 的 [CI 43572018275](https://github.com/Eversleeping/My-Wio/actions/runs/43572018275) | Web、Go、PostgreSQL 三个作业全部成功；Go 作业中的 vet、全量测试和两个命令的编译逐项通过 |
| 当前 HEAD `42c16ad` 的 [CI 43601149009](https://github.com/Eversleeping/My-Wio/actions/runs/43601149009) | Web、Go、PostgreSQL 三个作业全部成功；覆盖通用验收的前端构建、Vitest、类型检查、`go vet ./...`、`go test ./...`，以及 `make test-postgres` 和两个命令的编译 |

- 反向验证沿用上一条 P0-07 记录中已经完成的 overlay 实测：改错 Agent 的 Claude 中断操作类型，新测试失败而原有 Agent 测试通过，临时目录和假引擎均清理完毕。实现代码此后未变，本轮未重复该项，也未重复 CI 已验证的全量检查。
- 结论：P0-07 的验收项全部满足，总表改为 `已完成` 并填入已有实现提交号。本轮只更新路线图文档，无需升级 Agent 版本。

### 2026-11-15 P0-03 本地实现与验收

- 会话：`11-15 Codex`；验收模型 `GPT-6（Codex，自验）`。新增独立 `e2e` 作业，安装 Node 22、锁定依赖和 Chromium 后运行现有 Playwright；失败时上传 `outputs/playwright/report` 与 `outputs/playwright/test-results`，保留 7 天。
- `CI=true npm --prefix web run e2e` 退出码 0：33 项通过、11 项按桌面/移动项目的既有分工跳过，55.3 秒。未修改测试或页面交互。
- 本地短暂加入一个必定失败的浏览器用例，退出码 1，确认 HTML 报告和 PNG 截图均生成；探测源码已经删除，生成物在被忽略的输出目录。
- 通用验收：前端构建、Go vet、Go 全量测试、两个命令编译、前端类型检查、格式及差异检查通过；Vitest 最终完整复验 61 个文件、449 项通过。首次复验与 staticcheck 的首次编译重叠，`AppSession.test.tsx` 的过期会话用例有一次 WebSocket 清理时序断言失败；重任务结束后无源码改动的完整复验通过，未顺手修改既有测试或产品逻辑。
- 尚待远端：GitHub 作业及失败诊断制品实测。用户当前只授权本地自动提交，仓库规则要求推送另获明确授权，因此总表暂保留 `进行中`；代码先独立提交，拿到推送授权后用临时 `ci/**` 分支完成剩余验收并回填结果。

### 2026-11-15 P0-04 本地实现与验收

- 会话：`11-15 Codex`；验收 `GPT-6（Codex，自验）`。Go CI 复用 `make fmt-check`，固定运行 staticcheck 2024.1.1；本地新增 `make staticcheck` 并接入 `make verify`，README 同步命令。检查安排在前端构建之后，保持干净检出的嵌入文件要求。
- 未配置 staticcheck 的完整基线共 193 条：ST1005 179、ST1013 7、U1000 4、SA4006 2、S1038 1。按本步骤允许的渐进方式，`staticcheck.conf` 先启用 `SA*` 安全检查，暂排除有两条既存告警的 SA4006，不夹带旧代码清理。两条 SA4006 位于 `internal/store/codex_snapshots_test.go` 的 `older` 和 `internal/store/session_collaboration.go` 的 `version` 赋值；保留待后续清理。
- 评估两个 TypeScript 未使用项开关：共 49 条诊断，43 条在 `App.tsx`，其余在 `ServersPage.test.tsx`、`WorkspacePreviewPolling.test.tsx`、`ServerCredentialDialog.tsx`、`CodexPage.tsx`、`SettingsPage.tsx`；按计划留到 P3-07 后处理，当前不开启，也不引入 ESLint/Prettier。
- `make staticcheck` 退出 0；完整 `make verify` 退出 0，包含 npm ci、前端构建、61 文件/449 项 Vitest、类型检查、staticcheck、vet、Go 全量测试和两个命令编译。
- 本地反向探测：临时未格式化 Go 文件使 `make fmt-check` 与 `make verify` 均退出 2，后者在 npm ci 前停止。临时独立模块沿用本仓库配置，常量非法正则使 staticcheck 报 SA1000 并失败。探测文件全部清理。
- 尚待远端：临时分支上的格式失败 CI 验收；总表保留 `进行中`，本地代码独立提交，待推送授权后补齐 GitHub 证据。

### 2026-11-15 P0-05

- 会话与验收：`11-15 Codex`；`GPT-6（Codex，自验）`。新增 `scripts/check-agent-version.sh`，用 `go list -deps` 动态发现本仓库 Agent 依赖包，仅检查非测试 Go 文件；包含共享协议、入口、删除和未忽略的新文件。拒绝未递增及降低的版本，依赖发现失败也失败。只改 HTTP、测试或文档不触发。
- CI 的 Go 作业完整检出历史，以 PR 目标提交或 push 的 before 为基准，当前检出为 HEAD；新分支的全零 before 使用空树。脚本 `BASE HEAD` 模式检查提交范围；本地 `make version-check` 从上游共同祖先（无上游时父提交）检查待推送及工作区改动。已接入 `make verify`，执行顺序在前端构建之后。
- 明确豁免为单个干净提交中的 `[skip-agent-version]: 原因`；只允许版本不变，不接受降低版本，不隐藏额外工作区改动或多提交范围。不得用这个标记跳过需要更新 Agent 的行为改动。
- 新增 `scripts/test-check-agent-version.sh`，在隔离临时 Git 仓库验证 20 种情况，包含要求的 HTTP-only 通过、Agent 无版本失败、Agent 升版本通过，以及协议、入口、未跟踪文件（含空格路径）、删除、测试/文档、忽略文件、错误依赖、无效基准、首次分支、提交与工作区版本、豁免边界。全部通过，临时仓库全部清理。
- 完整 `make verify` 退出 0：npm ci、前端构建、护栏及用例、61 个文件/449 项前端测试、类型检查、staticcheck、vet、Go 全量测试及编译通过。最后收紧“豁免不能降低版本”后重新运行全部 20 个脚本用例通过。`bash -n`、actionlint 1.7.3（不调用可选 shellcheck）、`git diff --check` 通过。
- 改动为脚本/CI/开发入口，不在 Agent 的 Go 依赖包中，无需追加版本号。已按用户授权独立提交；未推送。


### 2026-11-15 P0-08 本地实现与验收

- 会话与验收：`11-15 Codex`；`GPT-6（Codex，自验）`，分支 `main`。新增独立 Playwright 配置与 `e2e:real` 命令。global setup 编译真实控制面和原有独立假引擎，动态监听回环端口，从 JSON 日志取得地址后传给 worker；teardown 发 SIGTERM、等待成功退出并删除临时目录，超时会失败并杀掉进程组。正常与反向验证后均未留下测试运行目录。
- 运行时使用白名单环境、临时 HOME / XDG / CLI 配置、数据库、仓库、克隆与 Agent 状态目录，Docker 路径及前置服务 socket 指到测试目录。SQLite WAL 沿用 P0-07 对已登记取消问题的隔离方式。没有继承真实 WIO 配置或凭据，没有 API 路由拦截或直接写库准备业务状态。
- 一条桌面冒烟通过浏览器完成首次设置、密码登录、内置控制机在线、Codex / Claude / Gemini 创建会话、发送消息并在 assistant 消息内看到各自回复。三种引擎均覆盖，没有引擎缺口；它只验证 Wio 链路，未验证付费模型或真实 CLI 的兼容性。
- CI 的 E2E 作业获取 `web-dist` 并安装 Go，先运行既有模拟 API 用例，再运行真实链路。两组报告及结果目录独立，失败制品增加真实组的报告、截图/trace/video 与控制面日志。既有命令显式排除 real 目录，README 和 E2E 文档同步入口与要求。
- 首次成功运行：浏览器用例 4.5 秒，Playwright 总计 7.9 秒。最终在重新构建前端后复验：用例 5.4 秒，Playwright 总计 8.9 秒，命令墙钟 10.01 秒。原 `CI=1 npm --prefix web run e2e` 退出 0，33 通过、11 个既有桌面/移动分工跳过，墙钟 63.22 秒。
- 反向验证在隔离源码副本临时将 `Gateway.publish` 改为空操作，抑制事件保存与发布。真实浏览器在 Codex assistant 回复断言失败（退出 1，27.50 秒），生成 PNG 截图、HTML 报告、trace 和视频；整个副本随后删除，产品源码未改动。
- 通用验收复用 P1-04 最终工作区的一轮检查：前端构建、Go vet、Go 全量、PostgreSQL、前端 61 文件 / 449 项、类型检查和两个命令构建全部通过。Vitest 首轮遇到既存 `AppSession` 清理时序失败，未改源码的定向和完整复验通过，已登记总览。另通过 staticcheck、格式检查、Agent 版本护栏及全部 20 个护栏用例、actionlint、Node 脚本语法、本地文档链接与新文件空白检查。
- 尚待远端：真实 GitHub E2E 作业通过、失败诊断制品验证，以及 P0 阶段要求的连续三次真实推送结果。用户尚未授权推送，未触发任何远端运行，P0-03 / P0-04 / P0-08 保持 `进行中`，实现分别提交。没有未提交的演练或反向探测源码；等待临时 `ci/m1-*` 分支推送授权后补齐验收。没有发布、部署或访问生产数据库。


### 2026-11-15 M1 远端验收启动（P0-03 / P0-04 / P0-08）

用户答复上一轮临时 CI 分支推送授权询问“继续”，因此在 `main` 的本地实现基础上推送临时 `ci/m1-*` 分支。先以独立分支分别注入未格式化 Go 文件与浏览器失败用例，确认格式步骤失败及报告/截图制品可下载；然后用最终代码完成三次连续的正向真实推送，与本地 `make verify` 比较。故意失败的探测提交不进入 `main`，远端临时分支验收后删除。最终记录需填写运行号、提交号、各作业结论和制品检查结果；生产阶段验收继续保留未验证状态。


### 2026-11-15 P0-04 远端验收完成

会话与验收：`11-15 Codex`；`GPT-6（Codex，自验）`。临时分支 `ci/m1-format-probe-20261115` 的提交 `36b9153cb2f18a46322655aa222c509afada7ea9` 只额外加入一个未格式化测试文件。GitHub [运行 43650011041](https://github.com/Eversleeping/My-Wio/actions/runs/43650011041) 的 Go 作业精确在 `Check Go formatting` 失败，日志列出 `internal/protocol/m1_ci_format_probe_test.go`，其后的 staticcheck、版本护栏、vet、测试和构建全部跳过；Web、PostgreSQL、Browser E2E 均通过。

作为正向证据，浏览器失败探测分支的 [运行 43650012476](https://github.com/Eversleeping/My-Wio/actions/runs/43650012476) 中，Go 作业完整通过，包括格式、staticcheck、版本护栏、vet、全量测试与构建。该运行的失败仅来自故意注入的浏览器用例。当前本地 `make verify` 再次完整通过，123.34 秒。P0-04 验收项已全部满足，总表标记完成；探测提交未进入 `main`，临时分支将在本轮验收结束时统一删除。


### 2026-11-15 P0-03 远端验收完成

会话与验收：`11-15 Codex`；`GPT-6（Codex，自验）`。GitHub [正向运行 43650059756](https://github.com/Eversleeping/My-Wio/actions/runs/43650059756) 的 Web、Go、PostgreSQL、Browser E2E 四个作业全部通过。另在格式探测 [运行 43650011041](https://github.com/Eversleeping/My-Wio/actions/runs/43650011041) 中确认 Browser E2E 单独通过：模拟 API 组 33 通过 / 11 既有跳过，48.6 秒，真实链路组通过，10.9 秒。

浏览器反向分支 `ci/m1-browser-probe-20261115`、提交 `f7d84374c13c3a7942540395655696d9cec6a854` 的 [运行 43650012476](https://github.com/Eversleeping/My-Wio/actions/runs/43650012476) 仅在 `Test browser interactions` 失败：注入的用例在桌面和移动端都按预期失败，既有用例仍为 33 通过 / 11 跳过，真实组因前一步失败而跳过。`Upload failed browser diagnostics` 成功；实际下载制品 `playwright-diagnostics`（ID `14135394057`，ZIP 为 7,299,900 字节），校验包含主 HTML 报告、trace viewer、6 个 trace 归档，以及 PNG 和视频。包内有 10 个 PNG 条目（含报告引用副本），读取 PNG 签名和 IHDR，样本分辨率 1440×900，确认截图有效。

P0-03 的正向作业及失败制品验收均满足，总表标记完成。本轮本地 `make verify` 也全部通过；探测提交只存在于临时分支，不进入 `main`。临时分支在三次连续正向运行完成后统一删除。
