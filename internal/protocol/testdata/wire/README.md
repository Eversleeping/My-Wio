# Agent 线上格式基准

此目录的 JSON 夹具只增不改。字段或格式演进时保留已有样本，新增变体；不得通过重写旧夹具来让协议变更通过测试。载荷和结果类型登记在 `../../wirecontract/registry.go`，测试同时检查常量、注册项与样本覆盖。

每份文件包含基线提交、方向、线上类型、生产者调用栈和实际 JSON。`operation` 是 Agent 解密后收到的命令；`result` 是操作回执里的 `data`，按结果 DTO 类型覆盖（多个操作可共用一种结果）；`upstream` 是 Agent 信封里的载荷。没有结果 DTO 的操作仍发送通用 `OperationResult` 回执。

## 来源与规范化

主基线是 `510b0cc`（Agent 0.3.23）；补充生产者测试来自 `dc40819`。在独立源码副本中运行 Store、HTTP、Agent、网关和原生适配器测试，通过临时 SQLite 插入/结果更新触发器、真实入队入口及生产函数返回值采集样本。密文只用这些隔离测试的开发 Vault 解密，无法解密的伪密文跳过。采集代码和原始日志不进入仓库。

- 操作优先来自真实 HTTP / Store 业务生产者，覆盖全部 56 种精确操作；三个前缀和两个引擎派生后缀不作为独立操作。
- Agent 心跳、指标、库存、三引擎事件、所有权、回执等来自实际出站入口。旧心跳另外从已发布 `25596d0`（0.3.21）的隔离 Agent 采集，确认没有能力字段时仍可读取。
- `TestWireFixtureOrphanWorktreeCleanup` 是采集副本里的补充场景：continued fork 缺少原生线程结果导致持久化失败，由网关实际排入 `git.worktree.cleanup`。
- `TestWireFixtureWorkspaceWrite` 在临时 Git 仓库执行 `remote.add`，从 Agent 的真实操作回执采集写操作及其快照结果。
- `TestWireFixtureUpdateProducer` 在隔离副本执行 `client.go` 确认升级分支原有的完整入队表达式（`version` / `status` 两字段），未连接真实服务器。
- `TestWireFixtureOptionalVariants` 以已采集 JSON 为底稿补齐未出现的可选字段，解码成真实 DTO，再经过 Store 入队或 Agent 出站入口。它们检验序列化格式，不作为组合选项的业务合法性样例；多引擎 worktree 标识等可选字段在实际业务中可分别出现。

临时路径替换为 `/fixture/work`；随机 UUID、时间、测试监听端口、主机名和指标值替换为固定合成值。其余固定值（包括测试代码里明文写出的假凭据、示例域名及产品默认目录）来自测试数据；不读取线上数据库、本机部署档案、真实凭据或项目 `.env`。

## 验证

```bash
go test ./internal/protocol/...
```

测试要求每种操作和上行信封都有样本、每种已注册结果 DTO 有样本；严格拒绝未知字段，重新编码后 JSON 语义必须一致，包括数值精度、字段缺失与 `null` 的差异。动态 CLI 内容及协作工具参数保持原有 `json.RawMessage` 边界。

新增操作的顺序见 `../../doc.go`：常量与清单 → DTO 与注册项 → 真实生产者夹具 → Agent 处理器 → 按需增加能力声明。
