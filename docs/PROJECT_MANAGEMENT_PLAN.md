# 项目与 Git 工作区管理实施计划

更新时间：2026-07-20

## 执行规则

- 每一类功能必须包含实现、自动化测试、人工或浏览器验收记录。
- 每一类功能独立提交，提交前更新本计划中的状态和提交号。
- 所有服务器文件和 Git 写操作通过 Agent 的结构化命令执行，不提供任意 Shell 接口。
- 创建、移动和物理删除只能作用于 Agent 上报的受管项目根目录。
- 删除项目默认不删除远程仓库；远程仓库删除不在本期范围内。
- 同一工作区的写操作必须串行，离线服务器不得接收新的写操作。

## 进度表

| 编号 | 功能类别 | 主要交付内容 | 验收门槛 | 状态 | 提交 |
| --- | --- | --- | --- | --- | --- |
| 00 | 计划与基线 | 本计划、范围和验收规则；确认现有测试基线 | `go test ./...`、`npm --prefix web run typecheck` | 已完成 | `chore: 建立项目管理实施计划` |
| 01 | 基础模型与 Git 执行层 | 项目/工作区生命周期字段、结构化操作结果、资源操作关联、Agent Git 仓库安全命令 | 新旧数据库迁移测试；真实临时 Git 仓库测试；全量 Go 测试 | 已完成 | `feat: 建立项目 Git 管理基础` |
| 02 | 空白项目创建 | 指定服务器和受管路径创建目录、`git init`、初始分支、可选 README 初始提交 | 成功/冲突/越界/离线/重试测试；页面创建验收 | 已完成 | `feat: 支持空白项目创建` |
| 03 | 远程仓库关联与创建 | 关联已有空远程；Gitee/GitHub/GitLab 创建空远程；Vault 凭据和失败恢复 | 提供商 mock 测试；凭据脱敏；部分失败可重试 | 已完成 | `feat: 支持远程仓库关联与创建` |
| 04 | 项目管理 | 项目详情、重命名、描述、默认分支、置顶、隐藏、恢复、状态与操作历史 | API/store/UI 测试；桌面和移动端验收 | 已完成 | `feat: 完善项目详情与管理` |
| 05 | Git 只读信息 | 状态、上游、ahead/behind、分支、remote、提交列表和提交详情 | unborn/detached/dirty/worktree 场景测试；分页验收 | 已完成 | `feat: 完善工作区 Git 管理` |
| 06 | Git 写操作 | 创建/重命名/删除/切换分支，fetch、pull、push、设置 upstream | 脏目录保护、分支占用、非 fast-forward、离线和并发测试 | 已完成 | `feat: 完善工作区 Git 管理` |
| 07 | 工作区生命周期 | 显示名称、刷新、路径移动、跨服务器复制、移除记录、受管目录物理删除 | 路径和符号链接防护；依赖预检；失败恢复测试 | 未开始 | - |
| 08 | 项目删除 | 删除影响预检、仅删除元数据、删除受管文件、关联任务和部署处理 | 活跃任务/部署/脏目录阻止；审计记录；端到端验收 | 未开始 | - |
| 09 | 前端整合与最终验收 | 项目页拆分、响应式交互、中英文文案、Vitest/RTL/Playwright 核心流程 | `make test`、前端 build、Playwright 桌面/移动端、完成审计 | 未开始 | - |

## 目标数据模型

### projects

- `description`
- `default_branch`
- `status`: `provisioning | ready | partial | failed | archived`
- `provision_error`
- `archived_at`

### workspaces

- `display_name`
- `management_mode`: `managed | observed`
- `status`: `provisioning | ready | moving | deleting | failed`
- `last_git_refresh_at`
- `git_error`

### project_remotes

- 项目、remote 名称、fetch/push URL、网页地址、提供商、凭据配置、是否主 remote。
- 首批提供商为 `gitee`、`github`、`gitlab`；其他服务只支持关联现有 URL。

### agent_operations

- 增加 `project_id`、`workspace_id` 和结构化 `result_data`。
- 写操作通过资源关联检查实现工作区级串行化。

## API 目标

- `POST /api/projects`：统一处理 `blank | clone | discover`。
- `GET/PATCH /api/projects/{projectID}`：项目详情和修改。
- `GET /api/projects/{projectID}/operations`：项目操作历史。
- `POST /api/projects/{projectID}/deletion-plan` 和 `DELETE /api/projects/{projectID}`。
- `GET /api/workspaces/{workspaceID}/git` 和 `POST .../refresh`。
- `GET/POST/PATCH/DELETE /api/workspaces/{workspaceID}/git/branches`。
- `POST /api/workspaces/{workspaceID}/git/checkout`。
- `GET /api/workspaces/{workspaceID}/git/commits` 和提交详情接口。
- `GET/POST/PATCH/DELETE /api/workspaces/{workspaceID}/git/remotes`。
- `POST /api/workspaces/{workspaceID}/git/fetch|pull|push`。
- `POST /api/workspaces/{workspaceID}/move`、复制和删除预检接口。

## 最终完成标准

只有在进度表所有类别均为“已完成”，对应提交存在，并且以下证据全部通过后才视为完成：

1. SQLite 与 PostgreSQL 迁移和存储测试通过。
2. Agent 在 Linux 路径规则下完成真实 Git 仓库集成测试。
3. 所有 HTTP API 的正常、冲突、离线、越界、幂等和失败恢复路径均有测试。
4. 前端 typecheck、生产构建和组件测试通过。
5. Playwright 在桌面与移动视口完成创建项目、查看 Git 信息、分支操作、移动和删除预检流程。
6. `make test` 和最终全量回归通过，工作树无遗漏生成文件。
