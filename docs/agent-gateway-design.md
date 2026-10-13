# agent-gateway — 设计（替代 workspace-gateway）

状态：定稿（维护者起草）。目标仓库：`easy-vcs/agent-gateway`。

## 0. 背景与边界

当前产品三层：

```
webui / workspace-extension ──workspace.v1──▶ workspace-gateway ──┬─▶ Forgejo   (VCS)
                                                                   ├─▶ agent     (会话/LLM)
                                                                   ├─▶ k8s       (沙箱/服务/PVC)
                                                                   └─▶ buildkit  (镜像构建)
```

替换后：

```
webui / workspace-extension ──workspace.v1──▶ agent-gateway ──┬─▶ easyvcs   (VCS，替代 Forgejo)
                                                               ├─▶ artifact  (OCI/包，替代 Forgejo registry)
                                                               ├─▶ agent
                                                               ├─▶ k8s
                                                               └─▶ buildkit
```

**关键边界**：`easyvcs` 替代的是 **Forgejo 这一个组件**，不是 workspace-gateway。
`agent-gateway` 是 workspace-gateway 的**继任者**：保留会话/沙箱/服务/构建/agent 转发，
只把 **VCS 后端**换成 easyvcs、**镜像/包后端**换成 artifact。
`easylab` 不再使用（仅作迁移参考）。

## 1. 职责划分

### 1.1 委托给 easyvcs（`internal/easyvcs` 客户端）

| workspace-gateway 现在（forgejo） | → easyvcs 端点 |
|---|---|
| `EnsureOrg` / `CreateOrg` | `POST /api/v1/orgs` |
| `EnsureRepo` / `CreateEmptyRepo` / `GetRepo` / `RepoExists` | `POST/GET /api/v1/repos/{ns}/{name}` |
| `DeleteRepo` | `DELETE /api/v1/repos/{ns}/{name}` |
| `Tree` | `GET .../tree?ref=&path=` |
| `ReadBlob` | `GET .../blob?ref=&path=` |
| `RawFile` / `RawContents` | `GET .../raw?ref=&path=` |
| `Log` | `GET .../log?ref=&path=&limit=&offset=` |
| `GetCommit` | `GET .../commit/{sha}` |
| `CommitDiff` | `GET .../diff?base=&head=`（或 `.../commit/{sha}` 的 diff） |
| `Compare` / `CompareFiles` | `GET .../diff?base=&head=` |
| `Blame` | `GET .../blame?ref=&path=`（含 `session`） |
| `Branches` / `BranchTip` / `BranchExists` | `GET .../branches` |
| `CreateBranch` / `DeleteBranch` | `POST/DELETE .../branches` |
| `Tags` / `CreateTagAt` | `GET/POST .../tags` |
| `ArchiveTarGz` | `GET .../archive/{ref}.tar.gz` |
| `CreateMR` / `GetMR` / `ListMRs` / `MRDiff` / `MRComments` / `CommentMR` / `MergeMR` | `POST/GET .../rr`、`.../rr/{iid}`、`.../rr/{iid}/diff`、`.../rr/{iid}/comment`、`.../rr/{iid}/merge` |
| `ProtectMain` | `GET/PUT .../protected-branches` |
| `CommitFiles` | `POST .../commit`（一次提交） |
| `ApplyFiles`（staging amend） | `POST .../commit`（带 `revision_id`，**amend**） |
| `CommitStaged`（reword 定稿） | `POST .../commit`（带 `revision_id`、空 `files`、新 `message`） |
| `SyncBranch`（`gitmerge`） | `POST .../branches/{branch}/sync` |
| `ImportRepo`（`gitimport`） | `POST .../import` |
| `Set/List/DeletePushMirror` | `POST/GET/DELETE .../push-mirrors*` |

### 1.2 委托给 artifact

| 现在 | → artifact |
|---|---|
| `ListContainerPackages`（镜像目录浏览） | artifact 的 catalog/list API |
| `oci-import`（镜像 mirror） | 无显式 mirror API：**agent-gateway 用 buildctl 重推**（`FROM <source>`），目标写 artifact registry（pull-through 缓存不覆盖此用途） |
| 镜像**推送目标**（buildkit 推 registry） | artifact registry（`<artifact-host>/<org>/<name>:<tag>`） |

**镜像构建编排留在 agent-gateway**（`internal/imagebuild`：拉 easyvcs `/archive` → buildkit 构建 → 推 artifact）。

### 1.3 保留在 agent-gateway（原样或小改）

| 模块 | 职责 | 处置 |
|---|---|---|
| `internal/workspacesvc` | workspace.v1 服务面 | **保留**，git 调用点换 easyvcs |
| `internal/roles` | 会话角色 + preset + 工具白名单 | **原样** |
| `internal/presets` | preset 定义 | **原样** |
| `internal/agentclient` + `forward.go` | 转发 agent.v1（chat/models/config/providers/files） | **原样** |
| `internal/sandboxmgr` / `sandboxreaper` | 沙箱生命周期 + 空闲回收 | **原样** |
| `internal/servicesmgr` | 长驻服务（Deployment/Service）+ PVC | **原样** |
| `internal/imagebuild` | buildkit 构建 | 推送目标改 artifact |
| `internal/idlewatch` | 空闲会话重触发 | **原样** |
| `internal/provision` | provider 注册 + extension config 校准 | **原样** |
| `internal/runtimeprofiles` | KVM/GPU | **原样** |
| `k8swatch` / `workspaceHub` | WatchWorkspace 流 | **原样** |
| `internal/members` | 租户所有权表 | **大幅裁剪**：删 org/repo 所有权（见 §2），只留 free_sessions |
| `internal/forgejo` | Forgejo 客户端 | **删除** → `internal/easyvcs` |
| `internal/gitcommit` | clone/amend/force-push + `ABCP_XXX` 占位提交 | **删除**（easyvcs `/commit` amend 取代） |
| `internal/gitimport` | 外部 git 导入（go-git） | **删除**（easyvcs `/import` 取代） |
| `internal/gitmerge` | 分支 3-way 合并 + marker | **删除**（easyvcs `/branches/{b}/sync` 取代） |

删除 `gitcommit`/`gitimport`/`gitmerge` 后，**go-git 依赖与「占位提交/force-push」hack 一并消失**。

## 2. 身份与所有权：tenant ≡ easyvcs user

现在 gateway 有一张**平行**的 `members` 表（`org_owners`/`repo_owners`/`free_sessions`），与 Forgejo 各记一份所有权。

easyvcs 的模型是「**user 即所有权边界**」（namespace 有 owner，`repo_members` 给角色）。

**定案**：
- **每个租户对应一个 easyvcs user**；租户开通时创建该 user + 一个 token。
- **admin token 只用于「建用户 / 发 token」这一件事**：`POST /api/v1/users` 是 admin-only，响应带一次性明文 `token`（scope `write`）。
- **其余所有动作都用该租户的 token**（包括**建 org/repo**）：easyvcs 的 `POST /orgs`、`POST /repos/{ns}/{name}` **不是 admin 门**，而是「**调用者成为 owner**」（`EnsureNamespace(u.ID, org)` / `Create(RepoRef{Owner: u.ID, …})`），并拒绝「namespace 已被**别的** user 拥有」（409）。**用 admin token 代建会把 owner 记成 admin → 之后租户自己访问会 403**，所以建 org/repo **必须**用租户 token。
- 所有**租户域** VCS 调用都带该租户 token → 可见性由 easyvcs 的 `requireRepoAccess` 强制，agent-gateway 不再自己算。
- **删除** `members` 的 org/repo 所有权（不再两处真相）；`claimOrg`/`claimRepo` 的「他租户拥有则拒绝」由 easyvcs 的 namespace owner 唯一性保证。
- **保留** `free_sessions(tenant, session, role)` 于本地小 DB（会话概念，非 VCS 概念）。

**租户 → easyvcs 凭据映射（新增一张本地表）**：

```
tenant_vcs(tenant PK, vcs_user_id, vcs_username, vcs_token, created, updated)
```

- **与 `members` 同一个 sqlite 库**（不要新开库）。
- `vcs_token` **密文存储**：明文只在 `POST /users` 响应里出现一次；easyvcs **不提供**「再取回 token」，丢了只能重建 token 并更新此行。
- 懒创建：首次接触某 tenant 时用 `EASYVCS_ADMIN_TOKEN` 调 `POST /users`（username 建议稳定映射，如 `t-<tenant>`），拿到明文 token 后**立即**用它建 org/repo。

**合并权限**：easyvcs token 只有 `read|write` 两档；**merge 不看 scope，看 repo 角色**（`canMerge` → `RoleOfRef`），owner 天然可 merge。**「只有 maintainer 会话能 merge」由 agent-gateway 的会话角色（`roles` 白名单）把关**，不是 easyvcs token scope。

## 3. 写路径：`/commit` amend + branch_staging 表

workspace-gateway 的 staging 靠 `ABCP_XXX` 占位提交 + clone/amend/force-push。easyvcs 用 `/commit` 的 **amend**：

1. 首次写（`repo-file-write/edit/delete`、`sandbox-port`）→ `POST .../commit`（**无** `revision_id`）→ 得到一个 **staging revision_id**；
2. 后续写 → `POST .../commit`（**带同一** `revision_id`）→ **amend 累积**（同一 revision，树累积）；
3. `repo-commit` → `POST .../commit`（带 `revision_id`、**空 files**、正式 message）→ **reword 定稿**，关闭 staging 区；
4. `repo-mr-create` / `repo-mr-merge` 的 **staging gate**：分支仍有未定稿 staging → 拒绝（与现在 `stagingGate` 同义）；
5. `stripStagingPlaceholder` → **不再需要**（没有占位提交）。

**agent-gateway 需一张小表** `branch_staging(org, repo, branch, revision_id, updated)`，记录每个分支当前的 staging revision。这就是「staging 区」。

## 4. workspace.v1 面：保持 proto 兼容

- **所有消息/RPC 保留**（webui/extension 不改）。
- 实现从 Forgejo 换成 easyvcs；**新增可选字段**：`BlameLine.session`、`CommitInfo.session`（easyvcs 已提供）。
- releases 的两个 RPC（`ListReleases`/`GetReleaseAsset`）**保留**，但 easyvcs 无 release 模型 → 返回**空列表 / NotFound**（webui 显示空即可）。
- `Tree`/`ReadBlob`/`RawFile`/`Log`/`Branches`/`Tags`/`Compare`/`Blame`/`CommitDiff`/`GetMR`/`MRDiff`/`ListMRComments`/`Archive` 的**语义保持一致**（easyvcs 端点已对齐）。
- **MR ↔ RR 语义差异**（重要）：easyvcs 的 RR 是「pinned snapshot → 目标分支」，没有 git 双父 merge；`CreateMR` 的 `head`/`base` 映射为 RR 的源快照/目标分支；`MergeMR` 映射为 `POST .../rr/{iid}/merge`。**冲突在 RR 里解决**（`/rr/{iid}/resolve`），不再是文件 marker——但 `SyncBranch` 仍走 `/branches/{b}/sync`（其冲突为**一等对象**，读文件时渲染成 marker）。

## 5. 依赖契约

**easyvcs 必须提供**（现在 `main` 已全部具备）：
`/orgs`、`/repos/{ns}/{name}`（CRUD）、`/tree`、`/blob`、`/raw`、`/log`、`/commit/{sha}`、`/diff`、`/blame`、`/branches`（CRUD）、`/tags`、`/archive/{ref}.tar.gz`、`/commit`（新建 + **amend**）、`/rr/*`（全套）、`/protected-branches`、`/branches/{branch}/sync`、`/import`、`/push-mirrors*`、`/healthz`、`/readyz`。鉴权：token → user，`requireRepoAccess(read|write)`。

**artifact 必须提供**（权威对接契约见 `artifact/docs/agent-gateway-integration.md`）：
- **OCI registry**：`/v2/...`（pull/push）；客户端**显式指向 artifact**（registry 走 **Host 头**，故 `IMAGE_REGISTRY_HOST` 必须是 artifact 的 Service 名），artifact 自己 fetch 上游并缓存（**pull-through**）。`--self-base` 必须是外部可达 origin（`/token` realm 用）。
- **catalog（已可用，artifact MR #10）**：`GET /v2/_catalog`（**已剥离部署 host 前缀**，返回 `acme/app` 形式）；`GET /v2/_catalog?owner=<org>`、`?owner=<org>&name=<name>` → `{"images":[{owner,name,tags:[…]}]}`（**已过滤 `sha256:` 摘要项**；未知 name → **404**，未知 owner → `{"images":[]}`）；`GET /v2/<name>/tags/list`（**保持原样**，pull-through 镜像仍可能含 `sha256:<hex>` 摘要行，客户端需自行过滤）。**`_catalog` 的 `?n=`/`?last=` 分页仍未实现**（全量返回）。
- （**不需要**显式 mirror API）镜像复制由 agent-gateway 用 buildctl 重推（`FROM <source>` → push 到 artifact）；artifact 只做 registry。**pull-through 缓存 ≠ import**：缓存按「被请求的名字」落在**上游 host** 下，不会落到你指定的 `<org>/<name>`。

**鉴权决策（定案）：共享 token + 路径约定（方案 a）**。artifact **无 per-org 强制**：任一 write token 可推任意 `<org>`；`<org>` 只是**路径约定**。**pull（manifest/blob/tags-list）匿名公开**（沙箱/服务拉镜像无需凭据）。
- **⚠️ catalog 需凭据**：auth 开启时 `_catalog`（含 `?owner=`）**需要任意有效凭据**（read 或 write token），匿名 → **401**（`Bearer realm="<self-base>/token"`）。agent-gateway 调 catalog **必须带 artifact token**。（artifact MR #10 引入；pull 路径不受影响。）
- **为何 (a) 足够**：与 **Forgejo 时代一致**（旧 `workspace-gateway` 也是单一共享 token 做全部 registry 操作），**不是回退**；写入方只有 agent-gateway（token 由它持有、集群内），隔离在**有身份的那一点**（gateway 的 `ensureVisible`/`ownsOrg`）执行。
- **方案 (b) per-org 强制：暂缓（未实现）**。它要改 artifact 的 `core`+`oci`+`cmd`（`Ownership` 接进 server + OCI 按 `(oci, namespace=<org>)` 鉴权 + catalog 按调用者过滤），且需要「**tenant↔token 映射**」这一 artifact **并不掌握**的身份源。若将来要做，映射应来自 **easyvcs（身份源，`tenant ≡ user`）**，不在 artifact 里另造 tenant 概念。

**agent 必须提供**：`agent.v1`（CreateSession/Fork/DeleteSession、GetIdentity、ListSessions、Prompt/WatchSession、providers、config、files）。

## 6. 部署

```yaml
# agent-gateway（沿用 workspace-gateway 形态）
env:
  # 删除
  # FORGEJO_URL / FORGEJO_TOKEN / FORGEJO_USER / FORGEJO_PASSWORD
  # 新增
  EASYVCS_URL:         http://easyvcs-server.<ns>.svc.cluster.local:8996
  EASYVCS_ADMIN_TOKEN: <secret>          # 开通租户（建 user/token）用
  ARTIFACT_URL:        http://artifact.<ns>.svc.cluster.local:80
  IMAGE_REGISTRY_HOST: <artifact host>   # 取代旧的 registry host
  # 不变
  AGENT_URL / AGENT_ADMIN_TOKEN / GATEWAY_SERVICE_TOKEN / GATEWAY_SERVICE_TENANT
  SANDBOX_NAMESPACE / DEFAULT_SANDBOX_IMAGE / SANDBOX_ORG / TOOLCHAIN_ORG
  BUILDKIT_ADDR / NATS_URL / WORKSPACE_RUNTIME
  SERVICE_PREVIEW_TTL / SANDBOX_IDLE_TTL / PVC_STORAGE_CLASS / …
```

另需部署：**easyvcs-server**（Deployment + PVC 或 Postgres）、**artifact**（Deployment + PVC）。

### artifact 的 egress：**无需 sidecar**

artifact 已改为**显式寻址**（客户端直接指向 artifact：非 OCI 在 `/artifacts/<proto>/...`，OCI 在 `/v2`），
**不经过 easysidecar**、不需要 MITM CA / DNS 覆盖 / `NET_ADMIN` / init container。
（`artifact/ARCHITECTURE.md` 里 `targets.EgressPolicy()` 与 `client → easysidecar` 的图示曾是**过时文字**，代码已在 `22027cb2`/`bb784872` 删除；artifact 侧的 `ARCHITECTURE.md` 也已更正（artifact MR #8）。）

artifact 的出网只在其 Deployment 上配：
- 走上游 HTTP 代理：`HTTP_PROXY`/`HTTPS_PROXY`（如 `http://mihomo.<ns>.svc.cluster.local:7890`）+ `NO_PROXY`（含 `.svc`、`.svc.cluster.local`）；
- 直连：两个 env 都不设；
- 纯本地/无上游：`--air-gap`。
- SSRF guard = `targets.IsPublicHost`（**公网域名检查**：拒绝 cluster-local / 裸地址 / IP），在 artifact 内部执行；作用于路径里**显式写出**的 host 段（netcache / git / ivy）。

agent-gateway 侧只需 `ARTIFACT_URL=http://artifact.<ns>.svc.cluster.local:80` 与 `IMAGE_REGISTRY_HOST=<artifact host>`。

`easysidecar` 的独立角色是**工作负载侧 egress 策略**（sandbox / job / service Pod 的 per-Pod block/direct/rewrite + 审计），
与 artifact 无关、按需启用；详见 `easy-vcs/easysidecar` 的 `docs/artifact-egress.md`。

## 7. 迁移步骤（建议顺序）

1. 复制 `agent-gateway` 仓（从 workspace-gateway）并改 module path / 镜像路径 / 部署引用。
2. 新增 `internal/easyvcs`（typed client，照搬 forgejo 的 `ErrNotFound`/`ErrConflict`/分页结构，换 URL/payload）。
3. 改 `workspacesvc` 的 git 调用点 → easyvcs；`members` 裁到只剩 free_sessions；引入 tenant→easyvcs-user/token 映射。
4. 写路径：`ApplyFiles`/`CommitStaged`/`BranchStatus` → `/commit` amend + `branch_staging` 表；删 `gitcommit`。
5. `SyncBranch` → `/branches/{b}/sync`；`ImportRepo` → `/import`；删 `gitmerge`/`gitimport`。
6. `imagebuild` 推送目标 → artifact；`ListOCIImages`/`oci-import` → artifact API。
7. proto：加 `session` 字段；releases RPC 返回空。
8. 部署切换：env 改指向 easyvcs-server + artifact。

## 8. 验收

- workspace.v1 对 webui/extension **行为兼容**（同一套 RPC，语义对齐）。
- 端到端：建租户（easyvcs user）→ 建 org/repo → 分支会话 → 沙箱 checkout（`/archive`）→ 写文件（`/commit` amend 累积）→ `repo-commit` 定稿 → 开 RR → 合并 → blame 显示作者/session。
- 无 go-git 依赖；无占位提交；无 force-push。
