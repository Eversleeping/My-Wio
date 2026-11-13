# P6 统一会话入口

返回[路线图总览](README.md)。状态以总览的“进度总表”为准，本文件只写步骤细节和执行记录。

本阶段落实总览的决策 D9：会话入口从“按引擎分页面”改为“按项目和工作区组织”，引擎是创建会话时的一个选择。与 P0 至 P5 不同，这个阶段会改变用户看到的界面，所以每一步都写明了用户能看到什么变化。

## 现状

- **三个并列的导航入口。** `web/src/App.tsx` 的导航里 `codex`、`gemini`、`claude` 是三个独立的视图，各自对应 `pages/CodexPage.tsx`、`pages/GeminiPage.tsx`、`pages/ClaudePage.tsx`。同一个工作区的会话要到三个页面分别查看。
- **三个侧栏其实已经按工作区分组。** 分组函数有三份：`App.tsx` 的 `groupThreadsByWorkspace`、`GeminiPage.tsx` 里的内联分组、`ClaudePage.tsx` 的 `groupClaudeThreads`。分组标题的拼法相同（项目名加工作区名、分支或目录名），都只显示有会话的工作区，都把置顶的项目排在前面。
- **置顶有两层。** 项目的置顶和隐藏存在 `projects`、`project_server_preferences`，三种引擎共用。会话的置顶各存各的：`codex_threads.pinned_at`、`gemini_threads.pinned_at`、`claude_thread_management.pinned_at`。
- **会话列表是三个请求。** `/threads`、`/gemini/threads?archived=`、`/claude/threads?archived=`。设置页为了统计已经同时请求这三个。
- **新建会话有三个对话框**，字段不同：Codex 只选工作区；Gemini 选工作区、模型、审批模式、思考程度；Claude 选工作区和模型等。三个侧栏的工作区右键菜单里都有“在此新建会话”。
- **协作已经跨引擎。** `components/CollaborationTaskDialog.tsx` 发起协作任务时可以选择目标引擎、模型和目录方式（共享目录或独立 worktree），接口是 `/sessions/{engine}/{id}/collaboration`。
- **定时任务按引擎分开。** 入口在 `CodexPage.tsx`、`pages/claudeManagement.tsx`、`pages/geminiManagement.tsx`，另有设置页里的汇总。
- **链接形式。** 地址栏是 `/?view=<engine>&thread=<id>`。控制面在协作结果里生成并持久化这种链接（`store/session_collaboration.go`、`store/session_collaboration_actions.go`）。
- **按引擎命名的本地偏好。** 侧栏高度有三个键：`wio_codex_sidebar_height`、`wio_gemini_sidebar_height`、`wio_claude_sidebar_height`。
- **首页的会话数只算 Codex。** `httpapi/resources.go` 的 `summary` 里 `threads` 是 `SELECT COUNT(*) FROM codex_threads`，前端文案键是 `dashboard.codexSessions`。

## 目标

1. 一个“会话”页面列出所有工作区和其中三种引擎的会话，不需要在三个页面之间切换。
2. 新建会话时先选工作区，再选引擎；单独任务和协作任务都可以自由选择引擎。
3. 引擎独有的能力和设置如实保留，不为了统一而隐藏或模拟。
4. 已有的链接、通知和协作结果里的地址继续可用。

不在本阶段范围内：

- 在会话之上引入“任务”对象（总览的待决策事项 D10）。
- 在同一个会话里中途切换引擎。三种 CLI 的原生历史互不兼容，跨引擎接力通过协作功能完成。
- 改动协作、定时任务、审批的后端行为。

## 设计

以下方案的六个待确认事项已由用户在 2026-11-13 确认。P6-01 的差异盘点如果发现与它冲突的事实，先更新本节并告知用户。

### 页面

2026-11-13 根据 P6-01 代码盘点修正：移动端现状是“会话 / 文件 / 对话”三态，不是“列表 / 对话”两态；统一页面继续保留三态。

```text
导航：总览 / 服务器 / 项目 / 会话 / 部署 / 运维 / 监控 / 设置

会话页
  侧栏
    搜索框、引擎筛选（全部 / Codex / Gemini / Claude）、归档切换、新建
    工作区分组（置顶的项目在前）
      会话条目：引擎标记、标题、状态、更新时间；置顶的在前，其余按更新时间混排
  主区
    P2-06 的会话外壳，按所选会话的引擎加载对应的引擎模块
```

- 侧栏的数据来自 P2-05 的 `GET /api/sessions`（省略 `engine` 参数的混合列表）和现有的 `/workspaces`。
- 引擎标记用图标加文字，不只靠颜色区分。
- 引擎筛选是本地偏好，不写进地址栏。
- 搜索在服务端分页前执行，匹配组名、路径、标题、服务器名、Wio 会话 ID 和原生会话 ID。
- 混合列表按会话级键（项目置顶、会话置顶、更新时间、引擎、ID）游标分页，同时返回分页前的完整工作区摘要；前端按项目置顶时间、项目名和规范工作区标题排组。

### 新建会话

一个对话框，三段：

1. 工作区。只列出 `status=ready` 的工作区；从某个工作区的右键菜单进入时已经选好。
2. 引擎。默认是这个工作区上一次使用的引擎。目标服务器上不可用的引擎显示为禁用并写明原因（CLI 未安装、Agent 版本不够、没有凭据），原因来自 P2-02 的能力解析，不在前端另写判断。
3. 引擎自己的字段（模型、模式、思考程度等），由引擎模块提供，内容与现在各自的对话框一致。

### 地址

- 新的规范形式是 `/?view=sessions&engine=<engine>&thread=<id>`。
- 旧形式 `/?view=<engine>&thread=<id>` 由前端永久识别并转换，不设下线时间。
- 控制面生成链接的代码不改，继续输出旧形式。原因：这些链接已经持久化在协作结果里；控制面回滚后提供的旧前端只认识旧形式；旧形式在新前端里永远可用，改它没有收益。

### 引擎独有的内容

| 内容 | 处理 |
| --- | --- |
| Codex 的审批、目标、状态快照、子 Agent 活动、MCP | 留在 Codex 引擎模块里，只在 Codex 会话中出现 |
| Gemini 和 Claude 的技能、会话内提问、计划模式 | 留在各自的引擎模块里 |
| CLI 版本和凭据管理 | 仍在“服务器”和“设置”里，会话页只在引擎不可用时给出跳转 |
| 定时任务 | P6-06 之前保持按引擎的入口；之后在工作区菜单里提供跨引擎的列表 |

### 已确认事项

用户在 2026-11-13 确认全部按下表执行。之后要改其中任何一项，先与用户确认并在这里注明日期。

| 编号 | 事项 | 结论 |
| --- | --- | --- |
| Q1 | 入口放在哪里 | 独立的“会话”导航项，取代三个引擎导航项（用户 2026-11-13 已确认要合并成一个页面） |
| Q2 | 侧栏是否显示还没有会话的工作区 | 只显示有会话的工作区，与现在一致；提供“显示全部工作区”的开关。内置控制机的扫描目录较宽，全部显示可能很长 |
| Q3 | 同一工作区里三种引擎的会话怎么排 | 混排，按更新时间，条目上带引擎标记；用引擎筛选收窄 |
| Q4 | 归档视图 | 一个归档视图，包含三种引擎 |
| Q5 | 首页是否改成“待处理事项”视图（等待回答的提问、待审批、失败的轮次） | 本阶段只把会话数改成三种引擎合计；待处理事项视图另行决定 |
| Q6 | 导航里是否保留按引擎直达的快捷方式 | 不保留，用侧栏的引擎筛选代替 |
| Q7 | 工作区组顺序 | 项目置顶时间倒序；未置顶组按项目名、规范工作区标题升序。顺序基于分页前的完整分组摘要，不只重排当前页 |
| Q8 | 搜索字段和完整性 | 在 P2-05 服务端分页前搜索组名、路径、标题、服务器名、Wio 会话 ID 和原生会话 ID |
| Q9 | 非 ready 工作区是否出现在新建对话框 | 不显示，与当前 Gemini、Claude 一致 |
| Q10 | 归档视图允许哪些管理操作 | 保留项目置顶、重命名、隐藏，以及会话重命名、恢复、删除；删除继续二次确认 |
| Q11 | 混合列表分页方式 | 按会话级键（项目置顶标志与时间、会话置顶标志与时间、`updated_at`、引擎、ID）分页，响应为 `{items,next_cursor,groups}`；`groups` 是分页前的完整工作区摘要 |

## 步骤

### P6-01 交互方案确认与差异盘点

不依赖其他步骤，可以随时开始。它的结论会反过来影响 P2-05 和 P2-06 的实现，越早做越好。

**做法**

1. 盘点三个页面的侧栏和新建流程，写成本文件末尾的“入口差异清单”：分组和排序规则、搜索范围、置顶和归档的操作入口、工作区和会话的右键菜单项、空状态、移动端的列表与对话切换、新建对话框的字段和默认值、按引擎命名的本地偏好键。
2. 每处差异标注“有意”或“偶然”。偶然的差异给出合并后采用哪一种的建议。
3. “已确认事项”已有结论，不需要再问。盘点中发现表里没有覆盖的取舍时，列出选项和建议，与用户确认后补进表里。
4. 对照盘点结果检查“设计”一节。如果需要 P2-05 或 P2-06 做出调整（例如混合列表需要额外的筛选参数），同步更新 P2 的阶段文件。

**验收**

- “入口差异清单”完整，每处偶然的差异都有合并后采用哪一种的结论。
- 这一步不改任何产品代码。

### P6-02 统一会话页面（与旧入口并存）

**用户看到的变化**：导航里多出一个“会话”入口，三个引擎入口仍然保留。

**做法**

1. 新增 `sessions` 视图和页面，侧栏按“设计”实现：三种引擎的会话按工作区合并分组，带引擎标记、引擎筛选、搜索和归档切换。分组、排序、置顶和分页规则按 Q7、Q8、Q11 只实现一份；工作区是否有会话不能只根据当前分页窗口判断。详情请求拿到但列表窗口中没有的选中会话要并入对应工作区组。归档视图按 Q10 保留项目置顶、重命名、隐藏和会话重命名、恢复、删除。
2. 主区复用 P2-06 的会话外壳，按所选会话的引擎加载引擎模块。引擎模块继续按引擎懒加载，避免把三种引擎的代码打进同一个块。
3. 地址解析支持新形式；旧形式在这一步仍然打开旧页面，不做转换。
4. 实时刷新：页面订阅 `codex`、`gemini`、`claude` 三个范围的并集，用一个公共 revision 刷新列表；不保留 Gemini、Claude 各自的 5 秒轮询。P3-09 尚未完成时最多保留一个页面级、仅页面可见时运行的兜底轮询。实时事件类型和范围的划分不变。
5. 侧栏高度改用一个新的偏好键，首次读取时沿用旧的三个键里已有的值。
6. 移动端沿用现有的“会话 / 文件 / 对话”三态切换。
7. 这一步的新建按钮先沿用各引擎现有的对话框：点击后先选引擎，再打开对应的对话框。统一的对话框在 P6-03 做。

**验收**

- 新页面的单元测试覆盖：跨引擎分组和排序、引擎筛选、搜索、置顶、归档切换、隐藏的项目不出现、分页窗口外的工作区不被误判为空、深链会话不在当前页时仍并入侧栏。
- Playwright 桌面和移动端各增加一条用例：在同一个工作区下看到三种引擎的会话并依次打开。
- 三个旧入口的现有测试不需要修改就能通过。
- 构建通过 `check:bundle`；把入口块和会话页面块的体积记到“执行记录”。

**风险与回滚**

新页面是附加的，删掉导航项和视图即可回滚。主要风险是会话多时混合列表的加载时间：Codex 的列表有自己的分页，合并后以 P2-05 统一的分页规则为准，在“执行记录”里记下一个会话较多的库上的实际加载时间。

### P6-03 新建会话时选择引擎

**用户看到的变化**：新建会话变成一个对话框，先选工作区，再选引擎。

**做法**

1. 按“设计”实现统一的新建对话框。引擎自己的字段由引擎模块提供，字段、默认值和校验与现在各自的对话框一致。
2. 引擎是否可用及原因取自能力解析。不可用时给出到“服务器”或“设置”相应位置的跳转。
3. 记住每个工作区上一次使用的引擎，作为下次的默认值。这是本地偏好，不入库。
4. 工作区右键菜单的“在此新建会话”打开同一个对话框并预选工作区。
5. 核对协作任务对话框（`CollaborationTaskDialog`）：它已经支持选择目标引擎，确认从统一页面里的三种引擎会话都能打开它，并且目标引擎的可用性判断与新建对话框一致。有出入时登记到总览的“发现的问题”。

**验收**

- 测试覆盖：三种引擎各自创建成功并跳转到新会话；不可用的引擎显示原因且不能提交；默认引擎取自上次的选择。
- 如果 P0-08 已完成，在真实链路的冒烟用例里用新对话框创建三种引擎的会话。
- 三个旧页面里的新建入口不受影响。

### P6-04 导航切换与旧链接兼容

前置除了 P6-03，还需要用户在实际使用过新页面之后确认可以切换。

**用户看到的变化**：导航里的 Codex、Gemini、Claude 三项消失，只剩“会话”。

**做法**

1. 从导航里移除三个引擎入口。`view=codex`、`view=gemini`、`view=claude` 的地址由前端转换成新形式，带 `thread` 时打开对应会话，不带时打开会话页并预选该引擎的筛选。
2. 控制面生成链接的代码不改，原因见“设计”。
3. 删除三个旧页面里已经没有调用方的侧栏、分组函数和新建对话框，以及它们的测试；对应的覆盖点应当已经在 P6-02、P6-03 的测试里。删除前逐项核对，把对照表写进“执行记录”。
4. 更新 Playwright 用例里按引擎视图打开页面的地址；保留至少一条用旧形式地址打开会话的用例。
5. 清理不再使用的文案键和三个旧的侧栏高度偏好键的读取。
6. 更新 `README.md` 的功能说明、`docs/USER_GUIDE.md` 以及 `docs/CLAUDE.md`、`docs/GEMINI.md` 里提到页面入口的段落。

**验收**

- 协作结果、通知里已有的旧形式链接能打开对应会话。用一份包含历史协作记录的数据验证，或者在测试里构造。
- 控制面回滚到上一个版本后，旧前端仍然正常工作（这一步没有删除任何后端路由）。
- 全部前端测试和 E2E 通过，构建通过 `check:bundle`。

**风险与回滚**

这一步只改前端，回滚这一个提交即可恢复三个入口。删除旧页面代码可以拆成第二个提交，在切换后观察一个版本再做。

### P6-05 项目页与首页衔接

**用户看到的变化**：从项目页可以直接进入某个工作区的会话；首页的会话数包含三种引擎。

**做法**

1. 项目页的工作区条目增加“查看会话”和“新建会话”入口，跳转到会话页并定位到该工作区。
2. 首页的会话数改为三种引擎合计。接口里 `counts.threads` 这个字段名不变，只改统计范围；文案键 `dashboard.codexSessions` 换成不带引擎名的键。
3. 首页不改成“待处理事项”视图（Q5 的结论）。以后要做时另立步骤。

**验收**

- 项目页和首页的测试更新并通过。
- `summary` 接口的测试断言新的统计范围。

### P6-06 定时任务的跨引擎入口

前置是 P2-07（定时任务合表）。在三张表上做跨引擎的列表，等于再写一份按引擎分支的代码。

**用户看到的变化**：在工作区菜单里看到这个工作区的全部定时任务，新建时选择引擎。

**做法**

1. 会话页的工作区菜单增加“定时任务”，列出该工作区三种引擎的定时任务，支持新建、编辑、启停、删除。新建时选择引擎，引擎自己的字段由引擎模块提供。
2. 现有的按引擎管理入口里的定时任务部分改为跳转到这里，设置页的汇总保留。
3. 调度器和定时任务的后端行为不变。

**验收**

- 测试覆盖三种引擎的定时任务在同一个列表里的增删改和启停。
- 调度器测试不需要修改就能通过。

## 阶段完成标准

- 进度总表里 P6 的步骤全部 `已完成` 或 `已取消`。
- 用户确认：日常使用不再需要按引擎切换页面。
- 前端非测试代码里，按引擎名分支的地方只剩引擎模块的注册处和引擎模块内部。把搜索结果贴到“执行记录”。

## 入口差异清单

盘点基线为当前 HEAD（路线图基线 `0247c1c`）。以下“差异”共 28 处，其中“偶然”23 处、“有意”5 处；“一致”项不计入差异数。判断“偶然”的依据是同一产品概念由三份平行实现产生了不同表现，且代码和已确认事项中没有引擎能力方面的理由；“有意”的依据是 CLI 能力、持久化字段或原生会话标识确实不同。

### 分组和排序规则

| 项目 | Codex | Gemini | Claude | 性质 | 合并后采用 | 出处 |
| --- | --- | --- | --- | --- | --- | --- |
| 一致：分组范围 | 先滤掉隐藏项目，只为有会话的工作区建组 | 同左 | 同左 | 一致 | 直接合成一份；“显示全部工作区”开关另行把 `/workspaces` 中的空工作区补进来 | `web/src/pages/CodexPage.tsx:129-134` `CodexPage`；`web/src/pages/GeminiPage.tsx:68-79` `GeminiPage.groups`；`web/src/pages/ClaudePage.tsx:340-352` `groupClaudeThreads` |
| D01 分组标题的兜底 | `项目名 · display_name/branch/目录名`；都取不到时还会退到目录名或工作区 ID | 主规则相同；取不到 identity 时只显示项目名 | 同 Gemini | 偶然；三份同类分组函数的兜底不同 | 采用 Codex：始终给工作区一个可区分的 identity，最后退到工作区 ID | `web/src/App.tsx:710-726` `groupThreadsByWorkspace`；`web/src/pages/GeminiPage.tsx:73-78` `GeminiPage.groups`；`web/src/pages/ClaudePage.tsx:347-351` `groupClaudeThreads` |
| D02 工作区组顺序 | 项目置顶时间倒序；其后按项目名、组标题升序，但只对已加载会话分组 | 项目置顶时间倒序；其余保持接口首次出现顺序，实际受组内最靠前会话影响 | 同 Gemini；接口本身还未按项目置顶排序 | 偶然；没有引擎能力理由，Q3 只决定组内会话混排，未决定组顺序 | 按 Q7：组顺序基于分页前的完整分组摘要；项目置顶时间倒序，未置顶组按项目名、规范工作区标题升序 | `web/src/App.tsx:710-726` `groupThreadsByWorkspace`；`web/src/pages/GeminiPage.tsx:80-85` `GeminiPage.groups`；`web/src/pages/ClaudePage.tsx:353-358` `groupClaudeThreads` |
| 一致：组内会话顺序 | 置顶时间倒序，再按 `updated_at` 倒序 | 接口已按项目置顶、会话置顶、更新时间排序，页面保持该顺序 | 接口按会话置顶、更新时间排序；页面稳定重排置顶项，效果相同 | 一致 | 跨引擎混排时采用：置顶会话先按 `pinned_at` 倒序，未置顶会话按 `updated_at` 倒序；最后加（引擎，ID）稳定破同值 | `web/src/App.tsx:724-726` `groupThreadsByWorkspace`；`internal/store/gemini.go:76-95` `GeminiThreads`；`internal/store/claude.go:59-75` `ClaudeThreads`；`web/src/pages/ClaudePage.tsx:126` `ClaudePage` |
| D27 选中或深链会话不在列表窗口 | 单独请求详情后，用 `mergeThreads` 并入当前活动/归档列表，再参与分组 | 单独请求只供主区 `active` 使用，不进入 `groups` | 同 Gemini | 偶然；同一深链行为由三份页面实现不同 | 采用 Codex：详情成功后按（引擎，ID）去重并入正确归档视图和工作区组，但不改分页游标；P2-06 公共侧栏负责 | `web/src/pages/CodexPage.tsx:117-134` `CodexPage` 与 `web/src/App.tsx:543-552` `mergeThreads`；`web/src/pages/GeminiPage.tsx:57-68` `GeminiPage`；`web/src/pages/ClaudePage.tsx:63-75` `ClaudePage` |

### 搜索范围

| 项目 | Codex | Gemini | Claude | 性质 | 合并后采用 | 出处 |
| --- | --- | --- | --- | --- | --- | --- |
| D03 是否有搜索入口 | 无 | 有 | 有 | 偶然；同一侧栏只有 Codex 未实现 | 统一页提供一个搜索框 | `web/src/pages/CodexPage.tsx:305-322` `CodexPage`；`web/src/pages/GeminiPage.tsx:125-140` `GeminiPage`；`web/src/pages/ClaudePage.tsx:108-131` `ClaudePage` |
| 一致：Gemini/Claude 的匹配字段 | — | 组名、路径；不命中组时匹配标题、服务器名、Wio ID、原生 `session_id`，忽略大小写 | 同 Gemini | 一致 | 作为统一搜索的现状基线 | `web/src/pages/GeminiPage.tsx:80-85` `GeminiPage.groups`；`web/src/pages/ClaudePage.tsx:353-358` `groupClaudeThreads` |
| D04 搜索完整性 | 无搜索；列表可逐页加载全部 | 只搜索接口截断后的最多 200 条 | 同 Gemini | 偶然；客户端搜索叠加不同列表上限导致结果不完整 | 按 Q8：P2-05 在服务端分页前搜索组名、路径、标题、服务器名、Wio 会话 ID 和原生会话 ID | `web/src/App.tsx:554-618` `useThreadList`；`web/src/pages/GeminiPage.tsx:57,68-86` `GeminiPage`；`web/src/pages/ClaudePage.tsx:63,97` `ClaudePage`；`internal/store/gemini.go:82` `GeminiThreads`；`internal/store/claude.go:65` `ClaudeThreads` |

### 置顶和归档的操作入口

| 项目 | Codex | Gemini | Claude | 性质 | 合并后采用 | 出处 |
| --- | --- | --- | --- | --- | --- | --- |
| 一致：页头入口 | 都有“恢复隐藏项目”、活动/归档切换；活动视图显示新建按钮，归档视图隐藏新建按钮 | 同左 | 同左 | 一致 | 直接合并；一个归档视图由 `archived` 筛选三种引擎 | `web/src/pages/CodexPage.tsx:307` `CodexPage`；`web/src/pages/GeminiPage.tsx:127-130` `GeminiPage`；`web/src/pages/ClaudePage.tsx:110-117` `ClaudePage` |
| 一致：活动视图的置顶和归档 | 工作区菜单有项目置顶、归档项目会话；会话菜单有会话置顶、归档 | 同左 | 同左 | 一致 | 公共菜单实现一次；项目归档仍调用所选引擎的能力，P6-06 前不把三引擎批量归档混成一次操作 | `web/src/pages/CodexPage.tsx:264-280` `projectMenuActions/threadMenuActions`；`web/src/pages/geminiManagement.tsx:42-60` `projectActions/threadActions`；`web/src/pages/claudeManagement.tsx:42-60` 同名函数 |
| D05 归档视图的工作区菜单 | 仍可打开，保留项目置顶、项目重命名、隐藏项目 | 标题不包 `ContextMenu`，没有工作区菜单 | 同 Gemini | 偶然；归档视图条件分支不同 | 按 Q10：归档视图保留项目置顶、重命名、隐藏 | `web/src/pages/CodexPage.tsx:264-270,310` `projectMenuActions`；`web/src/pages/GeminiPage.tsx:133-135` `GeminiPage`；`web/src/pages/ClaudePage.tsx:121-126` `ClaudePage` |
| D06 归档会话重命名 | 归档菜单不含重命名 | 归档菜单仍含重命名 | 同 Gemini | 偶然；后端都能更新标题，没有引擎能力差异 | 按 Q10：归档会话仍可重命名 | `web/src/pages/CodexPage.tsx:272-285` `threadMenuActions`；`web/src/pages/GeminiPage.tsx:109-117` `threadMenuActions`；`web/src/pages/ClaudePage.tsx:99-105` `threadActions` |
| D07 归档会话删除 | 归档列表不显示行内删除按钮 | 活动和归档列表都显示；只按运行/恢复状态禁用 | 同 Gemini | 偶然；三组删除接口都存在 | 按 Q10：归档视图允许删除并保留二次确认 | `web/src/pages/CodexPage.tsx:319` `CodexPage`；`web/src/pages/GeminiPage.tsx:135-138` `GeminiPage`；`web/src/pages/ClaudePage.tsx:126-129` `ClaudePage` |
| D08 会话置顶标记位置 | 标题区域末尾单独显示图钉 | 图钉放在标题文字前 | 同 Gemini | 偶然；纯展示差异 | 统一为“引擎标记、图钉、标题”的固定顺序，不能只靠图钉颜色 | `web/src/pages/CodexPage.tsx:319` `CodexPage`；`web/src/pages/GeminiPage.tsx:135-137` `GeminiPage`；`web/src/pages/ClaudePage.tsx:126-128` `ClaudePage` |

### 工作区和会话的右键菜单项

| 项目 | Codex | Gemini | Claude | 性质 | 合并后采用 | 出处 |
| --- | --- | --- | --- | --- | --- | --- |
| 一致：活动工作区菜单项 | 在此新建、项目置顶/取消置顶、重命名项目、创建永久 worktree、归档项目会话、隐藏项目 | 同左 | 同左 | 一致 | 合成公共菜单；行为按当前工作区和所选引擎分发 | `web/src/pages/CodexPage.tsx:264-271` `projectMenuActions`；`web/src/pages/GeminiPage.tsx:133-135` `GeminiPage` 与 `web/src/pages/geminiManagement.tsx:42-48`；`web/src/pages/ClaudePage.tsx:121-125` 与 `web/src/pages/claudeManagement.tsx:42-48` |
| D09 “创建永久 worktree”的禁用条件 | 要求同项目、同服务器至少有候选工作区；未显式检查服务器在线 | 只检查当前线程的服务器在线 | 同 Gemini | 偶然；这是引擎无关的工作区操作，三份包装条件不同 | 采用两边条件的并集：有有效源工作区且服务器在线；能力判断由公共工作区模块给出 | `web/src/pages/CodexPage.tsx:232-268` `openWorktreeDialog/projectMenuActions`；`web/src/pages/geminiManagement.tsx:41-46`；`web/src/pages/claudeManagement.tsx:41-46` |
| D10 “在此新建会话”的行为 | 不开对话框，直接只用 `workspace_id` 创建 | 有同工作区历史模型时直接沿用并创建；否则开对话框 | 总是打开对话框并预选工作区 | 偶然；是三份入口写法不同，不是 CLI 限制 | 按已确认设计统一打开同一个对话框并预选工作区，不再静默沿用参数直接创建 | `web/src/pages/CodexPage.tsx:220-230,264-266` `createSessionInWorkspace`；`web/src/pages/GeminiPage.tsx:87-107,133-135` `openNewSession/createSessionInWorkspace`；`web/src/pages/ClaudePage.tsx:94-95,121-125` `newSession` |
| 一致：活动会话菜单公共项 | 置顶、重命名、定时任务、继续到新会话、继续到新 worktree、归档、复制工作目录、复制 Wio ID、复制原生 ID、复制链接、新窗口打开 | 同左 | 同左 | 一致 | 公共项合成一份；原生 ID 的标签和能力禁用留给引擎模块 | `web/src/pages/CodexPage.tsx:272-285` `threadMenuActions`；`web/src/pages/GeminiPage.tsx:109-117` 与 `web/src/pages/geminiManagement.tsx:49-61`；`web/src/pages/ClaudePage.tsx:99-105` 与 `web/src/pages/claudeManagement.tsx:49-61` |
| 一致：删除入口 | 三者都不是右键菜单项，而是行尾垃圾桶加确认框；活动/恢复中会禁用 | 同左 | 同左 | 一致（归档可见性差异见 D07） | 保留一次确认；统一后可放行尾或公共菜单，但三种引擎位置必须一致 | `web/src/pages/CodexPage.tsx:287-303,319` `deleteSession`；`web/src/pages/GeminiPage.tsx:118-122,137,155`；`web/src/pages/ClaudePage.tsx:89-92,128,152` |

### 空状态

| 项目 | Codex | Gemini | Claude | 性质 | 合并后采用 | 出处 |
| --- | --- | --- | --- | --- | --- | --- |
| D11 列表为空 | 活动显示“无会话”，归档显示专门的“无归档任务” | 活动和归档都用同一个“无会话” | 同 Gemini | 偶然；仅文案分支不同 | 采用 Codex：活动、归档、搜索无结果分别给出明确文案 | `web/src/pages/CodexPage.tsx:307` `CodexPage`；`web/src/pages/GeminiPage.tsx:131-140` `GeminiPage`；`web/src/pages/ClaudePage.tsx:118-131` `ClaudePage` |
| D12 主区没有选中会话 | 通用 Empty，提示选择工作区，没有新建按钮 | 品牌欢迎页、说明和新建按钮 | 同 Gemini | 偶然；同一入口的空壳不同 | 采用 Gemini/Claude 的可操作欢迎页，但文案改为不带引擎；工作区已定位时把它带入新建对话框 | `web/src/pages/CodexPage.tsx:323-327` `CodexPage`；`web/src/pages/GeminiPage.tsx:145-151` `GeminiPage`；`web/src/pages/ClaudePage.tsx:136-142` `ClaudePage` |

### 移动端的列表与对话切换

| 项目 | Codex | Gemini | Claude | 性质 | 合并后采用 | 出处 |
| --- | --- | --- | --- | --- | --- | --- |
| 一致：视图数 | “会话 / 文件 / 对话”三个页签 | 同左 | 同左 | 一致；与原 P6 设计写的“两态”冲突 | 保留三态；已在“设计”和 P6-02 第 6 条注明 2026-11-13 修正 | `web/src/pages/CodexPage.tsx:116,149-152,305-327` `CodexPage`；`web/src/pages/GeminiPage.tsx:55,63-67,125-151` `GeminiPage`；`web/src/pages/ClaudePage.tsx:57,69-75,108-142` `ClaudePage` |
| 一致：自动切换 | 选择会话或打开文件后切到对话；有深链会话时初始为对话 | 同左 | 同左 | 一致 | 公共外壳直接保留 | 同上一行 |

### 新建对话框的字段和默认值

| 项目 | Codex | Gemini | Claude | 性质 | 合并后采用 | 出处 |
| --- | --- | --- | --- | --- | --- | --- |
| D13 工作区选项范围 | 列出 `/workspaces` 返回的全部工作区 | 只列 `status === "ready"` | 同 Gemini | 偶然；同一创建前提由页面各自过滤 | 按 Q9：只显示 `status=ready` 的工作区 | `web/src/App.tsx:940-945` `CreateThread`；`web/src/pages/GeminiPage.tsx:187-201` `CreateGeminiSession`；`web/src/pages/ClaudePage.tsx:143-149` `ClaudePage` |
| D14 顶部“新建”的初始工作区 | 空 | 有当前会话时预选其工作区，否则空 | 同 Gemini | 偶然；入口默认策略不一 | 统一为：从工作区菜单进入时预选该工作区；从页头进入时有当前会话则预选当前工作区，否则空 | `web/src/App.tsx:940-945` `CreateThread`；`web/src/pages/GeminiPage.tsx:87-90,153` `openNewSession`；`web/src/pages/ClaudePage.tsx:94-95,115,142-145` `newSession` |
| D15 用户在已打开的对话框里改工作区 | 只有工作区字段 | 清空模型和思考程度，但保留原审批模式 | 重取服务器模型、清空 effort，但保留原权限模式 | 偶然；跨工作区残留模式来自局部 state 更新，并无产品说明 | 切换工作区时重新计算该工作区该引擎的全部默认值，不沿用另一工作区的模式；作为疑点登记 | `web/src/pages/GeminiPage.tsx:187-201` `CreateGeminiSession`；`web/src/pages/ClaudePage.tsx:94-95,143-146` `newSession/ClaudePage` |
| D16 字段 | 工作区 | 工作区、模型、思考程度、审批模式 | 工作区、模型、effort、权限模式 | 有意；CLI 能力不同 | 公共段只放工作区和引擎；其余由引擎模块提供，字段不做伪统一 | `web/src/App.tsx:940-945` `CreateThread`；`web/src/pages/GeminiPage.tsx:159-201` `ModelOptions/CreateGeminiSession`；`web/src/pages/ClaudePage.tsx:143-163` `ClaudePage/ClaudeOptions` |
| D17 引擎字段默认值 | 创建时无模型字段；会话内编辑器默认审批 `on-request`、模型/思考为空 | 优先当前会话，否则取列表中同工作区的首个会话，沿用其模型、思考、审批模式，`plan` 会降为 `default`；没有可沿用值时模型列表自动选服务器默认或首项，模式 `default` | 模型先取服务器配置或 `sonnet`，模型接口的 `default_model` 可再覆盖；effort、权限模式取列表中同工作区的首个会话或空/`default` | 有意；对应引擎持久化方式和能力不同 | 各引擎模块保留自身默认算法；公共对话框只负责在工作区变化时要求模块重新计算 | `web/src/codexComposerPreferences.ts:7-15`；`web/src/pages/GeminiPage.tsx:87-90,159-201` `openNewSession/ModelOptions/CreateGeminiSession`；`web/src/pages/ClaudePage.tsx:47-61,94-95,143-163` `ClaudePage/ClaudeOptions` |
| 一致：提交校验 | 工作区必填 | 工作区、模型必填 | 工作区、模型必填，模型列表加载时不提交 | 一致（字段差异由引擎决定） | 引擎模块返回自己的可提交状态和错误 | `web/src/App.tsx:940-945` `CreateThread`；`web/src/pages/GeminiPage.tsx:196-201` `CreateGeminiSession`；`web/src/pages/ClaudePage.tsx:77-82,148` `ClaudePage.create` |

### 按引擎命名的本地偏好键

| 项目 | Codex | Gemini | Claude | 性质 | 合并后采用 | 出处 |
| --- | --- | --- | --- | --- | --- | --- |
| D18 侧栏高度键 | `wio_codex_sidebar_height` | `wio_gemini_sidebar_height` | `wio_claude_sidebar_height` | 偶然；同一布局的复制实现 | P6-02 改为一个新键；首次读取按当前选中引擎的旧键优先，再从其余旧键取首个有效值，最后用 38；旧键直到 P6-04 才停止读取 | `web/src/components/SidebarHeightDivider.tsx:4-11`；`web/src/pages/CodexPage.tsx:92-95`；`web/src/pages/GeminiPage.tsx:34-41`；`web/src/pages/ClaudePage.tsx:39-42` |
| 一致：高度取值 | 三者都经 `readSidebarHeight`，默认 38，限制 20–80 | 同左 | 同左 | 一致 | 直接复用 | `web/src/components/SidebarHeightDivider.tsx:4-11` `readSidebarHeight` |
| D19 会话内编辑器偏好键 | 另有每个会话一个 `wio_codex_composer_preferences_v1:<threadID>`，保存审批模式、模型、思考程度 | 无按 Gemini 命名的对应键 | 无按 Claude 命名的对应键 | 有意；Codex 当前线程结构不持久化这些编辑器选择，Gemini/Claude 从线程字段初始化 | 留在 Codex 引擎模块，不与统一侧栏键一起删除或迁移 | `web/src/codexComposerPreferences.ts:1-55` `load/save/clearCodexComposerPreferences`；`web/src/App.tsx:960-971` `SessionView` |

### 三个列表接口

| 项目 | Codex | Gemini | Claude | 性质 | 合并后采用 | 出处 |
| --- | --- | --- | --- | --- | --- | --- |
| D20 `archived` 参数 | 缺省为活动；`true` 为归档；`all` 为全部；其他值返回 400 | 只有字符串恰为 `true` 才查归档，其他任意值都当活动 | 同 Gemini | 偶然；三份处理器语法不一 | 新 `/api/sessions` 严格接受 `false`/缺省与 `true`；旧 Codex 路由继续兼容 `all`，旧三路由逐字段兼容 | `internal/httpapi/resources.go:1657-1698` `threads`；`internal/store/resources.go:50-63` `ListThreads`；`internal/httpapi/gemini.go:25-32` `geminiThreads`；`internal/httpapi/claude.go:23-30` `claudeThreads` |
| D21 分页、上限和响应形状 | 不带分页参数时返回完整数组；带 `limit`/`offset` 时返回 `{items,has_more,next}`，默认 50、最大 100，前端有“加载更多” | 固定 `LIMIT 200`，返回数组，无游标和加载更多 | 同 Gemini | 偶然；历史实现能力不一，混合列表不能据此完整搜索或稳定翻页 | 按 Q11：新路由统一 `limit` 和不透明会话级游标，过滤、搜索在分页前执行，响应为 `{items,next_cursor,groups}`；旧路由保持原响应 | `internal/httpapi/resources.go:99-117,1657-1698` `threads/threadListPageResponse`；`internal/store/resources.go:66-92` `ListThreadsPage`；`web/src/App.tsx:554-618` `useThreadList`；`internal/store/gemini.go:76-95` `GeminiThreads`；`internal/store/claude.go:59-75` `ClaudeThreads` |
| D22 Store 排序 | 项目置顶、会话置顶、更新时间 | 项目置顶、会话置顶、更新时间 | 只按会话置顶、更新时间；页面在取完最多 200 条后才把置顶项目组移到前面 | 偶然；项目置顶是三引擎共享概念，Claude SQL 漏了一层 | 按 Q11：项目置顶在分页前生效；游标为（项目置顶标志与时间、会话置顶标志与时间、`updated_at`、引擎、ID），并返回分页前完整分组摘要 | `internal/store/resources.go:48-83` `ListThreads/ListThreadsPage`；`internal/store/gemini.go:56-82` `GeminiThreads`；`internal/store/claude.go:53-65` `ClaudeThreads`；`web/src/pages/ClaudePage.tsx:353-358` `groupClaudeThreads` |
| D23 相同排序值的破同值 | 只有分页查询最后追加 `t.id DESC`；不分页查询没有 | 没有 | 没有 | 偶然；复制实现和后加分页造成 | 新游标排序最后固定追加（引擎，ID），契约测试覆盖同时间戳不重不漏 | `internal/store/resources.go:62,83` `ListThreads/ListThreadsPage`；`internal/store/gemini.go:82` `GeminiThreads`；`internal/store/claude.go:65` `ClaudeThreads` |
| D24 `revoked_at` 防御条件 | Codex 查询没有 `s.revoked_at IS NULL` | Gemini 查询保留该条件 | Claude 查询保留该条件 | 偶然；仅是历史残留的防御性实现差异，没有用户可见差异 | 统一查询保留 `s.revoked_at IS NULL` 作为旧库防御，不需要产品取舍。正常撤销会物理删除服务器；服务器到工作区、工作区到三张线程表均为级联删除；启动还会清理旧库残留 | `internal/store/resources.go:48-63` `codexThreadSelect/ListThreads`；`internal/store/gemini.go:76-82` `GeminiThreads`；`internal/store/claude.go:59-65` `ClaudeThreads`；`internal/store/store.go:838-909` `RevokeServer/purgeRevokedServers/deleteServerRecords`；`internal/store/schema.sql:120-150,498-529` |
| 一致：公共返回字段 | ID、工作区/项目/服务器 ID，标题、状态、路径、项目名、服务器名，项目/会话置顶，项目隐藏，归档、创建和更新时间 | 同左 | 同左 | 一致 | 进入统一列表项；P2 的 `Session` 草图已补项目/服务器展示与项目偏好字段 | `internal/store/resources.go:22-39` `Thread`；`internal/store/gemini.go:16-54` `GeminiThread`；`internal/store/claude.go:14-51` `ClaudeThread` |
| D25 引擎能力和运行字段 | 列表没有 `server_status`、`operation_id` 或 `*_supported`；Codex 的模型选择在本地偏好和状态快照中 | 返回 `server_status`、`operation_id`、模型、审批模式、思考程度、恢复状态及多项能力布尔值 | 返回 `server_status`、`operation_id`、模型、权限模式、effort、恢复状态及多项能力布尔值 | 有意；CLI 能力和现有持久化不同 | 公共字段统一；能力进入 `capabilities`，引擎设置进入引擎模块；缺失值不得伪造 | 同上一行；能力赋值见 `internal/store/gemini.go:83-94` `GeminiThreads`、`internal/store/claude.go:66-73` `ClaudeThreads` |
| D26 原生会话 ID 字段 | `codex_thread_id` | `session_id` | `session_id` | 有意；三种 CLI 的线上字段名已被旧前端依赖 | 新路由统一成 `native_id` 并带 `engine`；旧响应继续保留原字段 | `internal/store/resources.go:22-39` `Thread`；`internal/store/gemini.go:37-54` `GeminiThread`；`internal/store/claude.go:24-51` `ClaudeThread` |

### 列表刷新机制

| 项目 | Codex | Gemini | Claude | 性质 | 合并后采用 | 出处 |
| --- | --- | --- | --- | --- | --- | --- |
| D28 列表刷新 | 实时 revision 触发 `useThreadList` 刷新首页和已加载尾页，无页面内定时器 | 实时 revision 之外，每 5 秒在页面可见时递增 `tick`，重取列表和详情；工作区只依赖 `realtime`，不随 `tick` 重取 | 实时 revision 之外，每 5 秒在页面可见时递增 `tick`，重取列表、详情和工作区；服务器只依赖 `realtime`，不随 `tick` 重取 | 偶然；同一列表的刷新策略分散在三份页面 | P6-02 订阅三种实时范围的并集并只维护一个公共 revision；不保留两个引擎各自的 5 秒轮询。P3-09 尚未完成时最多保留一个页面级、仅页面可见时运行的兜底轮询 | `web/src/pages/CodexPage.tsx:90-98` `CodexPage` 与 `web/src/App.tsx:554-618` `useThreadList`；`web/src/pages/GeminiPage.tsx:56-65` `GeminiPage`；`web/src/pages/ClaudePage.tsx:62-73` `ClaudePage` |

### 与设计冲突的事实

1. **移动端不是两态。** 三个现有页面都有“会话 / 文件 / 对话”三个页签，并在选择会话或文件后切到对话。原 P6-02 第 6 条写成“列表 / 对话”两态，与代码冲突。已在 2026-11-13 对“设计”和 P6-02 第 6 条作最小修正，统一入口继续保留三态。出处：`web/src/pages/CodexPage.tsx:116,149-152,305-327` `CodexPage`；`web/src/pages/GeminiPage.tsx:55,63-67,125-151` `GeminiPage`；`web/src/pages/ClaudePage.tsx:57,69-75,108-142` `ClaudePage`。

### P6-01 确认结果

用户在 2026-11-13 确认：T1 选 A、T2 选 B、T3 选 A、T4 选 A、T5 选 A。结论已写入“已确认事项”Q7 至 Q11，后续步骤直接按该表执行。

### 建议登记到总览“发现的问题”

1. Gemini 和 Claude 的新建对话框在用户切换工作区时分别保留原审批模式、权限模式，可能把另一工作区的高权限模式带过去；见 D15。
2. Claude 的 Store 先按会话置顶和更新时间取最多 200 条，前端之后才按项目置顶重排；超过 200 条时，置顶项目可能完全不在返回结果里；见 D22。
3. Gemini 和 Claude 列表固定截断 200 条且没有“加载更多”，搜索也只覆盖这 200 条；见 D04、D21。
4. `CollaborationTaskDialog` 始终列出三种目标引擎，阻塞条件只检查源会话协作支持、授权和目标权限映射；读取的协作状态也只返回源 Agent 是否支持协作，没有返回目标引擎的 CLI、凭据和版本可用性。它与 P6 新建对话框要求的“不可用引擎禁用并写明原因”不是同一套判断，P6-03 必须对齐。出处：`web/src/components/CollaborationTaskDialog.tsx:50-90,96-122` `CollaborationTaskDialog`；`internal/store/session_collaboration.go:546-591` `ReadCollaborationState`。

### 未核实

- 按本步骤要求未运行构建、测试或浏览器 E2E；移动端结论来自组件状态与渲染分支，未实机核对 CSS 断点下的视觉效果。
- 未用超过 200 条会话的真实数据库测量混合列表耗时，也未实测 T5 两种分页方案在大工作区下的交互表现。
- P2-02 的统一能力解析尚未实现，当前代码不能核实未来能否区分“CLI 未安装、Agent 版本不够、没有凭据”三类禁用原因。

## 执行记录

按时间顺序追加，每条包含日期、步骤编号、会话标识、做了什么、验收结果、遗留事项。

- 2026-11-13，P6-01，11-13 Codex：逐项盘点 Codex、Gemini、Claude 的侧栏、分组与排序、搜索、置顶和归档、工作区和会话菜单、空状态、移动端、新建流程、本地偏好、深链选中与刷新机制，以及三个列表处理器和 Store 查询；记录 28 处差异（23 处偶然、5 处有意），列出 5 项待用户确认取舍和 4 项建议登记问题。验收：清单覆盖 P6-01 指定维度，每处差异均有代码出处，偶然差异均给出合并建议；复核确认 `revoked_at` 条件仅用于旧库防御，不构成用户可见取舍；同步修正移动端三态设计，并更新 P2-05 的混合列表筛选、搜索、分组摘要与游标分页要求和 P2-06 的公共侧栏约束；仅修改路线图文档，按纯文档步骤未运行构建测试。遗留：T1–T5 由协调者转交用户，确认后补进“已确认事项”；在此之前 P6-01 保持 `进行中`。四项疑点由协调者登记到总览。
- 2026-11-13，P6-01，11-13 Codex：用户确认 T1=A、T2=B、T3=A、T4=A、T5=A；将结论补入“已确认事项”Q7 至 Q11，并按结论固化 P6-02 和 P2-05 的分组顺序、搜索字段、工作区范围、归档操作、会话级游标与分页前分组摘要契约。验收：P6-01 的取舍均已确认，清单、设计、后续步骤和 P2-05 契约已同步，验收项满足；按纯文档步骤未运行构建测试。遗留：无，由协调者更新总表并提交。
