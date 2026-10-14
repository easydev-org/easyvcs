# 子目录作用域（Sub-repo scopes）设计

本文定义 EasyVCS 的**路径作用域**模型：把一个大仓库的**子目录**当作一个
「类 repo」来用——可以有自己的分支、可以开 RR、可以 release、可以 `git clone`、
可以当包消费——**而不复制任何对象**。

> 关联文档：`architecture.md`（引擎与对象模型）、`collaboration.md`（RR）、
> `conflict-model.md`（一等冲突）。**术语**：`revision_id`（稳定逻辑修改）、
> `snapshot`（内容寻址版本）、`branch`（可变 ref）、`tag`（不可变 ref）、
> `scope`（本文核心：`(repo, path)`）。

## 1. 核心命题

> **子目录不是新 repo，而是一个「路径作用域」（scope）。**

一个 scope 是 `(repo, path)`，其中 `path` 是仓库内的目录（`""` = 根）。
**对象存储（tree/blob/snapshot/revision）全局唯一、不复制**；scope 只是给
**元数据**（ref / RR / release / 保护规则）加一个**路径维度**，并定义一条
**投影（projection）**规则：父 scope 的元数据如何「流下」到子 scope，而子
scope 的元数据**不回流**到父。

这与 git 的 submodule/subtree **本质不同**：

| | git submodule / subtree | EasyVCS scope |
|---|---|---|
| 对象 | 另一个 repo 的对象（引用/复制） | **同一份对象，不复制** |
| 子目录历史 | 需要外部 repo | **父历史按路径投影** |
| 存储代价 | submodule=0（外链）/ subtree=复制 | **O(1) 额外** |
| 分支 | 每个 repo 独立 | **父分支投影到子，子分支 scoped** |

## 2. 术语与不变量

| 名称 | 含义 |
|---|---|
| **repo** | `(namespace, name)`，一套对象存储 |
| **scope** | `(repo, path)`；`path=""` 是**根 scope** |
| **路径投影** `π(P, Q)` | 把路径 `P` 的树的子树 `Q` 取出 |
| **作用域 ref** | 带 `path` 的 ref（`path=""` = 根 ref）|

**不变量**：
1. **对象共享**：子 scope 不新增任何 tree/blob/snapshot 行。
2. **ref 投影单向**：根 ref 对子 scope 可见；子 ref 对父/兄弟不可见。
3. **revision 稳定**：`revision_id` 永不因 rebase/投影而变。

## 3. 元数据加一列 `path`

所有「repo 相关内容」统一**加一个 `path` 列**（默认 `""`）：

| 实体 | 表 | 新主键 / 列 |
|---|---|---|
| **refs（branch/tag）** | `refs` | PK `(repo_id, path, name)` |
| **RR** | `revision_requests` | `path` |
| **release** | `releases` | `path` |
| **保护规则** | `protected_branches` | PK 含 `path` |

`path=""` 表示根作用域（与现状完全等价）。迁移：现有行补 `path=""`。

**这是唯一的结构改动**——其余都是**投影规则**（读时计算，不落库）。

## 4. 投影规则（本文的核心）

给定 scope `Q`（`Q` 可以是 `""`、`sub`、`sub/deep`），它可见的 refs 是：

```
refs(Q) =
    { r : r.path == ""  且 tip(r) 在 Q 处是一个目录 }   // ① 根 ref 投影下来
  ∪ { r : r.path == Q 或 Q 是 r.path 的前缀 }           // ② Q 自己的 ref
```

投影方向：

```
       根  main ──────────────►  sub 看到 main（投影，只读）
       sub  feature-x ────✗     根 / 兄弟看不到 feature-x
```

**① 父 branch → 子目录都有**：根 `main` 在每个子 scope 里都「存在」
（**投影视图**，不是独立 ref）。
**② 子目录 branch → 父没有**：`path=sub` 的 ref 只在 `sub` 及更深可见。

> **关键选择**：子 scope 看到的根 branch 是**投影**（只读），**不是独立 ref**。
> 否则根 `main` 与 `sub` 的 `main` 会各自前进、**发散**——那就退化成了「两个
> repo」，正是本模型要避免的。

## 5. 提交与「lift」（子 → 父）

在 `Q` 上「提交到 main」不是移动一个 `sub/main` 指针，而是**lift**：

```
lift(Q, 改动集):
  以「根 main 的 tip」为父，把它的 tree 中 subtree(Q) 替换为
  「投影后的新 tree」，生成一个新 revision（作用域标记 path=Q）
  并更新根 ref main → 新 revision
```

于是：
- **根 main 是唯一真相**；子 scope 的 main 永是它的投影。
- **父的一次大 revision**（动了很多目录）在子 scope 里**自动只表现为
  触碰自己路径的那部分**（见 §6）。

## 6. 父 revision → 子目录「小 revision」（零存储）

一个父 revision `R`（tree `T`）在 scope `Q` 上的**投影 revision**：

```
π(Q, R):
  sub_rev_id = H(R.id ‖ Q)          // 确定性；父不变则子不变
  tree       = subtree(T, Q)         // 只含 Q 下的内容
  parents    = [ π(Q, p) for p in R.parents ]   // 递归投影父
```

- **子 scope 的历史** = `RevisionsTouching(Q)`（**路径索引**，见 §8），
  每个做一次 `π(Q, ·)`。
- **`sub_rev_id` 不落库**——按需算，**存储 O(1)**。

## 7. 子 scope 的 branch / RR / release

### 7.1 子目录专属 branch（真 ref）
```
PUT /api/v1/repos/{ns}/{name}/refs   { path:"frontend", name:"feature-x", target:<rev> }
```
存 `refs(repo, "frontend", "feature-x", ...)`。**往上不可见**。

### 7.2 根 branch 在子 scope（投影）
```
GET /api/v1/repos/{ns}/{name}/refs?path=frontend
  → 含 { name:"main", path:"", projected:true }（读时计算）
```

### 7.3 子目录 RR
RR 的 `source_snapshot` / `target_branch` 都是**根仓库的 rev/snapshot**；
新增 `path` 字段把它**收窄到子目录**：diff / 冲突 / 合并都在该子树内。

### 7.4 子目录 release
`releases` 加 `path`：一个 tag 可以属于 `frontend`（「frontend v1.2」），
其内容 = 该 tag 处 `subtree(frontend)`。

## 8. 性能（为什么不会「乘以子目录数」）

核心支撑是既有的 **`revision_path_dirs` 索引**：每次写入 revision 时记录
它**触碰了哪些目录**。于是：

| 操作 | 复杂度 |
|---|---|
| 子 scope 的 refs | O(该 scope 的 refs) |
| 子 scope 的历史 | **O(触碰该路径的 revision)**，非 O(全部) |
| 子 scope 的 `git clone` | **O(该路径的 revision)**（gitserver 用索引跳过无关 snapshot）|
| 子 scope 的 RR | O(该路径的 diff) |
| 父 revision | O(改动) |
| **额外存储** | **O(1)**（对象共享） |

即：**父的一次大 revision 是 O(改动)**，每个子 scope 各是 **O(自己路径)** ——
**不随子目录数量线性增长**。

## 9. 与 git 端点的一致性

`gitserver.Build(ws, repo, subpath)` 已把子路径导出为独立 git 仓库；
scope 的 refs 投影**恰好**定义了 `git clone .../{ns}/{name}/{subdir}` 看到
的 refs。因此：

```
git clone http://u:t@host/api/v1/repos/easydev-org/easyagent/frontend
  → 该 scope 的投影 refs + 专属 refs
npm i git+http://.../easydev-org/easyagent/frontend.git
```

**写仍走 API/RR**（smart HTTP 只读），与本仓库现状一致。

## 10. 难点与取舍

| # | 难点 | 处理 |
|---|---|---|
| 1 | **投影 ref 的发散** | 用**只读投影**（§4）；写 = lift 到根（§5）|
| 2 | **跨目录移动**（`a/x`→`b/x`）| 路径索引记两个目录；两边历史都含，可接受 |
| 3 | **同名冲突**（`sub/main` 显式 vs 根 `main`）| PK 含 `path`：`(repo,"", "main")` 与 `(repo,"sub","main")` 并存 |
| 4 | **删除传播** | 删根 ref → 投影自动消失；删子 ref → 只删子 scope |
| 5 | **保护规则** | 同样加 `path`（「frontend 的 main 受保护」）|

## 11. 落地阶段

| 阶段 | 内容 | 状态 |
|---|---|---|
| **S1** | refs 加 `path` + 作用域 refs API + **投影规则** | ✅ 已完成 |
| **S3** | `lift`（子 → 根）：子目录改动提交到根 revision | ✅ 已完成 |
| **S4** | RR 加 `path`（子目录 RR，diff/合并按 path 收窄）| ✅ 已完成 |
| **S2** | `π(Q, R)` 派生 revision_id（零存储） | ❌ **不做** |

**关于 S2（决定不做）**：S2 原本要给「子目录视角的 revision」一个派生的稳定 id
（`H(R.id‖Q)`）。但 **RR 的主体是 `source_snapshot`（内容寻址的 snapshot），
不是 revision id**；而 snapshot 天然包含整仓内容，「子目录视角」只是**读时按
`path` 收窄**（§7.3）。因此 **S4 让子目录 RR 直接用「根 snapshot + path」即可**
——**不需要派生 id 层**，S2 省去。派生 id 的唯一好处（子目录 git commit hash 不
随根 rebase 变）被认为**不值得**（git 自身 rebase 也会改 hash）。

**S1 + S3 + S4 已让「子目录有独立 branch、看到父 branch 投影、能提交（lift）、
能开/合并 scoped RR」全部成立。**
