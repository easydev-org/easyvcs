# 性能特征与基准

本文记录 EasyVCS 的**性能特征**（何处快、何处慢）与**可复现的基准**。运行：

```sh
go test ./store/   -run '^$' -bench . -benchtime=2s
go test ./transfer/ -run '^$' -bench . -benchtime=2s
```

> 数字来自单机 SQLite（纯 Go，`SetMaxOpenConns(1)`）。Postgres 的绝对数不同，
> 但**相对结论**一致。

## 1. 写路径（提交）

| 基准 | 结果 | 说明 |
|---|---|---|
| `BenchmarkCommit` | **~2.2 ms/commit** | 一次提交（1 个文件）在 SQLite 上；含对象 + snapshot + revision + 路径索引写 |

**一次提交写什么**：`objects`（blob + tree）+ `snapshots` + `revisions` +
`revision_path_dirs`（改动路径 × 深度 行）。**中心 DB 写**，同 namespace 串行。

**对比 git**：git 追加到本地 packfile（顺序写，无中心锁）。**git 单次提交更快**，
且**无中心瓶颈**。easyvcs 的代价是"中心化 + 多表写"；回报是"服务端集中管理 +
路径索引 + 无工作树"。

## 2. 路径索引（子作用域查询）

| 基准 | 结果 | 说明 |
|---|---|---|
| `BenchmarkRevisionsTouching` | **~35 µs** | 2000 个提交下，"触碰某路径的 revision"查询 |
| `BenchmarkListRevisions` | **~8.1 ms** | 列出全部 2000 revision（log 的数据源）|

**关键**：`RevisionsTouching` 是 **O(触碰该路径的 rev)**，与总提交数**无关**；而
`ListRevisions` 是 **O(全部)**。**这正是"子目录历史"比 git 快的地方** —— git 的
`git log -- path` 仍需遍历。索引就绪标志已缓存（避免每次 COUNT）。

## 3. 同步（push/fetch delta）

| 基准 | 结果 | 说明 |
|---|---|---|
| `BenchmarkCollectFull` | **~2.5 ms** | 收集全量 bundle（对端什么都没有）|
| `BenchmarkCollectDeltaSkip` | **~0.94 ms** | 对端已有一切（全跳过）|
| `BenchmarkCollectBloomDelta` | **~1.8 ms** | 用服务端 Bloom 做对象级 delta |
| `BenchmarkBloomLookup` | **~0.22 µs** | 单次 Bloom 成员查询 |

**对象级 delta**：advertise 返回服务端对象集的 **Bloom filter**；push 用它跳过服务端
已有的对象。**单次查询 0.22 µs**，即使大仓库（百万对象）也是可接受的每对象开销。
**不发全量 id 列表**（Bloom 很小）。

## 4. 冷启动

**只读命令**（`log`/`show`/`refs`/`diff`/`verify`/`repositories`）**懒开库、不
SetWAL** —— 打开 DB 与 `PRAGMA journal_mode=WAL` 都是写操作，纯读不应付这个代价。
**可选 daemon**（`easyvcs daemon`）把 store 常驻，**彻底消除每次命令的冷启动**。

## 5. 已知短板（诚实）

| 短板 | 影响 | 缓解 |
|---|---|---|
| **无 delta 压缩**（blob 整块存）| 频繁改**大文件** → DB 快速增长 | 大文件不频繁改时**可接受**（内容寻址仍去重；读 O(1)）；将来可按大小启用 delta |
| **中心 DB 写** | 同 namespace 提交串行 | **跨 namespace 并行**（advisory lock 按 repo）；Postgres + 多副本 |
| **多表写 + 索引写放大** | 单次提交 ~2.2 ms | 批量写、异步索引（未做）|
| **读走 DB** | tree 每次 `ReadObject` | 对象 LRU 缓存（未做）|

## 6. 适用边界

- **适合**：服务端集中协作、多租户、子目录（monorepo）语义、无需完整 clone 的提议
  （sandbox → snapshot → RR）、结构化冲突。
- **不适合**：极致本地提交吞吐（用 git）、频繁改大文件（delta 更省）、离线开发。
