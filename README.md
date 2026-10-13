# easyvcs

开发规范见 [DEVELOP.md](DEVELOP.md)。

A Change-Native version control engine, extracted from EasyLab as a standalone,
dependency-light Go module.

## Concept

`Change` (a logical modification) is the first-class entity with a stable id
(`change_id` / `revision_id`) that **never** changes, no matter how often it is
rebased, squashed, or amended. The content version (`Snapshot`) is
content-addressed and mutable via rewrite. Refs are branches (mutable)
and tags (immutable).

- `Snapshots` are content-addressed graph nodes (`sha` derived from parents +
  tree + description + author). Rebasing changes the `sha` but never the
  `change_id`.
- `Tree` / `blob` / `conflict` (first-class conflict objects) make up the object
  model, with 3-way tree merges.
- Storage is a single central SQL database (default `~/.easyvcs/easyvcs.db`).
  `store.OpenDriver` selects the dialect: SQLite (default, pure Go), Postgres,
  or MySQL/MariaDB, all over one GORM-backed `RepoStore` interface. Object
  content (tree/blob/conflict) lives in the DB too (no `.git` object store).

## Horizontal scaling (HA) on Postgres

SQLite is a single-file DB: **run exactly one server instance** against it. To
run **multiple replicas**, use Postgres — the server then coordinates across
replicas with Postgres **advisory locks**:

```sh
EASYVCS_DB_DRIVER=postgres \
EASYVCS_DB_DSN='postgresql://user:pass@host:5432/easyvcs' \
EASYVCS_ADMIN_TOKEN=... EASYVCS_SECRET_KEY=... \
  easyvcs-server -addr :8996
```

What is made replica-safe on Postgres (and remains in-process on SQLite):

- **Per-repository write lock** — `withRepoLock` (push / RR merge / commit /
  branch-sync) takes a Postgres **session advisory lock** keyed by repo id, held
  on a dedicated connection, so two replicas serialise writes to the *same*
  repository while unrelated repositories proceed in parallel. A crashed holder's
  lock is released automatically when its connection drops.
- **Schema migration** — a **migration advisory lock** serialises `AutoMigrate`
  across replicas, so several replicas booting at once against a fresh DB do not
  race their DDL (which otherwise fails with `duplicate key value violates unique
  constraint pg_class_relname_nsp_index`).
- **Push-mirror scheduler** — **leader-elected**: only one replica runs it, so a
  mirror is pushed exactly once. Control it with `EASYVCS_MIRROR_SCHEDULER`:
  - `leader` (default on Postgres) — only the leader schedules; a standby takes
    over if the leader dies.
  - `always` (default on SQLite) — every instance schedules (single-instance).
  - `off` — nobody schedules.

Operational env: `EASYVCS_MIRROR_INTERVAL` (seconds; unset 5m, `<=0` disables),
`EASYVCS_MIRROR_TIMEOUT` (seconds; unset 2m).

Notes / limits:

- The in-process locks are **per-repository**; a repository's history is still
  linear (one writer at a time), which is the intended VCS semantics.
- `EASYVCS_SECRET_KEY` must be **the same** on every replica (it decrypts stored
  mirror tokens); a differing key makes those rows unreadable.
- MySQL/MariaDB is supported for the store but has **no cross-replica lock wiring
  yet** — run it single-instance (as with SQLite).

## Packages

- `object` — content-addressed primitives (`blob`, `tree`, `conflict`) and ids
- `store` — the persistence boundary (`RepoStore` interface over GORM;
  switchable sqlite/postgres/mysql, plus `CentralStore`/`Repo`). Object content
  lives in the DB (no .git object storage).
- `revision` — the semantic layer (commit / amend / rebase / squash / merge /
  resolve / drop / revert / diff), stable-id rules live here
- `merge` — 3-way tree merge producing first-class conflicts (path-aware;
  see `docs/conflict-model.md`)
- `transfer` — serialization / bundling (codecs, deltas)
- `gitbridge` — Git interoperability (go-git; exports/imports revisions as Git
  commits, no .git storage on the easyvcs side). `ImportBranch` takes a
  `Depth` (0 = full history, 1 = tip-only snapshot) and is single-branch.
- `server` — the authoritative server library (`server.New`/`Handler`): the
  smart protocol (advertise/fetch/push), the hosting REST surface (identity,
  orgs/repos, refs, browse) and Revision Requests, all under `/api/v1`
  (see `docs/api.md`); reusable by downstream
- `rr` — Revision Requests (the collaboration unit; see `docs/collaboration.md`)
- `mirror` — push/pull mirroring to/from an external git server, **go-git only
  (no `git` binary)**: push force-exports a branch's tree; pull reads an
  external branch (`Depth` 0 = full, 1 = snapshot).
- `gitserver` — exposes a repository over the git **smart HTTP** protocol
  (**read-only**: clone/fetch via `git-upload-pack`), so a standard `git clone`
  and package managers that treat a git repo as a package (go modules with
  `GOPROXY=direct`, npm/pip/cargo/composer git deps) can use easyvcs directly.
  Objects are built on the fly into memory (easyvcs still stores no `.git`).
- `encoding` / `ignore` — supporting layers

## CLI

```
easyvcs init [DIR]
easyvcs commit [--new] [DIR]
easyvcs amend [DIR]
easyvcs log / show / diff / checkout
easyvcs rebase <revision> --onto <sha>
easyvcs branch <name> <revision>     (branch; derives an independent revision)
easyvcs tag <name> <revision>        (immutable)
easyvcs refs / rev <expr>
easyvcs resolve / squash / message
easyvcs export / import
easyvcs remote add/remove; fetch / pull / push <remote>
easyvcs request create --from <dir> --into <ns/name>@<branch> --title <t>
easyvcs request list / show / diff / update / merge <iid>
easyvcs git-pull <git-url> [branch] [--token][--ssh-key][--passphrase]
easyvcs git-push <git-url> [branch] [--token][--ssh-key][--passphrase]
```

Server endpoints (all under `/api/v1`) are documented in `docs/api.md`.

### Clone a repository with plain git

```
git clone http://<user>:<token>@<server>/api/v1/repos/{ns}/{name}
```

Read-only (`git-upload-pack`); there is no `git push` (writes go through the
easyvcs API / Revision Requests). Because the clone URL is a normal git remote,
a repository can be consumed as a **package**: e.g. a Go module
(`GOPROXY=direct`), `npm i git+http://<user>:<token>@<server>/api/v1/repos/{ns}/{name}#<tag>`,
`pip install git+http://...`, Cargo `git = "..."`, Composer, etc.

**Monorepo: every subdirectory is its own git endpoint.** Append the subdir to
the clone URL and that subtree is served as an independent repository (its own
history = the commits that touch that path), so each subdir can be a package:

```
git clone http://<user>:<token>@<server>/api/v1/repos/{ns}/{name}/<subdir>
npm i git+http://<user>:<token>@<server>/api/v1/repos/{ns}/{name}/<subdir>.git
```

## Build

```
CGO_ENABLED=0 go build -o easyvcs ./cmd/easyvcs
CGO_ENABLED=0 go build -o easyvcs-server ./cmd/server
```

## Build & deploy the server image

`Dockerfile` builds `easyvcs-server` (a single Go module, `CGO_ENABLED=0`, SQLite
by default). `build-image.sh` builds it against the cluster buildkitd and pushes
it to the deployment registry — no local Docker daemon needed:

```
REGISTRY=artifact.worker.svc.cluster.local NAMESPACE=easy-vcs NAME=easyvcs TAG=v1 \
  ./build-image.sh
# -> artifact.worker.svc.cluster.local/easy-vcs/easyvcs:v1
```

Deployment lives in the org's deployment repo, **`easy-vcs/deploy`** (the
`easyvcs-platform` Helm chart): it deploys `easyvcs-server` together with
`artifact`, `easyops`, `agent-gateway` and `agent-webui`. Point `agent-gateway`
at this Service via
`EASYVCS_URL=http://easyvcs-server.<namespace>.svc.cluster.local:80`.

## Propose without a branch (`POST /snapshot`)

A Revision Request's source is a **snapshot**, not a branch, so a change does
not need a branch to be proposed. `POST /api/v1/repos/{ns}/{name}/snapshot`
builds a revision + snapshot from a **file-op set** and **moves no ref**
(`requirePropose`; developer and above, so owner/maintainer are included too):

```jsonc
{ "message": "propose from sandbox",
  "author": {"name":"alice","email":"a@x","session":"org:repo:branch"}, // optional
  "parents": ["<base snapshot sha>"],   // the tree baseline; omit = empty tree
  "files": [ {"path":"a.txt","operation":"create","content":"…"},
             {"path":"b.bin","operation":"update","content_bytes":"<base64>"},
             {"path":"c.txt","operation":"delete"} ] }
// -> {"ok":true,"revision_id":"…","snapshot":"…"}
```

Then open the request with `POST /api/v1/repos/{ns}/{name}/rr
{"source_snapshot":"<snapshot>","target_branch":"main"}`. **Set `parents` to the
snapshot the working copy was based on** (and send the full create/update/delete
diff): with an omitted `parents` (the empty tree) a merge would treat every file
you did not list as deleted.

The server's config env (`EASYVCS_DB_DRIVER`/`EASYVCS_DB_DSN`, `EASYVCS_HOME`,
`EASYVCS_ADMIN_TOKEN`, `EASYVCS_SECRET_KEY`, …) is documented in
`cmd/server/main.go`; the endpoint contract is in `docs/api.md`.

## Module

`github.com/easylab-platform/easyvcs`. It is consumed as a library by
`github.com/easylab-platform/easylab` (the server, agent bridge, and ops
platform). Licensed under MIT (see LICENSE).
