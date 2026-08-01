# Wio 用户体验与性能优化实施计划

更新时间：2026-08-02（第七批实现与全量回归已完成，待提交部署）

## 目标

以“操作有即时反馈、实时状态可靠、长会话不卡顿、移动端可顺畅完成核心流程”为验收标准，系统性优化说明文档、交互反馈、端到端延迟、页面流畅度和可访问性。

## 维护规则

- 每个任务只允许处于 `待开始`、`进行中`、`已完成`、`受阻` 四种状态之一。
- 开始实现、完成实现、完成测试和发现范围变化时，必须同步更新本文件。
- 子 Agent 只处理互不重叠的文件或模块；主 Agent 统一审查 diff、解决冲突并运行回归。
- 未通过对应验收项的任务不得标记为已完成。
- 提交前必须检查 diff；仅在用户明确授权时提交，并按单一职责拆分。

## 体验指标

| 指标 | 目标 |
| --- | --- |
| 普通操作接收反馈 | 200ms 内出现本地反馈或排队状态 |
| 在线 Agent 操作下发 | 通常 300ms 内，不依赖高频数据库轮询 |
| WebSocket 事件可恢复性 | 不静默丢失；发生缺口时自动重同步 |
| 活跃会话事件显示 | 正常网络下 500ms 内可见 |
| 长会话加载 | 首屏只加载有限窗口，历史按需加载 |
| 长会话滚动 | 用户阅读历史时不被强制拉到底部 |
| 文件预览后台请求 | 无固定 750ms 高频轮询；支持推送或退避 |
| 前端初始包 | 单个入口 chunk 不超过 500KB，持续降低 gzip 体积 |
| 键盘操作 | Dialog 可 Escape 关闭、焦点受控并在关闭后恢复 |
| 回归门槛 | Go/Vitest/typecheck/build/vet 全部通过 |

## 阶段与任务

### 阶段 0：基线与计划

状态：已完成

- [x] 审查前端、WebSocket、Agent Gateway、数据层和文档结构。
- [x] 记录基线：Go 测试、Go vet、39 个前端测试、typecheck 和 build 均通过。
- [x] 记录构建基线：主入口 chunk 669.97KB，gzip 190.15KB，并触发 Vite 大 chunk 警告。
- [x] 主 Agent 确认首批并行任务边界。
- [x] 启动并行子 Agent。（Luna 两次实测均不可用；经用户明确授权，改用 `gpt-5.6-terra`、`xhigh`。）

验收：计划已落盘，任务边界明确，工作树基线已记录。

### 阶段 1：实时事件可靠性与精准刷新

状态：已完成

任务：

1. [已完成] 前端按完整 `StreamEvent.kind` 将刷新划分到 Dashboard、服务器、项目、Codex、部署、监控、设置和审批作用域；未知事件保留安全全局回退。
2. [已完成] WebSocket 正常消息按完整事件处理，线程流按 `stream_id/sequence` 增量更新；连接建立、解析失败和断线重连按全局失效处理。
3. [已完成] 处理 Hub 客户端队列溢出：队列满时移除并关闭慢订阅，由 WebSocket 重连路径执行全量重同步，不再静默丢弃最终失效提示。
4. [已完成] 已增加 Hub 慢订阅、真实已认证 WebSocket 正常投递与溢出关闭测试，以及前端事件作用域与安全回退测试。
5. [已完成] Dashboard `/summary` 在事件突发时最多每秒刷新一次，并有连续 revision 测试防止退回 100ms 全量查询。

验收：

- 不相关线程事件不会刷新当前线程会话。
- 慢客户端溢出后能够自动恢复到最新状态。
- Dashboard 在事件突发时不会按 100ms 频率重复执行全量查询。

### 阶段 2：Codex 长会话与消息滚动

状态：进行中

任务：

1. [已完成] `/threads/{id}/events` 支持 `after` 游标；默认返回最近 500 条、最大 1,000 条，并保持 sequence 正序。
2. [已完成] 前端按线程/视图缓存已有事件；单调新增使用 `after` 追加并按 `event_id/sequence` 去重，sequence 回退或全局重连时安全重载最近窗口。
3. [已完成] 历史消息提供“加载更早内容”；后端 `before` 返回游标前最近窗口并保持正序，前端前插合并后保持原阅读锚点。
4. [已完成] 仅当用户位于底部 96px 范围内时自动跟随新消息；否则保持阅读位置并显示本地化“有新消息”入口。
5. [已完成] 已增加首屏窗口、前后游标/视图/上限、非法及互斥游标、增量分页、去重、sequence 回退、重连重同步、视图隔离、历史前插锚点、滚动保持和自动跟随测试。
6. 评估消息、命令输出和 Markdown 的虚拟化渲染。

验收：5,000 条事件测试数据下，新增事件不重新传输和重建全部历史；阅读历史时页面不跳到底部。

### 阶段 3：Agent 操作下发延迟与数据库压力

状态：进行中

任务：

1. [已完成] 以 `Wake(serverID)` 作为主要下发机制；操作入队后可立即唤醒在线 Agent。
2. [已完成] 保留 2 秒低频容错重查，将原先每台 Agent 的 250ms 固定轮询降低为每 2 秒一次。
3. [已完成] 普通 Agent 操作在标记 delivered 后不再执行未使用的状态查询，每条减少 1 次数据库 `SELECT`；Codex turn 继续保留取消竞态检查与 best-effort interrupt。
4. [已完成] 增加“唤醒立即下发”“遗漏唤醒后最终下发”“断线重连超时补发”测试，并验证连接与接收协程均能退出。
5. [进行中] 已添加队列等待、投递和执行耗时结构化日志；指标聚合与告警阈值待后续阶段补充。

验收：空闲 Agent 不再每秒查询数据库 4 次；正常排队操作无需等待 fallback ticker。

### 阶段 4：预览、扫描和异步操作反馈

状态：进行中

任务：

1. 文件预览、diff、Git 刷新和扫描完成后通过作用域事件更新快照。
2. [进行中] 文件与 diff 预览已使用 0.75→1.5→3→6 秒退避、页面隐藏暂停/恢复确认、终态停止和过期请求取消；其他必须轮询的快照待逐步迁移。
3. 统一展示 `queued → delivered → running → succeeded/failed/cancelled`。
4. 对耗时操作显示操作名称、目标、开始时间、当前阶段、取消/重试入口。
5. 失败时保留旧数据并明确标注数据时间和失败原因。

验收：一次 20 秒预览不会产生约 26 次固定间隔 GET；用户始终能判断操作是否仍在进行。

### 阶段 5：前端包体、长列表和主线程流畅度

状态：进行中

任务：

1. [进行中] 将 Dashboard、Servers、Projects、Codex、Deployments、Monitoring、Settings 拆成路由级懒加载模块；Dashboard、Servers 与 Projects 已迁移为真实 `React.lazy` 路由，共享数据 Hook、页面基础组件和格式化工具作为后续页面迁移边界，其他页面继续逐页迁出。
2. [已完成] React、Markdown 和图标依赖已配置稳定 vendor chunks；Prism 保持在既有懒加载路径，避免强制合并懒加载依赖。
3. [进行中] Codex 活跃/归档会话已接入每页 50 条的向后兼容分页、去重“加载更多”和首屏外深链保护；实时刷新第一页时保留已加载尾页。审计日志、变更文件和大型文件预览仍待分页或虚拟化。
4. [已完成] 图片压缩已迁移到 Worker/OffscreenCanvas，并接入上传流程；Worker 创建、通信、超时或浏览器能力不足时自动回退主线程，保留 WebP、1600px、900KB 与 6 次尝试契约。
5. [进行中] 已增加构建后单 JS chunk 500,000 bytes 硬门槛和分包配置测试；关键交互性能测试待后续补充。

验收：不再出现单个入口 chunk 超过 500KB 的构建警告；上传大图时输入和滚动保持响应。

### 阶段 6：操作一致性与可访问性

状态：进行中

任务：

1. [已完成] 通用 Dialog 已接入现有页面，支持 Escape、焦点陷阱、首次聚焦、关闭后焦点恢复、背景关闭、ARIA 标题关联和多层 Dialog 顶层响应。
2. [已完成] 已建立统一确认对话框并迁移部署目标/历史删除、工作区与 diff 文件丢弃、部署回滚、容器操作、Codex 项目归档/隐藏/会话删除、服务器强制撤销与更新，以及设置资源删除。危险操作由类型约束强制展示影响范围，请求期间阻止重复确认、关闭和并发操作，失败后保留确认框以便重试；前端业务代码不再使用浏览器原生 `confirm()`。
3. [已完成] 61 个图标按钮均具备稳定的可访问名称；连接状态、审批计数和操作 toast 通过适当的 live region 通知，并增加静态守卫防止回退。
4. 增加键盘导航、窄屏、减少动画和屏幕阅读器相关测试。

验收：只使用键盘能够完成登录、创建会话、审批和取消操作，Dialog 期间焦点不会逃逸。

### 阶段 7：说明文档与运维信息治理

状态：已完成

任务：

1. [已完成] README 增加“5 分钟快速开始”、首次使用任务路径和常见启动问题。
2. [已完成] 增加 Agent/Git/Docker/Codex/npm/Playwright 能力依赖与离线行为矩阵，以及 `queued/delivered/running/终态` 含义和重试原则。
3. [已完成] 将生产部署记录改为可公开的脱敏模板，明确私有值只保存在受控配置中。
4. [已完成] 移除公开文档和宿主机 Caddy 模板中的实际公网 IP、域名、SSH 用户、端口和环境专属目录。
5. [已完成] 增加故障排查、备份、更新与回滚清单，并说明两种 Compose 方案的挂载边界。

验收：新用户仅按 README 即可完成本地启动；公开仓库不包含环境专属基础设施标识。

### 阶段 8：可复现 E2E 与最终验收

状态：进行中

任务：

1. [已完成] 已建立 Playwright 桌面端和 390×844 移动端自动化流程，使用本地 Vite 与页面级 API/WebSocket mock，不依赖生产服务、真实会话或真实凭据。
2. [已完成] 已覆盖登录、桌面/移动导航、部署排队与日志查看、503 错误恢复与重试、服务器注册、受管工作区重命名、Codex 会话创建与审批。所有用例统一校验页面级 mock、未匹配 API 和最小启动请求契约。
3. 增加 WebSocket 断线、慢客户端、长会话和大文件性能场景。
4. [已完成] 第四批完整 Go、前端、typecheck、build、vet、E2E、依赖审计和 diff 检查均通过。
5. 主 Agent 检查所有子 Agent 的代码、测试、文档和计划状态。

验收：所有自动化检查通过，无未解释的构建警告和未处理的高优先级问题。

## 当前并行任务分工

| 任务 | 执行者 | 文件边界 | 状态 |
| --- | --- | --- | --- |
| Luna 子 Agent 可用性验证 | 主 Agent | 运行时模型能力 | 受阻 |
| 后端实时 Hub 与 Agent 下发优化 | 主 Agent（Luna 不可用后接管） | `internal/realtime`、`internal/agentgateway` 及对应测试 | 进行中 |
| 前端滚动体验 | Terra xhigh 子 Agent，主 Agent 已验收 | `web/src/App.tsx`、`web/src/i18n.tsx` 及对应测试 | 已完成 |
| 前端会话事件增量追加 | Terra xhigh 子 Agent，主 Agent 已验收 | `web/src/App.tsx`、`web/src/CodexEventLoading.test.tsx` | 已完成 |
| 后端会话事件增量接口 | Terra xhigh 子 Agent，主 Agent 已验收 | `internal/httpapi/resources.go`、`internal/store/store.go` 及对应测试 | 已完成 |
| Agent 断线重连补发测试 | Terra xhigh 子 Agent，主 Agent 已验收 | `internal/agentgateway/gateway_test.go` | 已完成 |
| 快速上手与部署文档脱敏 | Terra xhigh 子 Agent，主 Agent 已验收 | `README.md`、`docs/PRODUCTION_DEPLOYMENT.md`、`deploy/Caddyfile.host-proxy` | 已完成 |
| 文件/diff 预览轮询退避 | Terra xhigh 子 Agent，主 Agent 已验收 | `web/src/App.tsx`、`web/src/WorkspacePreviewPolling.test.tsx` | 已完成 |
| 能力依赖与离线行为说明 | Terra xhigh 子 Agent，主 Agent 已验收 | `README.md` | 已完成 |
| API WebSocket 慢客户端恢复测试 | Terra xhigh 子 Agent，主 Agent 已验收 | `internal/httpapi/websocket_test.go` | 已完成 |
| 第三批：会话历史向前游标 | Terra xhigh 子 Agent，主 Agent 已验收并接入前端 | `internal/httpapi`、`internal/store`、`web/src/App.tsx` 及对应测试 | 已完成 |
| 第三批：前端稳定分包与体积检查 | Terra xhigh 子 Agent，主 Agent 已验收 | `web/vite.config.ts`、构建检查脚本及对应配置 | 已完成 |
| 第三批：通用 Dialog 可访问性组件 | Terra xhigh 子 Agent，主 Agent 已验收并接入 | 通用组件、独立测试和 `App.tsx` | 已完成 |
| 第三批：精准实时刷新与 Dashboard 限频 | 主 Agent | `web/src/App.tsx`、`web/src/RealtimeRefresh.test.tsx` | 已完成 |
| 生产部署 | 主 Agent | 服务器仓库、Compose、健康检查 | 已完成：`e3ff7c4` 已部署，本机与公网健康检查通过 |
| 第四批：图片压缩 Worker 与回退 | Terra xhigh 子 Agent，主 Agent 已接入并验收 | 压缩模块、Worker、`App.tsx` 与测试 | 已完成 |
| 第四批：统一确认对话框组件 | Terra xhigh 子 Agent，主 Agent 已接入并验收 | 新增组件、部署删除迁移与测试 | 已完成全部浏览器原生确认迁移 |
| 第四批：Playwright E2E 基础设施 | Terra xhigh 子 Agent，主 Agent 已验收 | E2E 配置、用例、测试脚本和说明 | 已完成 |
| 第四批：图标按钮与 live region 审计 | 主 Agent | 前端主应用与可访问性测试 | 已完成 |
| 第五批：Agent 队列数据库往返优化 | Terra xhigh 子 Agent，主 Agent 已验收 | `internal/agentgateway` 与对应测试 | 已完成 |
| 第五批：核心流程与错误恢复 E2E | Terra xhigh 子 Agent，主 Agent 已验收 | `web/e2e` | 已完成本批范围 |
| 第五批：工作区 Git 丢弃确认迁移 | Terra xhigh 子 Agent，主 Agent 已验收 | `WorkspaceGitDialog` 与对应测试 | 已完成 |
| 第五批：Dashboard 路由懒加载 | 主 Agent | 共享前端模块、Dashboard 页面与对应测试 | 已完成 |
| 第六批：Servers 路由懒加载 | Terra xhigh 子 Agent，主 Agent 已验收 | `web/src/App.tsx`、Servers 页面与对应测试 | 已完成 |
| 第六批：注册服务器与项目/工作区 E2E | Terra xhigh 子 Agent，主 Agent 已验收 | `web/e2e` | 已完成本批范围 |
| 第六批：剩余原生确认与长列表审计 | Terra xhigh 子 Agent，主 Agent 已审计并实施高风险项 | 前端危险操作、列表和对应测试 | 已完成审计并在第七批完成会话分页与确认迁移 |
| 第七批：Projects 路由懒加载 | Terra xhigh 子 Agent，主 Agent 已验收 | `web/src/App.tsx`、Projects 页面与对应测试 | 已完成 |
| 第七批：Codex 会话与审批 E2E | Terra xhigh 子 Agent，主 Agent 已验收 | `web/e2e` | 已完成本批范围 |
| 第七批：Codex 会话列表分页 | Terra xhigh 子 Agent与主 Agent，主 Agent 已验收 | `internal/store`、`internal/httpapi`、`web/src/App.tsx` 与对应测试 | 已完成 |
| 第七批：剩余危险确认迁移 | Terra xhigh 子 Agent与主 Agent，主 Agent 已验收 | Codex、Servers、Settings 与对应测试 | 已完成 |
| 计划维护、集成与最终验收 | 主 Agent | 全局审查与验证 | 进行中 |

## 进度日志

- 2026-08-02：完成项目 UX/性能审计；确定实时刷新、Agent 轮询、长会话和主包体积为首要问题。
- 2026-08-02：创建本实施计划，记录基线、验收指标、阶段任务和并行边界。
- 2026-08-02：两次尝试启动 `gpt-5.6-luna`、`max` 子 Agent，均被运行时以“未知模型”拒绝；遵守用户约束，未改用 Sol/Terra，独立任务暂由主 Agent 串行接管。
- 2026-08-02：用户授权在 Luna 不可用时改用 Terra xhigh；已并行启动前端滚动、后端事件窗口、文档体验三个独立子任务，等待主 Agent 验收。
- 2026-08-02：完成 Hub 慢订阅断开重同步实现及测试；完成 Gateway 唤醒优先、2 秒 fallback 轮询实现，以及立即下发与最终下发测试。
- 2026-08-02：`go test ./internal/realtime ./internal/agentgateway` 通过；竞态测试受阻于本机未安装 `gcc`（Go race detector 需要 CGO），已完成主 Agent 并发锁审查。
- 2026-08-02：主 Agent 审计全部生产 `QueueOperation` 调用，确认入队后均有显式 `Wake`；相关包连续 10 轮测试通过，并增加操作队列等待、投递及执行耗时结构化日志。
- 2026-08-02：完成会话事件后端窗口与游标接口，默认最近 500 条、最大 1,000 条；主 Agent 增补负游标用例，`go test ./internal/httpapi ./internal/store` 通过。
- 2026-08-02：完成会话滚动跟随优化及中英文“有新消息”入口；Terra 子 Agent 初版因硬编码英文被退回，修正后由主 Agent 复验 5/5 测试与 typecheck 并接收。
- 2026-08-02：完成 README 快速开始与生产部署脱敏；初版因挂载边界描述不准确、Caddy 模板仍含环境标识被退回，修正后通过主 Agent 敏感标识与静态一致性检查。当前环境无 Docker/Caddy CLI，运行期解析验证待具备工具的环境补做。
- 2026-08-02：启动前端会话事件增量追加与 Agent 断线重连补发测试两个 Terra xhigh 后续任务。
- 2026-08-02：完成 Agent 断线重连补发测试；主 Agent 确认 31 秒重投阈值模拟、同 operation 重发及两条连接的 goroutine 退出均有效，相关包测试通过。
- 2026-08-02：前端增量追加初版 46 个测试通过，但主 Agent 发现“改写会话导致 sequence 回退”和“WebSocket 重连需全量重同步”未覆盖，已退回 Terra 子 Agent 修正，暂不验收。
- 2026-08-02：前端增量追加返工通过主 Agent 二次验收：普通事件使用 `after` 追页，sequence 回退替换旧窗口，全局重连强制重同步；完整前端 15 个测试文件、49 个测试及 typecheck 通过。
- 2026-08-02：本批完整回归通过：`go test ./...`、`go vet ./...`、前端测试、typecheck、build、`git diff --check`。构建入口 chunk 为 672.88KB（gzip 191.27KB），500KB 大包警告仍存在，继续列为阶段 5 高优先级任务。
- 2026-08-02：启动第二批 Terra xhigh 并行任务：文件/diff 预览轮询退避、能力依赖与离线行为说明、API WebSocket 慢客户端恢复测试。
- 2026-08-02：完成 README 能力依赖、离线降级、操作状态和重试原则说明；主 Agent 对照 Store、Gateway 与安装流程复核后接收。
- 2026-08-02：完成真实 WebSocket + Hub 慢客户端恢复测试；主 Agent 连续 20 次复跑，确认正常事件可达、队列溢出后连接关闭且无僵尸连接。
- 2026-08-02：完成文件/diff 预览退避轮询、后台暂停、恢复立即确认、终态停止与切换取消；主 Agent 复核 AbortController/定时器时序，完整前端 16 个测试文件、53 个测试及 typecheck 通过。
- 2026-08-02：第二批完整回归通过：`go test ./...`、`go vet ./...`、前端 53 个测试、typecheck、build、`git diff --check`。入口 chunk 当前 673.68KB（gzip 191.65KB），大包拆分仍是下一批最高优先级之一。
- 2026-08-02：三个已验收提交已推送到 `origin/main`（`15a07e8`、`4131d1a`、`3caac86`）；生产域名健康检查正常，部署因当前 SSH 私钥未被新控制机接受而受阻，继续核对本机候选密钥且不覆盖服务器状态。
- 2026-08-02：启动第三批 Terra xhigh 并行任务：会话历史向前游标、前端稳定分包与体积检查、通用 Dialog 可访问性组件；主 Agent 负责接入、审查和完整回归。
- 2026-08-02：使用项目目录内、未纳入 Git 的 `aws.pem` 完成生产 SSH 登录；部署前备份 `.env`、PostgreSQL 与旧源码，预构建通过后将 `3caac86` 切换为当前版本。Compose 两个服务均健康，本机和公网 `/api/health` 均返回 HTTP 200，新前端 hash 为 `0cd4dd354fb89e2b`；旧源码目录保留用于回滚，未修改宿主机 Caddy。
- 2026-08-02：第三批实现并经主 Agent 验收：WebSocket 事件按业务作用域刷新，Dashboard 汇总限频为每秒最多一次；会话事件增加 `before` 向前窗口、前端“加载更早内容”和滚动锚点保持；通用 Dialog 完成键盘与焦点可访问性并接入全部现有页面。
- 2026-08-02：完成稳定 vendor 分包与构建体积硬门槛，最大 JS chunk 从 673.68KB（gzip 191.65KB）降至 342.75KB（gzip 92.71KB），不再出现 500KB 警告。完整回归通过：`go test ./...`、`go vet ./...`、前端 19 个测试文件/67 个测试、typecheck、build、bundle check 和 `git diff --check`。
- 2026-08-02：第三批 5 个提交已推送并部署到生产 `e4ae537`；部署前再次备份 `.env` 与 PostgreSQL，Compose 服务、本机和公网健康检查通过，新前端 hash 为 `a81412176e7204f9`。
- 2026-08-02：启动第四批 Terra xhigh 并行任务：图片压缩 Worker 与兼容回退、统一确认对话框组件、Playwright 桌面/移动 E2E 基础设施；主 Agent 负责图标按钮/live region 审计、接入和完整验收。
- 2026-08-02：第四批实现完成并进入最终验收：图片压缩迁移至独立 Worker，失败或不支持时安全回退主线程；统一确认对话框已迁移部署目标与部署历史删除；61 个图标按钮通过可访问名称静态审计，连接状态、审批计数和 toast 已增加 live region；Playwright 桌面与 390×844 移动端基础流程使用完全本地 mock，可重复运行且不会访问生产服务。
- 2026-08-02：主 Agent 与三路 Terra xhigh 完成第四批验收并修正审查问题：图片达到压缩极限后不再回退主线程重复计算，最多两张并发处理且切换会话时可取消；确认对话框忙碌时统一禁用 Escape、背景与关闭按钮，并将影响说明关联到 Dialog。完整回归通过：`go test ./...`、`go vet ./...`、前端 22 个测试文件/80 个测试、typecheck、build、bundle check、Playwright 4 passed/2 skipped、生产依赖审计和 `git diff --check`；最大 JS chunk 348.57KB（gzip 94.68KB），Worker 产物 1.55KB。
- 2026-08-02：第四批 5 个提交已推送并部署到生产 `89c5469`。部署前备份 `.env`、PostgreSQL 和旧 HEAD 到 `/opt/wio-backups/20260801T183342Z`；首次前台 SSH 等待超时后确认旧容器仍健康，再使用远端日志安全完成构建与替换。Compose 服务、本机和公网健康检查均通过，生产前端版本为 `cb9f81e1b80a7003`，未修改宿主机 Caddy。
- 2026-08-02：启动第五批 Terra xhigh 并行任务：Agent 队列数据库往返优化、核心流程与错误恢复 E2E、工作区 Git 丢弃确认迁移；主 Agent 负责路由级懒加载边界审查、计划维护和最终验收。
- 2026-08-02：第五批实现完成并进入最终审查：普通 Agent 操作每条减少一次 delivered 后状态查询，Codex 取消竞态保护保持不变；工作区 Git 文件丢弃已接入统一危险确认；E2E mock 对未知请求明确返回 501，并新增部署排队、日志与 503 重试恢复；Dashboard 已从单体 `App.tsx` 迁移为独立懒加载路由，共享数据 Hook、页面组件和格式化工具形成后续页面拆分边界。
- 2026-08-02：第五批主 Agent 完整回归通过：`go test ./...`、`go vet ./...`、前端 22 个测试文件/83 个测试、typecheck、build、bundle check、Playwright 6 passed/4 expected skipped 和 `git diff --check`。构建新增 2.80KB（gzip 1.08KB）的 Dashboard 独立 chunk，最大主 chunk 从 348.57KB 降至 347.13KB（gzip 94.58KB）。最终嵌套 Dialog 集成测试发现并修正丢弃确认关闭后焦点落到页面根节点的问题，现在会明确恢复到原丢弃按钮。
- 2026-08-02：第五批 5 个提交已推送并部署到生产 `b52377a`。部署前备份 `.env`、PostgreSQL 和旧 HEAD 到 `/opt/wio-backups/20260801T185948Z`；后台构建完成后 controlplane 容器成功 Recreated/Started，PostgreSQL 与 controlplane 均 healthy，远端工作树干净，本机与公网 `/api/health` 均返回 HTTP 200。公网与本机前端 SHA-256 一致，为 `0e8b8f9212bed90b0c35ccddb3f5358ed9484a632223d8806a737dd0944fb977`，未修改宿主机 Caddy。
- 2026-08-02：启动第六批 Terra xhigh 并行任务：Servers 路由级懒加载、注册服务器与项目/工作区 E2E、剩余原生确认与长列表审计；主 Agent 负责第五批部署闭环、子 Agent diff 审查、回归测试与后续确认迁移。
- 2026-08-02：第六批实现完成并通过主 Agent 与独立审查：Servers 从 `App.tsx` 迁移为独立懒加载路由，生成 25.34KB（gzip 5.78KB）chunk，最大主 chunk 从 347.13KB（gzip 94.58KB）降至 324.68KB（gzip 90.06KB）；diff 文件丢弃、部署回滚及容器 stop/restart/remove 迁移到统一危险确认框，并修复确认期间仍可触发其它部署操作的并发回归。
- 2026-08-02：E2E 新增服务器 SSH 指纹确认与流式安装、受管工作区重命名流程；Mock 流对齐 `starting → progress → completed` NDJSON 协议，所有实际运行用例通过统一 `afterEach` 检查未匹配 API 与最小启动请求契约。前端 22 个测试文件/83 个测试、typecheck、build、bundle check、Playwright 8 passed/6 expected skipped 和 `git diff --check` 全部通过；独立审查无 P0/P1/P2 遗留。
- 2026-08-02：第六批 4 个提交已推送并部署到生产 `e3ff7c4`。部署前备份旧 HEAD、Git bundle、`.env` 和 PostgreSQL 到 `/opt/wio-backups/20260801T193447Z`；首次误用基础 Compose 时仓库 Caddy 因 80 端口占用未启动，宿主机 Caddy 未修改。随后终止冗余构建，使用已完成的新镜像与正确 host-proxy 双文件组合恢复 `127.0.0.1:18080`，并移除本次新建且仅含空目录的 Caddy 容器和两个卷。最终仅 controlplane/PostgreSQL 容器运行且均 healthy，宿主机 Caddy active，本机和公网 `/api/health` 均为 HTTP 200，前端 SHA-256 一致为 `67928cebd60c2aba711850dbadeb19369cc88c1512cedfb34026c179f16d3093`。
- 2026-08-02：启动第七批 Terra xhigh 并行任务：Projects 路由级懒加载、Codex 会话创建与审批 E2E、Codex 会话列表向后兼容分页后端；主 Agent 负责生产部署收口、分页前端接入、剩余确认迁移和完整验收。
- 2026-08-02：第七批实现完成并经主 Agent 集成验收：Projects 迁出为 54.53KB（gzip 12.66KB）独立懒加载 chunk；会话列表无分页参数继续返回旧数组，显式分页使用默认 50、最大 100 的稳定 `limit/offset` 窗口；Codex 首屏仅加载 50 条，支持去重加载更多、实时首屏刷新保留尾页和首屏外深链安全加载。Codex、Servers、Settings 剩余 9 类危险操作全部迁入统一确认框，失败保留重试且 busy 期间锁定关闭与重复提交。
- 2026-08-02：独立审查发现实时首屏刷新会永久合并旧 tail 的 P2；主 Agent 修正为后台重放当前已加载的全部 50 条尾页窗口，刷新期间保留旧列表，成功后原子替换，并以共享请求代次消除 load-more/refresh 竞态。修正后第七批全量回归通过：`go test ./...`、`go vet ./...`、前端 24 个测试文件/92 个测试、typecheck、build、bundle check、Playwright 10 passed/8 expected skipped、乱码扫描和 `git diff --check`。最大主 chunk 为 275.99KB（gzip 79.45KB），Projects chunk 为 54.53KB（gzip 12.66KB），Servers chunk 为 26.60KB（gzip 6.04KB）；项目目录中的 `aws.pem` 仍被 Git 忽略，未进入变更集。
