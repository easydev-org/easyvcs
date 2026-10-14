# Namespace 作为唯一容器（repo 消失）

本文定义 EasyVCS 的**目标数据模型**：**namespace 是唯一的顶层容器**，其下
**不再有 repo 实体**——原来叫"仓库"的东西，只是 namespace 内的一个**子路径
（path scope）**。这与 `docs/subrepo-scopes.md`（路径作用域 + 投影）合起来构成
完整模型。

> 关联：`subrepo-scopes.md`（refs/RR 的 path 作用域与投影规则）。
> **术语**：namespace（顶层容器，= 原有的 org）、path（namespace 内的子目录
> 作用域）、scope = `(namespace, path)`。

## 1. 核心命题

> **一个 namespace = 一套对象存储 + 一个 revision DAG。**
> 子目录（path）是**该 DAG 的投影**，不是独立的仓库。

```
namespace easydev-org
  = 一套 objects + 一个 revisions DAG（共享）
  ├── easyvcs/               ← scope(path="easyvcs")
  └── easyagent/             ← scope(path="easyagent")
        ├── frontend/         ← scope(path="easyagent/frontend")
        └── backend/          ← scope(path="easyagent/backend")

不同 namespace 之间完全隔离。
```

**推论**：
- 一个 revision（父目录的大改动）在 `easyagent/frontend` 的投影里**自动只含
  自己路径的小改动**（`subrepo-scopes.md` §6 / `RevisionsTouching` 索引）。
- `easyvcs/` 与 `easyagent/` **共享同一份 DAG**（同一个 namespace）。
- **repo / org 概念消失**：org → namespace；repo → path。

## 2. 现状 vs 目标

| 维度 | 现状 | 目标 |
|---|---|---|
| 顶层容器 | `namespaces` + `repositories`（两张表）| **`namespaces`（唯一）** |
| "仓库" | `repositories` 行，用 `(namespace, name)` 定位 | **不存在**；`(namespace, path)` 是 scope |
| 子表外键 | `repo_id` → `repositories.id` | **`namespace_id` → `namespaces.id`** |
| 对象存储 | `objects`（全局，内容寻址）| **不变**（全局） |
| DAG | **每 repo 一个** | **每 namespace 一个** |
| API | `/api/v1/namespaces/{ns}/...` | `/api/v1/namespaces/{ns}/...?path=<path>` |
| org | `/api/v1/namespaces`（= namespace）| `/api/v1/namespaces` |
| 权限 | `repo_members`（按 repo）| **`namespace_members`**（按 namespace）|

## 3. Schema 迁移

### 3.1 `namespaces` 吸收 `repositories` 的容器字段

```
namespaces:
  id, owner_user_id, name, created,
  description, visibility, default_branch, kind,
  mirror_url, mirror_branch, mirror_interval, mirror_last_rev,
  mirror_last_sync, mirror_last_error, mirror_token,
  protected_branches
```

`repositories` 表**删除**；其行不再需要（namespace 行就是容器）。

### 3.2 子表的 `repo_id` → `namespace_id`

以下表的主键/外键由 `repo_id`（FK `repositories.id`）改为 `namespace_id`
（FK `namespaces.id`），**语义不变**（只是容器换了）：

```
snapshots, revisions, revision_path_dirs, refs, workspaces,
remotes, remote_refs, push_mirrors,
revision_requests, rr_merge, git_revision_links
```

`objects` 保持**全局**（内容寻址、跨 namespace 共享字节——与今天跨 repo 共享
一致）；namespace 隔离在**元数据/访问**层，不在对象层。

### 3.3 `repo_members` → `namespace_members`

协作角色从"按 repo 授权"改为"**按 namespace 授权**"。

### 3.4 `path` 已就位

`refs.path`（S1）、`revision_requests.path`（S4）、`revision_path_dirs`
（路径索引）已实现——**path 作用域这一半已完成**，本重构只做"容器合并"。

## 4. 领域类型与 API

### 4.1 类型

```
RepoRef{Owner, Namespace, Name}   →   NSRef{Owner, Namespace}         // 容器
Repo（handle，repoID）             →   Namespace（handle，namespaceID）  // 容器句柄
RepoInfo                          →   NamespaceInfo
```

一个 scope（`Namespace` + `path`）在语义层由 `revision.Workspace` 表达；refs /
revisions / RR 查询按 `path` 过滤（`ListRefsIn` / `RevisionsTouching`）。

### 4.2 API

**容器操作**（namespace 级，`?path=` 收窄到 scope）：

```
GET    /api/v1/namespaces                          list
POST   /api/v1/namespaces                          create
GET    /api/v1/namespaces/{ns}                     metadata
PATCH  /api/v1/namespaces/{ns}                     update
DELETE /api/v1/namespaces/{ns}                     delete

GET    /api/v1/namespaces/{ns}/refs?path=<p>       refs (projected)
POST   /api/v1/namespaces/{ns}/branches {path}     create branch
GET    /api/v1/namespaces/{ns}/tags?path=<p>
POST   /api/v1/namespaces/{ns}/commit {path,...}   commit / lift
POST   /api/v1/namespaces/{ns}/snapshot {path,...}
GET    /api/v1/namespaces/{ns}/tree?path=<p>&ref=
GET    /api/v1/namespaces/{ns}/log?path=<p>
GET    /api/v1/namespaces/{ns}/rr?path=<p>
POST   /api/v1/namespaces/{ns}/rr {path,...}
GET/PUT /api/v1/namespaces/{ns}/protected-branches
...
```

**git 端点**（子路径即独立 git 仓库，`gitserver` 已支持 subpath）：

```
git clone http://host/api/v1/namespaces/{ns}/{path...}
  e.g. .../api/v1/namespaces/easydev-org/easyagent/frontend
```

### 4.3 兼容

旧路径 `/api/v1/namespaces/{ns}/...` 可**保留为别名**一段时间（把 `name`
当作 `path` 的第一段），以平滑迁移客户端；或**直接切换**（若可接受破坏性）。
本设计默认**直接切换**（与"从新出发"一致）。

## 5. 落地阶段

| 阶段 | 内容 | 影响 |
|---|---|---|
| **N1** | schema：`namespaces` 吸收容器字段；删 `repositories`；`repo_id`→`namespace_id`；`repo_members`→`namespace_members` | store 全量 |
| **N2** | store：`Repo`→`Namespace` 句柄；`NSRef`；全部 `repoID` 用法换成 `namespaceID`；`Create/Open/List/Delete` 按 namespace | store + 32 文件 |
| **N3** | server：路由 `/namespaces/{ns}/...?path=`；`repo(w,r)` → `ns(w,r)` + `scopePath` | server 55 路由 |
| **N4** | revision/rr/transfer/mirror 的 `RepoStore` 实现对齐；CLI（`namespace` 命令替换 `repositories`）| 各包 |
| **N5** | 测试全量改 + 文档 | — |

**N2 是核心**：一旦 `Repo` 句柄的 `repoID` 变成 `namespaceID`，其下 67 个方法
的查询自然按 namespace 作用域——**path 投影是它们之上的过滤**（已在 S1–S4 里
作为 `*Ref`/`RevisionsTouching`/RR.path 提供）。

## 6. 风险与取舍

| # | 风险 | 处理 |
|---|---|---|
| 1 | **一次性改动巨大**（11 表 + 55 路由 + 67 方法）| 分 N1–N5，每阶段编译 + 测试 |
| 2 | **数据迁移**：旧库 `repositories` 行如何变成 namespace | 无存量（"从新出发"）：新库直接建目标 schema，**无迁移** |
| 3 | **API 破坏性**（客户端需改 URL）| 接受（rebrand/新模型）；或保留 `/repos` 别名 |
| 4 | **权限模型变化**（repo→namespace 授权）| `namespace_members` 语义等价于"对整个 namespace 授权" |
| 5 | **一个 namespace 一个大 DAG** | 正是目标；`RevisionsTouching(path)` 保证 scope 操作 O(subtree) |
| 6 | **对象全局共享**（跨 namespace 可见）| 与现状一致（今天对象已跨 repo 全局）；隔离在访问层 |

## 7. 不变的部分

- **对象层**（`object`：blob/tree/conflict）**完全不变**。
- **revision 语义**（稳定 id、rebase/merge/resolve）**不变**。
- **path 作用域 + 投影**（`subrepo-scopes.md`）**不变**，本重构只是把它的
  **容器**从 repo 提到 namespace。
- **RR / 冲突 / 镜像** 机制不变，只是作用域从 repo 变 namespace（+ path）。
