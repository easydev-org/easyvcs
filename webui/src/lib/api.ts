// Typed client for the easyvcs hosting REST API (/api/v1). Reads are anonymous
// on public repos; a token (localStorage) is sent as a Bearer header.
// A namespace is the SINGLE container (there is no separate repository). Its
// name may contain slashes (e.g. "easydev-org/easyvcs"); the old "repository"
// is a ?path= scope within it. See docs/namespace-container.md.
export interface Repo {
  namespace: string
  default_branch?: string
  private?: boolean
}
export interface Entry {
  name: string
  path: string
  kind: string
  size: number
  sha: string
}
export interface Commit {
  revision_id?: string
  sha: string
  created?: number
  author?: string
  // The server returns the revision's message as `description`.
  description?: string
  message?: string
}
export interface Ref {
  name: string
  sha?: string
  target?: string
}
export interface User {
  id: number
  username: string
  display_name?: string
  disabled?: boolean
  created?: number
}

export interface ConflictTerm {
  role: string
  label: string
  path?: string
  sha: string
  kind: 'blob' | 'dir' | 'conflict' | 'absent' | 'unknown' | 'other'
  size?: number
  is_text?: boolean
  is_binary?: boolean
  content?: string
  entries?: string[]
}
export interface ConflictView {
  path: string
  conflict_sha: string
  terms: ConflictTerm[]
}

export interface RR {
  iid: number
  source_snapshot: string
  base_snapshot: string
  target_branch: string
  state: string
  title: string
  body?: string
  path?: string
  created: number
  updated: number
  conflicts?: string[]
  pending_revision?: string
  reviews?: { id: number; state: string; body: string; created: number }[]
  comments?: { id: number; body: string; path?: string; created: number }[]
  merged_by?: number | null
}
export interface SearchHit {
  path: string
  line?: number
  text?: string
  matches?: string[]
}
export interface CommitMeta {
  revision_id: string
  sha: string
  tree?: string
  parents?: string[]
  description?: string
  author?: string
  session?: string
  time?: number
  conflicted?: boolean
  conflicts?: string[]
}
export interface RRDiff {
  path: string
  status: string
  added: number
  removed: number
  content?: string
}

export class Api {
  constructor(private token: () => string) {}
  private h(): HeadersInit {
    const t = this.token().trim()
    return t ? { Authorization: 'Bearer ' + t } : {}
  }
  private async req<T>(path: string, init?: RequestInit): Promise<T> {
    const r = await fetch(path, { ...init, headers: { ...this.h(), ...(init?.headers ?? {}) } })
    if (!r.ok) throw new Error('HTTP ' + r.status)
    return (await r.json()) as T
  }
  repos = (owned = false) => this.req<{ repos: Repo[] }>('/api/v1/namespaces' + (owned ? '?owned=true' : ''))
  repo = (ns: string) =>
    this.req<{ description?: string; visibility?: string; default_branch?: string; empty?: boolean; kind?: string }>(
      `/api/v1/namespaces/${ns}`,
    )
  tree = (ns: string, ref: string, path: string) =>
    this.req<{ entries: Entry[] }>(
      `/api/v1/namespaces/${ns}/tree?` + new URLSearchParams({ ref, path }),
    )
  // Directory listing for a SUB-SCOPE: `scope` is the ?path= directory, `dir`
  // the sub-path within it. A namespace's old "repository" is just a scope.
  treeScoped = (ns: string, ref: string, scope: string, dir: string) =>
    this.req<{ entries: Entry[] }>(
      `/api/v1/namespaces/${ns}/tree?` +
        new URLSearchParams({ ref, path: dir ? (scope ? scope + '/' + dir : dir) : scope }),
    )
  blob = (ns: string, ref: string, path: string) =>
    this.req<{ is_text: boolean; content?: string; size: number; binary?: boolean }>(
      `/api/v1/namespaces/${ns}/blob?` + new URLSearchParams({ ref, path }),
    )
  log = (ns: string, ref: string, limit = 50) =>
    this.req<{ commits: Commit[] }>(
      `/api/v1/namespaces/${ns}/log?` + new URLSearchParams({ ref, limit: String(limit) }),
    )
  // Structured conflict view: every WAY (base/ours/theirs/…) with its content
  // (text inline; binary/dir carry a sha to fetch via objectUrl).
  conflict = (ns: string, ref: string, path: string) =>
    this.req<ConflictView>(
      `/api/v1/namespaces/${ns}/conflict?` + new URLSearchParams({ ref, path }),
    )
  objectUrl = (ns: string, sha: string) =>
    `/api/v1/namespaces/${ns}/object?` + new URLSearchParams({ sha })
  // Revision/snapshot metadata (author, message, parents, conflicted, conflicts).
  commit = (ns: string, sha: string) =>
    this.req<CommitMeta>(`/api/v1/namespaces/${ns}/commit/${sha}`)
  // Line diff between two refs (unified per-file content).
  diff = (ns: string, base: string, head: string) =>
    this.req<{ files: RRDiff[] }>(`/api/v1/namespaces/${ns}/diff?` + new URLSearchParams({ base, head }))
  // Write a file (create/update/delete) on a NON-protected branch via /commit.
  commitFiles = (
    ns: string,
    body: { branch: string; message: string; files: { path: string; operation: string; content?: string }[] },
  ) =>
    this.req<{ ok: boolean; branch: string; revision_id: string; snapshot: string }>(
      `/api/v1/namespaces/${ns}/commit`,
      { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) },
    )
  createBranch = (ns: string, name: string, from: string) =>
    this.req<Ref>(`/api/v1/namespaces/${ns}/branches`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, from }),
    })
  createTag = (ns: string, name: string, target: string) =>
    this.req<Ref>(`/api/v1/namespaces/${ns}/tags`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, target }),
    })
  search = (ns: string, ref: string, q: { name?: string; content?: string }) =>
    this.req<{ results: SearchHit[] }>(
      `/api/v1/namespaces/${ns}/search?` + new URLSearchParams({ ref, ...q } as Record<string, string>),
    )
  branches = (ns: string) => this.req<{ branches: Ref[] }>(`/api/v1/namespaces/${ns}/branches`)
  deleteBranch = (ns: string, branch: string) =>
    this.req<unknown>(`/api/v1/namespaces/${ns}/branches/${branch}`, { method: 'DELETE' })
  tags = (ns: string) => this.req<{ tags: Ref[] }>(`/api/v1/namespaces/${ns}/tags`)
  users = () => this.req<{ users: User[] }>('/api/v1/users')
  createUser = (username: string) =>
    this.req<{ user: User; token?: string }>('/api/v1/users', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username }),
    })
  // Create a namespace (the container). `ns` may contain slashes.
  createRepo = (ns: string, body: { description?: string; visibility?: string; init?: boolean }) =>
    this.req<unknown>(`/api/v1/namespaces/${ns}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
  whoami = () => this.req<{ authenticated: boolean; username?: string }>('/api/v1/whoami')

  // ---- Revision Requests (collaboration) ----
  rrList = (ns: string, state?: string) =>
    this.req<{ requests: RR[] }>(
      `/api/v1/namespaces/${ns}/rr` + (state ? '?' + new URLSearchParams({ state }) : ''),
    )
  rrGet = (ns: string, iid: number) => this.req<RR>(`/api/v1/namespaces/${ns}/rr/${iid}`)
  rrCreate = (ns: string, body: { source_snapshot: string; target_branch: string; title: string; body?: string }) =>
    this.req<RR>(`/api/v1/namespaces/${ns}/rr`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
  rrDiff = (ns: string, iid: number) =>
    this.req<{ files: RRDiff[] }>(`/api/v1/namespaces/${ns}/rr/${iid}/diff`)
  rrComment = (ns: string, iid: number, body: string, path?: string) =>
    this.req<RR>(`/api/v1/namespaces/${ns}/rr/${iid}/comment`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ body, path: path ?? '' }),
    })
  rrReview = (ns: string, iid: number, state: string, body: string) =>
    this.req<RR>(`/api/v1/namespaces/${ns}/rr/${iid}/review`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ state, body }),
    })
  // Resolve a conflict: pick a side, choose a rename path, or write bytes.
  rrResolve = (
    ns: string,
    iid: number,
    path: string,
    opt: { side?: number; choosePath?: string; contentB64?: string },
  ) =>
    this.req<RR>(`/api/v1/namespaces/${ns}/rr/${iid}/resolve`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        path,
        side: opt.side ?? 0,
        choose_path: opt.choosePath ?? '',
        content: opt.contentB64 ?? '',
      }),
    })
  rrMerge = (ns: string, iid: number, allowConflict = false) =>
    this.req<Record<string, unknown>>(
      `/api/v1/namespaces/${ns}/rr/${iid}/merge` + (allowConflict ? '?allow_conflict=true' : ''),
      { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' },
    )
  rrClose = (ns: string, iid: number) =>
    this.req<RR>(`/api/v1/namespaces/${ns}/rr/${iid}/close`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    })
}
