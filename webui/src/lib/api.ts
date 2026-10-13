// Typed client for the easyvcs hosting REST API (/api/v1). Reads are anonymous
// on public repos; a token (localStorage) is sent as a Bearer header.
export interface Repo {
  org: string
  repo: string
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
  repos = (owned = false) => this.req<{ repos: Repo[] }>('/api/v1/repos' + (owned ? '?owned=true' : ''))
  repo = (o: string, n: string) =>
    this.req<{ description?: string; visibility?: string; default_branch?: string; empty?: boolean; kind?: string }>(
      `/api/v1/repos/${o}/${n}`,
    )
  tree = (o: string, n: string, ref: string, path: string) =>
    this.req<{ entries: Entry[] }>(
      `/api/v1/repos/${o}/${n}/tree?` + new URLSearchParams({ ref, path }),
    )
  blob = (o: string, n: string, ref: string, path: string) =>
    this.req<{ is_text: boolean; content?: string; size: number; binary?: boolean }>(
      `/api/v1/repos/${o}/${n}/blob?` + new URLSearchParams({ ref, path }),
    )
  log = (o: string, n: string, ref: string, limit = 50) =>
    this.req<{ commits: Commit[] }>(
      `/api/v1/repos/${o}/${n}/log?` + new URLSearchParams({ ref, limit: String(limit) }),
    )
  branches = (o: string, n: string) => this.req<{ branches: Ref[] }>(`/api/v1/repos/${o}/${n}/branches`)
  tags = (o: string, n: string) => this.req<{ tags: Ref[] }>(`/api/v1/repos/${o}/${n}/tags`)
  users = () => this.req<{ users: User[] }>('/api/v1/users')
  createUser = (username: string) =>
    this.req<{ user: User; token?: string }>('/api/v1/users', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username }),
    })
  createRepo = (o: string, n: string, body: { description?: string; visibility?: string; init?: boolean }) =>
    this.req<unknown>(`/api/v1/repos/${o}/${n}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
  createOrg = (org: string) =>
    this.req<unknown>('/api/v1/orgs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ org }),
    })
  whoami = () => this.req<{ authenticated: boolean; username?: string }>('/api/v1/whoami')
}
