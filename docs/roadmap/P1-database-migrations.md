# P1 数据库迁移版本化

返回[路线图总览](README.md)。状态以总览的“进度总表”为准，本文件只写步骤细节和执行记录。

## 现状

入口是 `internal/store/store.go` 的 `OpenWithOptions`，每次启动按固定顺序做这些事：

1. 执行整份 `internal/store/schema.sql`。全部语句都是 `CREATE TABLE IF NOT EXISTS` 或 `CREATE INDEX IF NOT EXISTS`。
2. 依次调用 21 个 `migrate*` 函数（`migrateUserAuthMode`、`migrateGeminiModelConfig`、`migrateWorkspaceMetadata` 等）。每个函数自己判断是否需要执行，并按 `driver == "pgx"` 分方言处理。
3. 中间夹着一次数据维护 `purgeRevokedServers`，以及一段只对 PostgreSQL 执行的 `ALTER TABLE metric_rollups`。

这种做法目前能工作，但有四个问题：

- **不知道库在哪个版本。** 没有版本表，只能靠每个函数的幂等判断。
- **顺序是隐式的。** 新迁移加在哪里、依赖哪个旧迁移，只体现在函数调用顺序上。
- **无法拒绝降级。** 旧版控制面可以连上被新版改过的库。表结构里已经出现过为了兼容旧控制面而拆表的做法（见 `schema.sql` 里 `claude_thread_options` 的注释），说明回滚控制面是真实场景。
- **破坏性迁移没有落脚点。** P2 要合并表，需要“只执行一次、可测试、失败可定位”的迁移。

已有的有利条件：`internal/store/store_test.go` 里已经有用旧结构建库再打开的迁移测试；`make test-postgres` 覆盖 PostgreSQL 的迁移与存储。

## 目标

1. 库里有一张版本表，记录已执行的迁移。
2. 新迁移只执行一次，在事务里执行，失败时报出迁移编号。
3. 旧版程序遇到更新的库会拒绝启动并给出明确提示。
4. 有一条自动化测试保证“旧库升级后的结构”与“全新建库的结构”一致。

不在本阶段范围内：改动任何现有表结构；引入外部迁移工具。运行器预计一百行左右，自己实现即可，符合仓库尽量少依赖外部工具的做法。

## 设计

### 版本表

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  applied_at TIMESTAMP NOT NULL
);
```

### 迁移清单

迁移是 Go 代码里的一个有序列表，每项包含编号、名称和执行函数。执行函数拿到事务和方言标识，需要分方言时在函数内部处理。

```go
type migration struct {
    version int
    name    string
    up      func(ctx context.Context, tx *sqlx.Tx, driver string) error
}
```

以上是示意，实际签名以实现为准。

### 基线迁移

编号 1 是基线，内容就是现在的启动流程：整份 `schema.sql` 加上现有的 21 个函数和夹在中间的两段逻辑，顺序原样保留。

- 全新的库：执行基线，得到当前结构，记录版本 1。
- 已有的库：同样执行基线。这些函数本来就是每次启动都跑的幂等逻辑，再跑一次是安全的，然后记录版本 1。
- 之后启动：版本 1 已记录，基线不再执行。

基线内部不重构、不调整顺序。`purgeRevokedServers` 目前处在两个迁移函数之间，基线里保持原位；它同时也是数据维护逻辑，如果启动时仍需要每次执行，就在迁移结束后另外调用一次。动手前先读代码确认它的调用语义。

### 并发与事务

- PostgreSQL：迁移期间持有会话级 advisory lock，避免控制面重启重叠时两个进程同时迁移。
- SQLite：连接池上限已经是 1，不需要额外的锁。
- 每个迁移单独一个事务，迁移内容和版本记录在同一事务里提交。基线迁移例外：它内部的函数现在直接使用 `*sqlx.DB`，把它们全部改成事务版本属于行为重构，不在本阶段做；基线成功后单独写入版本记录即可，因为它是幂等的。

## 步骤

### P1-01 旧库夹具与结构一致性测试

**做法**

1. 为 SQLite 准备一份 0.3.12 的结构夹具：用当前代码新建一个空库，导出结构，存为测试数据。再从 Git 历史里挑一到两个更早的版本（例如某个已知发布过的 0.2.x）导出旧结构，用来模拟真实的升级路径。
2. 新增测试：分别从“空库”和“每份旧结构夹具”打开 Store，然后比较两边的最终结构。SQLite 用 `PRAGMA table_info` 和 `PRAGMA index_list`，比较表名、列名、列类型、非空约束、默认值、索引。
3. 在 PostgreSQL 集成测试里加入同样的比较，查询 `information_schema`。
4. 这一步先于运行器落地。它描述的是当前行为，应该直接通过；如果不通过，说明现有迁移已经有分叉，登记到总览的“发现的问题”并先与用户确认处理方式。

**验收**

- 新测试在两种数据库上通过，或者已把发现的分叉登记并得到处理意见。

**说明**

`schema.sql` 里有些表把后加的列写在 `CREATE TABLE` 的最前面（例如 `gemini_threads` 的 `thinking_level`）。比较结构时不要比较列顺序，只比较列集合。

### P1-02 迁移运行器与 `schema_migrations`

**做法**

1. 新增 `internal/store/migrate.go`，实现上面“设计”一节的版本表、清单和运行器。
2. 把 `OpenWithOptions` 里从执行 `schema.sql` 到最后一个 `migrate*` 调用的整段搬进基线迁移函数，顺序不变。`OpenWithOptions` 只保留打开连接、调用运行器、返回 Store。
3. 现有的 `migrate*` 函数留在原文件，不改名、不改内容。文件拆分留给 P3-03。
4. 新增测试：全新库执行后版本为 1；重复打开不再执行基线（可以用计数或日志断言）；人为让某个迁移返回错误时，错误信息包含迁移编号和名称，且版本表没有该记录。

**验收**

- P1-01 的结构一致性测试仍然通过。
- `make test-postgres` 通过。
- 用一份真实数据的副本（生产库的 dump 恢复到临时实例）启动新版本，确认正常启动且版本表有一条记录。这一步需要用户提供或授权访问数据副本；拿不到时在“执行记录”里注明未做。

**风险与回滚**

这一步之后，回滚到旧版控制面仍然安全：旧版会忽略 `schema_migrations` 表，继续按老方式每次启动跑全部函数。

### P1-03 降级保护

**做法**

启动时如果库里的最大版本号大于程序内置清单的最大编号，拒绝启动，错误信息写明库版本、程序支持的版本，以及“请使用不低于某版本的控制面”。

**验收**

- 测试：手工往版本表插入一个更大的编号后打开 Store，得到预期错误。

**说明**

这个保护只对包含本步骤之后的版本生效，更早的版本不认识版本表。所以 P2 的破坏性迁移必须等本步骤发布并运行过至少一个版本之后再做，并且在升级说明里写明不可回滚到哪个版本之前。

### P1-04 迁移编写规范与升级文档

**做法**

1. 在 `docs/OPERATIONS.md` 增加“数据库迁移”一节，写明下面的规则：
   - 新迁移追加到清单末尾，编号递增，已发布的迁移不可修改。
   - 每个迁移必须同时支持 SQLite 和 PostgreSQL（决策 D1 改变前），并在两种数据库的测试里各跑一次。
   - 先扩展、后收缩：新增列或表的版本，与删除旧列或旧表的版本，至少隔一个发布版本。
   - 破坏性迁移要在提交信息和升级说明里标注，并写明最低可回滚版本。
   - `schema.sql` 冻结为基线，不再修改；所有结构变化只通过新迁移表达。
2. 在 `docs/PRODUCTION_DEPLOYMENT.md` 的“更新、回滚与备份”一节补充：升级前的数据库备份是迁移失败时的唯一恢复手段；降级保护的行为和错误信息。
3. 把 P1-01 的结构一致性测试做成模板：新增迁移时，只需要增加一份“迁移前”的夹具。

**验收**

- 两份文档的新内容与实现一致。
- 用一个示例迁移（例如给某张表加一个带默认值的列）走一遍完整流程，验证后撤销，不提交。

## 阶段完成标准

- 进度总表里 P1 的四个步骤全部 `已完成`。
- 生产环境已经运行过带版本表的版本，且版本表里有基线记录。

## 执行记录

按时间顺序追加，每条包含日期、步骤编号、会话标识、做了什么、验收结果、遗留事项。

### 2026-11-13 P1-01

会话标识：`11-13 Codex`。

新增两种数据库各三份纯结构 SQL 夹具，以及旧库升级、新库创建和连续两次打开的结构比较。只涉及 `internal/store` 的测试、测试数据和本阶段记录；迁移函数、`schema.sql` 和 `Makefile` 均未改动。没有引入迁移运行器或版本表。

#### 夹具来源

| 版本 | 来源提交 | 发布记录与选择理由 | 初始表数（两种数据库相同） |
| --- | --- | --- | --- |
| 0.2.45 | `ea21636713d11fa8ed1678583cd24eb91fdceadd` | 2026-08-04，提交说明“发布 Agent 0.2.45”；覆盖三个多月、早于 Gemini 和 Claude 接入的升级跨度 | 34 |
| 0.2.58 | `14797ff995ef031e0cf104dd787e1c11b5c171b5` | 2026-10-23，提交说明“发布 0.2.58”；覆盖 Gemini、Claude 和会话协作扩展之前的较近发布基线 | 36 |
| 0.3.12 | `74697fb45af6ce19209558eebec6ea871bc60e68` | 2026-11-13，提交说明“bump release version to 0.3.12”；当前发布结构，之后到本步骤的基线没有 Store 代码变动 | 56 |

六份夹具位于 `internal/store/testdata/schema/{sqlite,postgres}/`；文件头记录版本、完整提交号、生成方法。它们均由对应提交自己的 `store.Open` 真实创建空库后导出，未从当前 `schema.sql` 推导，未包含业务数据。当前 Store 实际生成 56 张表；总览的 55 张是 `schema.sql` 的静态表数，额外的 `codex_requests` 由 `migrateRetentionAndCodexRequests` 创建。

#### 生成与复现

对上表每个提交分别执行以下操作。尖括号表示自行选择的仓库外临时位置或临时数据库连接，不是仓库中的目录；不需要增加 worktree、分支或产品代码。

```bash
git archive <提交号> | tar -x -C <已创建的仓库外空目录>
```

在导出的源码中临时增加 `cmd/schemafixture/main.go`。小程序只调用该版本的 Store，接受一个数据库连接参数，打开成功后关闭；核心内容如下：

```go
package main

import (
    "log"
    "os"

    "github.com/wio-platform/wio/internal/store"
)

func main() {
    database, err := store.Open(os.Args[1])
    if err != nil {
        log.Fatal(err)
    }
    if err := database.Close(); err != nil {
        log.Fatal(err)
    }
}
```

SQLite：在各自历史源码目录执行，目标文件必须尚不存在；`.schema` 直接读取刚生成数据库的结构。为输出增加对应版本的来源注释后，保存为该版本夹具。

```bash
go run ./cmd/schemafixture <新的空库文件>
sqlite3 <新的空库文件> '.schema' > <结构导出文件>
```

PostgreSQL：使用 0.3.12 源码已有的 `embedded-postgres` 依赖启动独立 PostgreSQL 16 临时实例，端口用 `net.Listen("tcp4", "127.0.0.1:0")` 取得，运行和数据目录均使用新的仓库外临时目录。各版本使用独立空 schema（`v0245`、`v0258`、`v0312`），数据库连接通过 `search_path` 指向对应 schema；用该版本自己的上述小程序创建结构。导出器保存在 `internal/store/testdata/schema/export_postgres.go`，带 `ignore` 构建标签，不进入产品构建。

以下以 0.2.45 为例，`init`、`dump` 在当前仓库中执行，`schemafixture` 在对应历史源码目录执行；其他版本只替换版本和 schema 名：

```bash
go run internal/store/testdata/schema/export_postgres.go init '<临时 PostgreSQL URL>' v0245
go run ./cmd/schemafixture '<临时 PostgreSQL URL>&search_path=v0245'
go run internal/store/testdata/schema/export_postgres.go dump '<临时 PostgreSQL URL>' v0245 > <结构导出文件>
```

示例 URL 已带连接查询参数；没有查询参数时，用 `?search_path=`。导出器从 `pg_attribute`、`pg_attrdef` 读取列类型、非空与默认表达式，从 `pg_constraint` 和 `pg_index` 分别导出约束与非约束索引，调用 `format_type`、`pg_get_expr`、`pg_get_constraintdef`、`pg_get_indexdef` 保留 PostgreSQL 的实际结构。表先全部创建，再创建非外键约束、外键、非约束索引；移除生成用 schema 的限定名前缀，使夹具可恢复到任意独立 schema。增加来源注释后保存，不导出行数据。完成后停止临时实例。

最终夹具由三行来源注释紧接原始导出正文组成，不额外追加换行。以下组装方式中的版本、提交号和生成说明替换为实际内容，第三行之后直接接正文；导出正文自身以一个换行结束，文件末尾没有空行：

```bash
{
  printf '%s\n' '-- Source: Wio <版本>, commit <完整提交号>.' '-- Generated: <对应方言的生成说明>' '-- Structure only; no business data.'
  cat <结构导出文件>
} > <夹具文件>
```

SQLite 的生成说明原文为 `git archive; temporary Go program called that version's store.Open on an empty SQLite database; sqlite3 .schema exported the structure.`；PostgreSQL 的原文为 `git archive; temporary Go program called that version's store.Open on an empty PostgreSQL 16 schema; a pg_catalog exporter emitted tables, constraints, and indexes.`。返工时用上述三行注释与原始导出正文重新组装，六份均经 `cmp` 逐字节比对一致；同时对全部未跟踪文件逐个运行 `git diff --no-index --check /dev/null <文件>`，防止普通 `git diff --check` 漏检新文件。

#### 比较范围与用例组织

- SQLite 顶层测试 `TestSQLiteFixtureSchemaConsistency` 包含三个版本子用例。每份夹具恢复后，由当前 `Open` 升级并采集结构；关闭、再打开并验证结构不变；另建全新库并比较最终结构。
- PostgreSQL 在 `TestPostgresMigrationAndStorage/fixture_schema_consistency` 下加入三个版本子用例，复用原测试的临时实例和动态端口，每个版本各用独立的 `fixture_*`、`fresh_*` schema。既不增加实例、端口和运行目录竞争，也能直接被现有 `make test-postgres` 的测试名过滤器选中。原有存储用例继续使用自己的数据库。
- 两种数据库均比较表名、列名、完整列类型、非空约束、默认值、主键列及主键内的位置；不比较表内列顺序。索引比较名称、所属表、唯一性、索引列及其顺序、部分索引条件；包括主键和唯一约束隐式产生的索引。SQLite 还采集索引排序方向及 collation；条件提取按 SQL token 识别顶层 `WHERE`，不依赖空格、换行或制表符，并跳过引号和注释中的关键字。`partial` 标志与条件存在性不一致、或部分索引条件为空时直接报错，错误包含索引和表名。
- PostgreSQL 比较移除当前隔离 schema 限定名之后的完整 `pg_get_indexdef(indexrelid)`，覆盖排序方向、NULLS 排序、collation、opclass、访问方法、INCLUDE 列、存储参数和部分索引条件；不再使用会丢失列级选项的带列号重载。默认表达式和谓词只去首尾空白，保留字符串内部空白。PostgreSQL 专用采集函数放在带 `postgresintegration` 构建标签的测试文件中。
- 外键：SQLite 比较引用表、源列与目标列的有序集合、更新与删除动作、MATCH；PostgreSQL 比较约束名及 `pg_get_constraintdef` 输出（包括列、引用、动作和延迟选项），仅去掉当前独立 schema 的限定名前缀。
- 未比较：数据及回填内容、CHECK、触发器、视图、独立序列、权限、表存储选项、表列自身的 collation、生成列/隐藏列；SQLite 外键延迟选项、表达式索引的表达式文本。PostgreSQL 索引的访问方法、INCLUDE、存储参数和列级排序/collation/opclass 选项已由完整定义覆盖。当前夹具没有 SQLite 表达式索引；后续增加此类结构时需要扩展采集器。
- 已知分叉清单 `knownSchemaDivergences` 当前为空。预留的豁免必须精确匹配数据库、夹具版本、差异路径、升级值和新库值；新分叉、分叉值变化或旧豁免消失都会失败。

新增 2 个 Go 顶层测试、6 个版本子用例（SQLite 3 个、PostgreSQL 3 个，另有 PostgreSQL 分组节点），以及 8 个 SQLite 条件提取回归子用例，覆盖换行、制表符、引号和注释、隐式索引、标志不一致及空条件。默认构建下 Store 顶层测试数由返工前的 169 增至 170；包含 `postgresintegration` 标签时由 171 增至 172。

#### 验收与反向验证

所有重型 Go 命令串行执行；审阅返工后的重型命令均持有共享文件锁。返工前运行全量 Go 时按总览已登记的问题隔离 Git 配置注入，本轮按审阅要求未重跑 `go test ./...`，该行保留返工前的结果。前端依赖安装、构建仅用于提供 Go 嵌入产物，本步骤未运行前端测试和独立类型检查。耗时为命令的实际墙钟时间，包内测试耗时另列；返工命令的耗时包含等待共享锁的时间。

| 命令 | 退出码 | 耗时 | 关键结果 |
| --- | --- | --- | --- |
| `npm --prefix web ci` | 0 | 7.06 秒 | 安装 552 个包；安装器提示 17 项 audit 问题（3 low、6 moderate、6 high、2 critical），未运行审计或修改依赖 |
| `npm --prefix web run build` | 0 | 25.50 秒 | 生成 Go 所需的前端嵌入产物，包体积检查通过 |
| `gofmt -l cmd internal web/embed.go` | 0 | 0.20 秒 | 审阅返工后重跑；文件列表为空 |
| `go vet ./...` | 0 | 1.90 秒 | 审阅返工后重跑；无诊断输出 |
| `go vet -tags=postgresintegration ./internal/store` | 0 | 0.59 秒 | 审阅返工后运行；无诊断输出 |
| `go test ./internal/store -count=1` | 0 | 149.88 秒 | 审阅返工后重跑；包内耗时 148.846 秒；170 个顶层测试，包含三个 SQLite 夹具与八个条件提取子用例 |
| `go test ./...`（返工前取得，本次未重跑） | 0 | 173.62 秒 | 30 个测试包全部通过，部分未受影响包命中缓存；Store 包耗时 163.580 秒 |
| `make test-postgres` | 0 | 22.04 秒 | 审阅返工后重跑；包内耗时 18.628 秒；原有完整入口及三个 PostgreSQL 夹具子用例通过 |

反向验证在测试中临时给 0.3.12 夹具升级后的 `events` 表执行 `ALTER TABLE events ADD COLUMN p1_01_reverse_probe TEXT`，在第一次结构采集之前注入。该列在第二次打开后仍保留，因此最终“升级库对新库”的断言失败。两种数据库均精确报出：

```text
column events.p1_01_reverse_probe: upgraded="present" fresh="<missing>"
```

上述新增列反向验证在审阅返工前取得：SQLite 命令退出码 1，耗时 5.36 秒；PostgreSQL 命令使用完整 `TestPostgresMigrationAndStorage` 入口，退出码 1，耗时 24.19 秒，唯一失败为上述新增列。注入代码已经还原，夹具与产品代码未受影响。

审阅返工后的 PostgreSQL 反向验证：临时将 0.2.58 夹具的 `audit_time_idx` 改为 `(occurred_at DESC)`，加锁运行 `go test -tags=postgresintegration ./internal/store -run TestPostgresMigrationAndStorage -count=1`，退出码 1、耗时 22.36 秒，唯一失败为该索引定义差异，输出如下。随后还原夹具，六份夹具与原始导出重新组装后的字节比对全部一致。

```text
index audit_time_idx definition:
  upgraded="CREATE INDEX audit_time_idx ON audit_log USING btree (occurred_at DESC)"
  fresh="CREATE INDEX audit_time_idx ON audit_log USING btree (occurred_at)"
```

新增 SQLite 条件提取回归用例与三个 SQLite 夹具的定向运行退出码 0、耗时 7.90 秒（包内 4.530 秒）。夹具字节比对及全部未跟踪文件的逐文件空白检查总耗时 0.06 秒，聚合检查退出码 0、无空白诊断；`git diff --no-index --check` 对非空新文件返回 1 表示存在新增差异，检查同时确认无空白错误输出。六份夹具末尾均只保留正文最后一个换行，不再有额外空行。

另观察到现有 PostgreSQL 测试不能通过更细的 `-run` 过滤器独立运行任意子用例：父测试调用的 `assertCodexSnapshotTimeoutAndRetry` 会在若干 `t.Run` 中准备后续父测试代码依赖的缓存；过滤掉这些子测试后，后续断言会报 `unexpected snapshot`。完整入口没有出现该失败，未据此认定产品缺陷，也未修改原用例。

发现的结构分叉：没有。遗留事项：本步骤未更改产品行为；CI 上的前端测试与类型检查仍由统一工作流执行。
