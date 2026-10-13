# EasyVCS HTTP API (`/api/v1`)

This is the single contract surface for clients (the webui, agents, and the
CLI). Every route lives under the `/api/v1` prefix. There is no separate merge
request surface: collaboration is expressed as **Revision Requests** (RR), whose
subject is a pinned, content-addressed snapshot.

## Auth

- A **bearer token** resolves to a user via `store.LookupToken` (tokens are
  stored as SHA-256; the plaintext is shown once at creation).
  `Authorization: Bearer <token>`.
- **Admin** endpoints (user administration) additionally require the static
  `EASYVCS_ADMIN_TOKEN`; when it is unset the admin endpoints return **503**.
- When no users exist the instance is **open**: anonymous read/write is allowed
  (dev convenience). Once the first user exists, writes require a token.
- Repository access is role-based: `owner` / `maintainer` / `developer` (see
  `store.RoleOf`). Reads need `read`, requests need `propose` (developer+),
  merges need `merge` (owner/maintainer).

## Ref semantics

An empty `ref` means the repository's **default branch** (`RepoMeta.DefaultBranch`,
falling back to `main`). A non-empty `ref` may be a branch name, a tag name, a
revision id (or a unique prefix), or a snapshot hash.

## Authorship

Every snapshot carries an `Author {name, email, session}`. The author is part of
the snapshot's content hash, so it is fixed **at commit time** and cannot be
rewritten afterwards.

- The server authorizes the **action**, not the **content**: a token that may
  push to a repo/branch may push any snapshot, regardless of its claimed author
  (like git, which never validates the author of a pushed commit). The server
  therefore never rejects a push over its `author`.
- An **RR merge** is committed by the server and authored by the **merger** (the
  authenticated caller; `anonymous` on an open instance) — the server is the
  committer of that action, so it records who triggered it.
- The optional **`session`** field attributes a change to a session (e.g. a
  workspace session `org:repo:branch`). It is empty for callers that do not use
  sessions, and — like the author — is hashed only when non-empty, so a
  session-less snapshot's id is unchanged.

The CLI resolves the identity from `--author`/`--session` > `EASYVCS_AUTHOR_NAME`
/ `EASYVCS_AUTHOR_EMAIL` / `EASYVCS_SESSION` > `$EASYVCS_HOME/config` (key=value)
> the built-in `easyvcs` default.

---

## Health / readiness (unauthenticated)

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/v1/healthz` | none | liveness: 200 while the process is serving (never touches the store) |
| GET | `/api/v1/readyz` | none | readiness: 200 when the store is reachable, 503 otherwise |

## Identity / tokens

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/v1/whoami` | any | resolves the token to a user (`anonymous` when none) |
| GET | `/api/v1/users` | admin | list users |
| POST | `/api/v1/users` | admin | create a user; returns a one-time write token |
| PATCH | `/api/v1/users/{id}` | admin | update display name / disabled |
| DELETE | `/api/v1/users/{id}` | admin | delete a user |
| POST | `/api/v1/users/{id}/tokens` | admin | mint a NEW token for an **existing** user (`level: read\|write`, default write); returns plaintext once, does NOT revoke existing tokens. Use when `POST /users` returns 409 (user already exists) and its plaintext was lost. |
| GET | `/api/v1/tokens` | token | list the caller's tokens (no plaintext) |
| POST | `/api/v1/tokens` | token | create a token (`level: read\|write`); returns plaintext once |
| DELETE | `/api/v1/tokens/{id}` | token | delete one of the caller's tokens |

## Hosting (orgs / repos)

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/v1/repos` | any | repositories the caller can access (public + own/member); each carries `default_branch` + `private` inline. `?owned=true` lists ONLY the caller own/member repos (no other tenant public repos) |
| GET | `/api/v1/orgs` | any | namespaces the caller owns |
| POST | `/api/v1/orgs` | token | create a namespace (owned by the caller) |
| GET | `/api/v1/repos/{ns}/{name}` | read | metadata: description / visibility / default_branch / kind / empty. **404** when the repo does not exist; **403** only when it exists but the caller may not read it (so a client can distinguish missing from forbidden) |
| POST | `/api/v1/repos/{ns}/{name}` | token | create a repository (owner = caller). Body `{description?, visibility?, default_branch?, init?}`. **New repos default to `private`** (multi-tenant safety; pass `visibility:"public"` to publish). `init:true` seeds an initial empty commit on `main` (server-side bootstrap, bypassing branch protection) so branches can be created from `main` |
| PATCH | `/api/v1/repos/{ns}/{name}` | owner | update description / visibility / default_branch |
| DELETE | `/api/v1/repos/{ns}/{name}` | owner | delete a repository |

## Refs

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/v1/repos/{ns}/{name}/refs` | read | all branch + tag refs |
| GET | `/api/v1/repos/{ns}/{name}/branches` | read | branches (with tip sha) |
| POST | `/api/v1/repos/{ns}/{name}/branches` | write | `{name, from}`; derives a fresh revision |
| DELETE | `/api/v1/repos/{ns}/{name}/branches/{branch}` | write | delete a branch |
| GET | `/api/v1/repos/{ns}/{name}/tags` | read | tags |
| POST | `/api/v1/repos/{ns}/{name}/tags` | write | `{name, target}`; immutable (re-tag is 409) |
| POST | `/api/v1/repos/{ns}/{name}/branches/{branch}/sync` | write | integrate `base` (default `main`) into `{branch}`; `{base?}`; returns `{ok, branch, base, clean, conflicts[], revision_id, snapshot}` (+`up_to_date`). See "Branch sync" below |

## Branch sync

`POST /api/v1/repos/{ns}/{name}/branches/{branch}/sync` integrates the `base`
branch (default `main`) into a feature `branch`, so the branch catches up with
the base (git's *update branch / merge base into this branch*). Auth is
**write** (owner); a protected branch (e.g. `main`) cannot be the TARGET.

- **easyvcs-native, not a git two-parent merge**: history is linear
  single-parent, so a sync commits ONE new single-parent revision on the branch
  (parent = the branch tip) whose tree is the three-way merge of
  `(merge base, branch tip, base tip)`.
- **Conflicts are first-class objects** (`object.Conflict`) in that tree — never
  text markers. A client reading the file (`/raw`, `/blob`, archive) gets the
  marker text; resolve them and commit, or open an RR. `conflicts` lists the
  affected paths.
- The **base branch is never touched**. When the base tip is already an ancestor
  of the branch tip the call is a no-op: `{clean:true, up_to_date:true}` and no
  new revision.


## Browse

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/v1/repos/{ns}/{name}/tree?ref=&path=` | read | directory listing (name/kind/size/sha) |
| GET | `/api/v1/repos/{ns}/{name}/blob?ref=&path=` | read | text inline; binary is flagged (use `/raw`) |
| GET | `/api/v1/repos/{ns}/{name}/raw?ref=&path=` | read | raw bytes (any content type) |
| GET | `/api/v1/repos/{ns}/{name}/log?ref=&path=&limit=` | read | commit history (or a file's history); each entry has `author` + `session`. **`ref` empty = the whole repository's revisions; `ref` set (branch/tag/revision id) = only the revisions REACHABLE from it** (the branch's own history, along first-parent + fork edges); an unknown `ref` is 404 |
| GET | `/api/v1/repos/{ns}/{name}/commit/{sha}` | read | commit/snapshot metadata (`author` + `session`) |
| GET | `/api/v1/repos/{ns}/{name}/diff?base=&head=` | read | per-file diff between two snapshots (`binary` flag) |
| GET | `/api/v1/repos/{ns}/{name}/blame?ref=&path=` | read | line-level attribution (`author` + `session` per line) |
| GET | `/api/v1/repos/{ns}/{name}/archive/{ref}.tar.gz` | read | the tree at `ref` as a gzipped tar (streamed; sandbox checkout). The ref must be explicit (`main.tar.gz`); a non-`.tar.gz` path is 400, an unknown ref is 404. Optional `?path=<dir>` archives only that **subtree**, rooted AT the directory (its entries appear at the archive root) |

## git smart HTTP (read-only)

An easyvcs repository is also a **standard git remote** (read-only), so
`git clone`/`fetch` and package managers that treat a git repo as a package work
directly. Auth is the same bearer token (the git CLI sends it as Basic auth,
which the server accepts). There is **no `git push`** — writes go through the
easyvcs API / Revision Requests.

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/v1/repos/{ns}/{name}/info/refs?service=git-upload-pack` | read | smart-HTTP advertisement |
| POST | `/api/v1/repos/{ns}/{name}/git-upload-pack` | read | the packfile for clone/fetch |

```
git clone http://<user>:<token>@<server>/api/v1/repos/{ns}/{name}
```

**Monorepo subdirectories**: append a subdir path and that subtree is served as
its own repository (its history = the commits that touch it):

```
git clone http://<user>:<token>@<server>/api/v1/repos/{ns}/{name}/<subdir>
```

Go module vanity also works per subdir: `GET .../{ns}/{name}/<subdir>?go-get=1`
returns a `go-import` meta tag pointing at that subdir's git endpoint.

### Sub-repository (native)

Any subdirectory of a repository is itself a (recursive) repository: its
revisions are the commits that changed anything under it, and its refs are the
branches/tags whose tip contains it. This is backed by the `revision_path_dirs`
index (built at write time; lazily backfilled for older revisions), so it is
O(revisions under the path).

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/v1/repos/{ns}/{name}/subrepo?path=<dir>&limit=` | read | `{path, refs:[{kind,name,target,subtree}], commits:[{revision_id,sha,description,author,time,subtree}]}` |

## Push mirrors

A push mirror force-pushes ONE branch's tree to an external git destination
(`git push --force`, mirror semantics: the destination branch keeps exactly that
tree). A repository may have several. The mirror token is a secret: it is stored
encrypted and is **never** returned by any endpoint or written to a log. A
background scheduler (see `cmd/server`) periodically re-pushes every mirror;
`EASYVCS_MIRROR_INTERVAL` (seconds; unset = 5m, `<=0` disables) and
`EASYVCS_MIRROR_TIMEOUT` (seconds; unset = 2m) control it.

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/v1/repos/{ns}/{name}/push-mirrors` | read | list mirrors (token omitted) |
| POST | `/api/v1/repos/{ns}/{name}/push-mirrors` | owner | create/replace `{name, url, branch?, token?}`; `branch` defaults to `main` |
| DELETE | `/api/v1/repos/{ns}/{name}/push-mirrors/{name}` | owner | delete a mirror (404 when absent) |
| POST | `/api/v1/repos/{ns}/{name}/push-mirrors/{name}/sync` | owner | push now; on failure returns **502** `{ok:false,error}` and records `last_error` (the server stays up) |

## Import an external git repository

`POST /api/v1/repos/{ns}/{name}/import` clones an **external** git repository
(heads only — never `refs/pull/*`) and imports its history as easyvcs revisions,
landing the selected ref on the repository's `main`. It is the engine-native
replacement for the workspace gateway's `repo-import` tool, and it lives in the
engine because importing converts git commits into snapshot/revision objects
(which needs direct store access). It is the same family as push mirrors (git
interop). Unlike push mirrors, the import path uses go-git, so the server image
needs no `git` binary for it.

Auth is **owner**. The repository may be created by the import (`create` defaults
to true); the org must be owned by (or claimable by) the caller. Importing into a
repository that already has content on `main` is refused **409** (it never
overwrites). A source that cannot be reached/authenticated is **502** with a
redacted `detail` — the token and any URL userinfo are never returned or logged.

| Method | Path | Auth | Notes |
|---|---|---|---|
| POST | `/api/v1/repos/{ns}/{name}/import` | owner | `{url, branch?, depth?, token?, ssh_key?, ssh_key_passphrase?, description?, create?}`; `branch` defaults to `main` (the source ref name is irrelevant — it lands on `main`). `depth` (default `0` = full history; `1` = tip only, i.e. a single-revision **snapshot**; `N` = last N commits) — executed by go-git, no `git` binary. Returns `{ok, org, repo, branch:"main", revisions}`. Bounded by `EASYVCS_IMPORT_TIMEOUT` (seconds; unset = 15m). |

## Commit (working-copy-free write path)

`POST /api/v1/repos/{ns}/{name}/commit` applies a set of file operations to a
**non-default** branch in one revision — no server-side working copy, no staging
area. It is the easyvcs-native replacement for a git clone/amend/force-push flow
(and for a workspace gateway's "staging placeholder" hack): N file ops become
exactly ONE commit. Auth is **write** (owner).

Body:

```json
{ "branch": "feature/x", "message": "add files", "revision_id": "<optional>",
  "author": {"name":"alice","email":"a@x","session":"org:repo:feature/x"},
  "parents": ["<optional snapshot sha>"],
  "files": [
    {"path":"a.txt","operation":"create","content":"..."},
    {"path":"b.bin","operation":"update","content_bytes":"<base64>"},
    {"path":"c.txt","operation":"delete"}
  ] }
```

- `branch` is **required** and must **not** be a protected branch: a commit to a
  protected branch (e.g. `main`) is refused **403** (`protected-branch`) — the
  default branch changes only through a merged Revision Request.
- **Fresh commit**: no `revision_id` → a NEW revision with `parents` = the branch
  tip (or a root revision when the branch does not exist).
- **Amend**: with `revision_id` → that revision is amended in place (same stable
  id, replaced snapshot), preserving previously accumulated edits. This is how a
  staging writer accumulates several writes and then finalises them (a **reword**:
  `revision_id` given + empty `files` + a new `message`) in a single commit.
- `parents` (optional) is an optimistic-concurrency guard: when the branch exists
  and its tip no longer matches the pinned parent, the commit is refused **409**
  (`stale`).
- each file op is `create`/`update`/`delete` (empty = update); `content_bytes`
  (base64) is binary-safe and wins over `content`; `delete` ignores content.
  A path that is absolute, contains `..`/`.` components, or uses backslashes is
  rejected **400** (it can never escape the tree root).
- empty `files` with no `revision_id` is **400**.
- `author` defaults to the authenticated caller. The server authorizes the
  **action**, not the content, so it never rejects a request over its author.
- returns `{ok, branch, revision_id, snapshot}`.

## Snapshot (propose without a branch)

`POST /api/v1/repos/{ns}/{name}/snapshot` applies a set of file operations and
returns a new **revision + snapshot** that **no ref points at** — it moves no
branch or tag. It is the "propose from a sandbox" primitive: the RR source is a
**snapshot**, not a branch, so a client can upload the files it changed and hand
the returned snapshot straight to `POST /rr`:

```
sandbox dir -> POST /snapshot (get a snapshot) -> POST /rr {source_snapshot}
```

Auth is **propose** (developer and above), matching `/rr` and `/rr/objects` —
**not** the owner-only `/commit`; owners and maintainers are ≥ developer, so they
may use it too.

Body:

```json
{ "message": "propose from sandbox",
  "author": {"name":"alice","email":"a@x","session":"org:repo:branch"},
  "parents": ["<optional snapshot sha: the tree baseline>"],
  "files": [
    {"path":"a.txt","operation":"create","content":"..."},
    {"path":"b.bin","operation":"update","content_bytes":"<base64>"},
    {"path":"c.txt","operation":"delete"}
  ] }
```

- **No branch is involved**: there is no `branch` argument and no ref is moved,
  so no branch-protection check and no per-repo lock are needed.
- `parents` (optional) is the base snapshot the changes are applied on top of;
  absent = the empty tree (a root snapshot of brand-new content).
- file ops, `content_bytes`, path safety and `author` behave exactly as `/commit`.
- empty `files` is **400**.
- returns `{ok, revision_id, snapshot}`.

## Branch protection

A repository may mark one or more branches as **protected**: a protected branch
may only change through a **merged Revision Request**. A direct push that would
move a protected branch is refused (403, `error: protected-branch`); a no-op push
(target unchanged) is allowed. New repositories protect `main` by default.

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/v1/repos/{ns}/{name}/protected-branches` | read | the protected set |
| PUT | `/api/v1/repos/{ns}/{name}/protected-branches` | owner | `{protected_branches: [...]}`; empty clears protection |

## Revision Requests (collaboration)

An RR pins a snapshot (`source_snapshot`) plus the target tip it was opened
against (`base_snapshot`); both are immutable, so the review never drifts when a
branch moves. The source is **always a pinned snapshot**: to propose a branch's
tip, the client resolves that branch to its snapshot sha first
(`GET .../branches`) and opens the RR on that snapshot. There is no source kind,
no source revision, and no fork.

| Method | Path | Auth | Notes |
|---|---|---|---|
| POST | `/api/v1/repos/{ns}/{name}/rr` | propose | create; `{source_snapshot, target_branch, title, body}` |
| POST | `/api/v1/repos/{ns}/{name}/rr/objects` | propose | upload the source snapshot's objects (bundle, **no refs**) |
| GET | `/api/v1/repos/{ns}/{name}/rr?state=` | read | list requests |
| GET | `/api/v1/repos/{ns}/{name}/rr/{iid}` | read | one request; includes `reviews` + `comments` |
| GET | `/api/v1/repos/{ns}/{name}/rr/{iid}/diff` | read | pinned diff (never drifts) |
| POST | `/api/v1/repos/{ns}/{name}/rr/{iid}/update` | propose | explicit re-pin (`{source_snapshot}`) |
| POST | `/api/v1/repos/{ns}/{name}/rr/{iid}/review` | propose | `{state: approved\|request_changes\|comment, body}` |
| POST | `/api/v1/repos/{ns}/{name}/rr/{iid}/comment` | propose | `{body, path?}` |
| POST | `/api/v1/repos/{ns}/{name}/rr/{iid}/resolve` | propose | `{path, side}` for a conflicted merge |
| POST | `/api/v1/repos/{ns}/{name}/rr/{iid}/merge` | merge | always commits a new single-parent revision on the target |
| POST | `/api/v1/repos/{ns}/{name}/rr/{iid}/close` | merge | close without merging (idempotent; a merged RR is refused) |

## Smart protocol (sync)

Used by the CLI (`fetch`/`pull`/`push`). Bodies are binary bundles
(`transfer.Bundle`), gzip-compressed.

| Method | Path | Auth | Notes |
|---|---|---|---|
| POST | `/api/v1/repos/{ns}/{name}/advertise` | read | list revisions + refs (+ per-revision snapshot hashes) |
| POST | `/api/v1/repos/{ns}/{name}/fetch` | read | incremental bundle |
| POST | `/api/v1/repos/{ns}/{name}/push` | write | non-fast-forward is rejected (409); atomic apply |

---

## Notes

- Errors are JSON: `{"error": "..."}`. Client errors (4xx) carry a semantic
  message; server errors (5xx) are collapsed to a generic message (details go to
  the server log).
- Request bodies are bounded (`EASYVCS_MAX_BODY`, default 512 MiB); gzip bodies
  are decompressed with a bound.
- This module does **not** provide a git wire protocol, a server-side working
  copy / staging area, or sandbox/session lifecycle — those are out of scope.
