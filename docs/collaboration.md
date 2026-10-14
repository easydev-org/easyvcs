# Revision Requests (RR) — 原生协作模型

本文定义 EasyVCS 的协作单元：**Revision Request（RR）**。它不是 git 的
merge request，而是把「一次提议」表达成**钉住的、内容寻址的快照**。

## 1. 核心命题

**RR 的主体是 snapshot，不是 branch。**

| 维度 | git MR / Forgejo PR | EasyVCS RR |
|---|---|---|
| 评审对象 | 一个可变的 branch 指针 | **pinned snapshot（内容寻址、不可变）** |
| head 移动后 | diff 漂移，需靠 mergeable 重算 | **diff 永不漂**；只把 RR 标 `stale` |
| 更新内容 | 隐式（push 到 branch） | **显式**：`rr_updates` 追加一条记录 |
| 冲突 | 工作树状态 / 文本 marker | **一等 `object.Conflict` 对象** |
| 合并 | 移动指针 / 多父合并 | **纯函数**：pinned snapshots + 当前 target tip |

`source_snapshot` 与 `base_snapshot` 在创建时确定，**永不改变**。这是
「不跟着 branch 更新而更新」的落点。

## 2. 数据模型（`store`）

```
revision_requests(
  id PK, repo_id, iid(每 repo 单调),
  source_snapshot      -- ★ 钉住的 sha，永不改
  base_snapshot        -- ★ 创建时 target tip 的 sha，永不改
  target_branch        -- 落地到哪个 ref
  state                -- open | stale | conflicted | merged | closed
  title, body, author_id, created, updated
)
rr_updates(rr_id, seq, source_snapshot, base_snapshot, author_id, created)
rr_merge(rr_id, merged_snapshot, merged_revision_id, state, merged_by)
rr_reviews(rr_id, reviewer_id, state, body, created)
rr_comments(rr_id, author_id, body, path, created)
```

评审与评论存于 **`rr_reviews` / `rr_comments`**（外键 `rr_id`，与 `revision_requests.id`
对应）。它们**只追加、不覆盖**；`GET /api/v1/namespaces/{ns}/rr/{iid}` 会连同详情一起
返回 `reviews` / `comments`（列表端点不带，避免 N+1）。

## 3. source 只有一种：pinned snapshot

`rr.Source` 只有一个字段 `Snapshot`，就是一个**钉住的、内容寻址的快照**。没有
source 形态（kind）、没有 source revision 身份、没有 fork：

- **从本地目录提**：CLI 把目录 `BuildTreeFromFS` 成 tree，`Commit` 成一个快照，
  先把该快照的 bundle 上传（`/rr/objects`，propose 级），然后建 RR。
- **从某个 branch/revision 提**：客户端**自己**先解析出该 branch 的 tip 快照
  （`GET /api/v1/namespaces/{ns}/branches` 已返回 tip 的 snapshot sha），再按
  上面的快照路径提。服务端只认快照。

因为快照不可变，**评审对象按构造永不漂**；不存在「source 侧漂移」。

## 4. merge 是纯函数

```
base   = rr.base_snapshot            （创建时 target tip）
ours   = rr.source_snapshot
theirs = 当前 target tip 的快照
merged, conflicts = ws.Merge(base.tree, ours.tree, theirs.tree)
- **无冲突** → 在 target 上 `Commit` **一个新 revision**（单父 = 当前 target tip
  快照），tree = merged；target ref 指向它；记 `rr_merge`。**每次合并都产生一个
  新 revision**（没有 fast-forward 特例：source 永远是裸快照），目标历史保持线性，
  **source 快照不动**。
- **有冲突** → `state = conflicted`，冲突作为**一等对象**留在合并结果里，
  不回退；评审者 resolve 后重试。

`ApplyMerge` 全程在 **每 repo 锁 + 单事务** 内执行（与 `handlePush` 同款），
消除 TOCTOU。

### 落地提交的署名

落地提交（target 上那个新 revision）的 **author = 源快照的作者**（即**谁写的**这
个改动），因此 blame 把合并进来的行归给**作者**，而不是「谁点了合并」——这才是
「追责到作者」。源快照没有作者时，回退为**合并者**。

**「谁合并的」另行记录**在 `rr_merge.merged_by`（user id；开放实例为 null），并在
RR 详情 / merge 响应里以 `merged_by` 透出，信息不丢。

（这与 git 一致：author 是写提交的人；触发/落地是另一维度。）

## 5. stale 与显式 update

- `IsStale(rr)`：只判断 **target tip ≠ pinned `base_snapshot`**（目标分支动过）。
  源是 pinned snapshot，按构造不可变，所以没有「source 侧漂移」。合并始终用
  **当前** target tip 做 3-way，因此 stale 只是 UI 提示。
- `Update(rr, newSourceSnapshot)`：显式重新钉，追加一条 `rr_updates`，
  state 回到 `open`。更新是一次**可审计的显式动作**。

## 6. 权限

沿用 `store/rbac.go` 的判定：

- **propose**（developer+）：创建 / update / review / comment / resolve。
- **merge**（owner / maintainer）：`merge`。

## 7. GC 根集合

`revision/gc.go` 原本只按「revision 的当前 snapshot」算可达。RR 的
`source_snapshot` / `base_snapshot` 可能**没有任何 branch 指向**（尤其是
「从本地目录直接提」），因此 **open / stale / conflicted 的 RR 的 pinned
snapshot 及其整棵树必须是 GC 根**，否则会误回收。见
`store.ListOpenRRSnapshots` 与 `revision/gc.go`。

## 8. 它如何替代 gateway 的 hack

gateway（`coding-workspace/workspace-gateway`）在 Forgejo + git 上模拟
「服务端无工作副本、会话化」模型，为此堆了：

| gateway hack | RR / easyvcs 原生替代 |
|---|---|
| `ABCP_XXX` 占位提交当暂存区（`gitcommit.go`） | 无工作副本，revision 可自由 amend |
| MR 只合并占位提交的父 + 强推剥离（`service.go` `MergeMR`） | merge 是 pinned snapshot 的纯函数，source 分支不动 |
| `main` 分支保护锁死唯一写入口（`forgejo.go` `ProtectMain`） | server 侧 ref 权限：只有 merge 能推进 target |
| 冲突以文本哨兵块写进文件内容（`gitmerge/marker.go`） | `object.Conflict` 一等对象 + `Resolve` |
| 网关自写 diff3（`gitmerge/merge3.go`） | `merge.Trees` |
| 网关自写 compare/diff（Forgejo patch 为空） | `revision.Diff` / `DiffContent` |
| MR 跟着 branch 漂 | `source_snapshot` pinned + `stale` + 显式 `rr_updates` |

## 9. 端点

```
POST /api/v1/namespaces/{ns}/rr                 创建（source_snapshot）
POST /api/v1/namespaces/{ns}/rr/objects         上传源快照对象（bundle，不含 refs）
GET  /api/v1/namespaces/{ns}/rr?state=open      列表
GET  /api/v1/namespaces/{ns}/rr/{iid}           详情
GET  /api/v1/namespaces/{ns}/rr/{iid}/diff      基于 pinned shas 的 diff
POST /api/v1/namespaces/{ns}/rr/{iid}/update    显式重新钉
POST /api/v1/namespaces/{ns}/rr/{iid}/review    approve / request_changes / comment
POST /api/v1/namespaces/{ns}/rr/{iid}/comment
POST /api/v1/namespaces/{ns}/rr/{iid}/resolve
POST /api/v1/namespaces/{ns}/rr/{iid}/merge
```

## 10. CLI

```
easyvcs request create --from <dir> --into <ns/name>@<branch> --title <t> [--body <b>] [--remote origin]
easyvcs request list [state] [--remote origin]
easyvcs request show <iid>
easyvcs request diff <iid>
easyvcs request update <iid> <snapshot-sha>
easyvcs request merge <iid>
```

`create --from <dir>` 是「从本地文件直接提」的主路径：目录 → tree → snapshot
→ push → RR，无需 branch 或 fork。
