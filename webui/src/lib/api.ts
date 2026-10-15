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
  branches = (ns: string) => this.req<{ branches: Ref[] }>(`/api/v1/namespaces/${ns}/branches`)
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
}
