# 数据库迁移测试模板

`schemaFixtureVersions`（[schema_consistency_test.go](../../schema_consistency_test.go)）
驱动两种数据库的同一组历史夹具。新增迁移时复用这个入口，无需复制测试函数。
原始历史夹具的来源及导出方法见 [P1 执行记录](../../../../docs/roadmap/P1-database-migrations.md#执行记录)。

## 增加迁移与夹具

1. 从新增迁移前的已发布提交创建隔离临时数据库，用该提交自己的 `store.Open`
   建库。不能用已加入新迁移的代码反推旧结构，也不使用生产数据。
2. 导出 SQLite 与 PostgreSQL 的结构，分别写入 `sqlite/<版本>.sql` 和
   `postgres/<版本>.sql`。PostgreSQL 复用本目录 `export_postgres.go` 的 `dump`
   子命令；SQLite 使用 `.schema`。只保存结构与迁移元数据，禁止业务行或凭据。
3. 若旧版本已有 `schema_migrations`，在结构正文后追加该临时库实际已完成的全部
   迁移记录。复制编号、名称，时间统一为固定测试时间；不能只导出空版本表。
   例如只有基线的库追加如下语句，版本更多时逐条记录：

   ```sql
   INSERT INTO schema_migrations(version, name, applied_at)
   VALUES (1, 'baseline', '2000-01-01 00:00:00');
   ```

同一 Agent 版本可以跨多个控制面 schema；`0.3.21.3` 标识版本 0.3.21 / schema 3，保留已发布的 `0.3.21` / schema 1 夹具不变。

4. 两份文件头记录来源发布版本、完整提交号、导出方法及“不含业务数据”。在
   `schemaFixtureVersions` 末尾加上同一个版本标识。文件名用点分数字版本，供
   PostgreSQL 测试安全生成临时 schema 名。
5. 在 `migrate.go` 的 `schemaMigrations` 末尾追加新迁移，并增加迁移前后数据断言。
   结构比较入口自动恢复每份夹具、打开升级、重开验证稳定，再与新建库比较。
   不修改已发布夹具来掩盖差异。新增表达式索引等结构时，先确认采集器覆盖它；
   当前比较范围和局限见 P1 执行记录。

## 示例（仅演练，不加入产品清单）

在独立临时源码副本的清单末尾追加以下条目。编号用当时最大编号加一；当前最大编号为 4，所以下例使用 5。
此例使用两种数据库均支持的 SQL，在已有 `events` 行上自动提供默认值。

```go
{
    version: 5,
    name: "example_event_source",
    up: func(ctx context.Context, tx *sqlx.Tx, driver string) error {
        _, err := tx.ExecContext(ctx,
            "ALTER TABLE events ADD COLUMN migration_example TEXT NOT NULL DEFAULT 'legacy'")
        return err
    },
},
```

演练至少覆盖空库、全部历史结构夹具、重复打开、已有行读取到 `legacy`、新写入行默认值
和版本 5 只记录一次。追加示例迁移后，可在测试中用
`runMigrations(..., schemaMigrations[:4])`
准备迁移前的隔离库并写一条事件，再执行完整清单；两个方言共用断言，PostgreSQL
入口挂在 `TestPostgresMigrationAndStorage` 下，确保 `make test-postgres` 选中。
框架的事务失败、数据/结构回滚与重试断言已由 `assertMigrationRunner` 共用覆盖。

```bash
go test ./internal/store -run 'TestSQLiteFixtureSchemaConsistency|TestMigrationRunner' -count=1
make test-postgres
```

真实迁移还须执行仓库通用验收。演练结束后删除整个临时源码副本，确认示例列与
示例条目没有进入工作分支。备份与回滚流程见[运维文档](../../../../docs/OPERATIONS.md#数据库迁移)。
