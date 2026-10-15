<script lang="ts">
  import { File as FileIcon, Folder, GitCommitHorizontal, GitBranch, Tag, ArrowUp, Copy, Check, GitPullRequest, ChevronRight, ChevronDown } from '@lucide/svelte'
  import Badge from '$lib/components/ui/badge.svelte'
  import type { Api, Repo, Entry, Commit, Ref, ConflictView } from '$lib/api'
  import { i18n } from '$lib/i18n/store.svelte'
  import { highlight, extOf } from '$lib/diff'
  import Requests from '$lib/pages/Requests.svelte'

  let { api, repo, onBack }: { api: Api; repo: Repo; onBack: () => void } = $props()
  let tab = $state<'code' | 'branches' | 'tags' | 'revisions' | 'requests'>('code')
  let ref = $state(repo.default_branch || 'main')
  let path = $state('')
  let scope = $state('')
  let entries = $state<Entry[]>([])
  let blob = $state<{ is_text: boolean; content?: string; size: number; conflict?: boolean } | null>(null)
  let conflict = $state<ConflictView | null>(null)
  let readme = $state<{ path: string; content: string } | null>(null)
  let branches = $state<Ref[]>([])
  let tags = $state<Ref[]>([])
  let commits = $state<Commit[]>([])
  let meta = $state<{ description?: string; visibility?: string }>({})
  let copied = $state('')
  let err = $state('')

  async function loadMeta() {
    try {
      meta = await api.repo(repo.namespace)
    } catch {}
  }
  async function loadTab() {
    err = ''
    try {
      if (tab === 'code') await loadTree()
      else if (tab === 'branches') branches = (await api.branches(repo.namespace)).branches ?? []
      else if (tab === 'tags') tags = (await api.tags(repo.namespace)).tags ?? []
      else if (tab === 'revisions') commits = (await api.log(repo.namespace, ref)).commits ?? []
    } catch (e) {
      err = String(e)
    }
  }
  const fullPath = (p: string) => (scope ? (p ? scope + '/' + p : scope) : p)
  async function loadTree() {
    entries = (await api.tree(repo.namespace, ref, fullPath(path))).entries ?? []
    if (!path) await loadReadme()
  }
  async function loadReadme() {
    readme = null
    for (const name of ['README.md', 'README', 'readme.md', 'README.rst', 'README.txt']) {
      const e = entries.find((x) => x.name === name && x.kind === 'file')
      if (e) {
        try {
          const b = await api.blob(repo.namespace, ref, fullPath(e.path))
          if (b.is_text) readme = { path: e.path, content: b.content ?? '' }
        } catch {}
        return
      }
    }
  }
  async function open(e: Entry) {
    if (e.kind === 'dir') {
      path = e.path
      blob = null
      conflict = null
      await loadTree()
    } else {
      blob = await api.blob(repo.namespace, ref, fullPath(e.path))
      if (blob.conflict) {
        try {
          conflict = await api.conflict(repo.namespace, ref, fullPath(e.path))
        } catch {
          conflict = null
        }
      } else {
        conflict = null
      }
    }
  }
  function up() {
    const i = path.lastIndexOf('/')
    path = i < 0 ? '' : path.slice(0, i)
    blob = null
    conflict = null
    loadTree()
  }
  function setScope(s: string) {
    scope = s.trim().replace(/^\/+|\/+$/g, '')
    path = ''
    blob = null
    conflict = null
    loadTree()
  }
  function goto(p: string) {
    path = p
    blob = null
    conflict = null
    loadTree()
  }
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      copied = text
      setTimeout(() => (copied = ''), 1200)
    } catch {}
  }
  const cloneUrl = $derived(`${location.origin}/api/v1/namespaces/${repo.namespace}`)
  const fmtSize = (n: number) => (n < 1024 ? n + ' B' : (n / 1024).toFixed(1) + ' KB')
  const when = (ms?: number) => (ms ? new Date(ms).toLocaleString() : '')
  const crumbs = $derived(path ? path.split('/') : [])
  const conflictObjectUrl = (sha: string) => api.objectUrl(repo.namespace, sha)

  $effect(() => {
    loadMeta()
  })
  $effect(() => {
    tab
    ref
    loadTab()
  })
</script>

<div class="mb-3 flex items-center gap-2">
  <button onclick={onBack} class="text-meta text-muted-foreground hover:text-foreground">← {i18n.t('ns.back')}</button>
  <h2 class="text-body font-semibold">{repo.namespace}</h2>
  <Badge variant="muted"><GitBranch class="mr-1 size-3" />{ref}</Badge>
  {#if meta.visibility}<Badge variant="muted">{meta.visibility}</Badge>{/if}
</div>
{#if meta.description}<p class="mb-3 text-meta text-muted-foreground">{meta.description}</p>{/if}

<div class="mb-4 flex items-center gap-2 rounded-md border border-border bg-secondary px-3 py-1.5">
  <span class="text-micro text-muted-foreground">{i18n.t('ns.clone')}</span>
  <code class="truncate font-mono text-micro">git clone {cloneUrl}</code>
  <button onclick={() => copy(`git clone ${cloneUrl}`)} class="ml-auto text-muted-foreground hover:text-foreground">
    {#if copied === `git clone ${cloneUrl}`}<Check class="size-3.5 text-success" />{:else}<Copy class="size-3.5" />{/if}
  </button>
</div>

<nav class="mb-4 flex gap-1">
  {#each [['code', FileIcon], ['branches', GitBranch], ['tags', Tag], ['revisions', GitCommitHorizontal], ['requests', GitPullRequest]] as [id, Icon] (id)}
    <button onclick={() => (tab = id as typeof tab)}
      class="flex items-center gap-1.5 rounded-md border px-3 py-1 text-meta {tab === id ? 'border-primary text-foreground' : 'border-border text-muted-foreground hover:text-foreground'}">
      <Icon class="size-3.5" />{i18n.t(('tabs.' + (id === 'commits' ? 'revisions' : id)) as 'tabs.code')}
    </button>
  {/each}
</nav>

{#if err}<p class="mb-3 text-meta text-destructive">{err}</p>{/if}

{#if tab === 'code'}
  <div class="mb-3 flex items-center gap-2">
    <span class="text-micro text-muted-foreground">{i18n.t('ns.scope')}</span>
    <input value={scope} onchange={(e) => setScope((e.target as HTMLInputElement).value)} placeholder={i18n.t('ns.scopePlaceholder')} class="h-8 w-64 rounded-md border border-input bg-secondary px-2 font-mono text-micro" />
  </div>
  <div class="grid gap-4" style="grid-template-columns: 220px minmax(0, 1fr);">
    <aside class="overflow-hidden rounded-lg border border-border bg-card">
      <div class="border-b border-border px-3 py-2 text-micro font-medium">{scope || repo.namespace}</div>
      <div class="p-2">
        {#if path}
          <button onclick={up} class="mb-1 flex items-center gap-1 text-micro text-primary hover:underline"><ArrowUp class="size-3" /> {i18n.t('ns.up')}</button>
        {/if}
        {#each entries as e (e.path)}
          <button onclick={() => open(e)} class="flex w-full items-center gap-1.5 rounded px-1.5 py-1 text-left text-micro hover:bg-secondary {e.path === path ? 'text-foreground' : 'text-muted-foreground'}">
            {#if e.kind === 'dir'}<Folder class="size-3.5 text-accent" />{:else}<FileIcon class="size-3.5" />{/if}<span class="truncate">{e.name}</span>
          </button>
        {/each}
      </div>
    </aside>

    <section class="min-w-0">
      {#if blob}
        <div class="overflow-hidden rounded-lg border border-border bg-card">
          <div class="flex items-center gap-2 border-b border-border px-3 py-2 font-mono text-micro text-muted-foreground">
            {#each crumbs as c, i (i)}
              <button onclick={() => goto(crumbs.slice(0, i + 1).join('/'))} class="hover:text-foreground">{c}</button>
              <ChevronRight class="size-3" />
            {/each}
            <span>{path.split('/').pop()}</span>
            <span class="ml-auto">{fmtSize(blob.size)}</span>
            {#if blob.conflict}<Badge variant="destructive">{i18n.t('ns.conflict')}</Badge>{/if}
          </div>
          {#if blob.conflict && conflict}
            <div class="grid gap-3 p-3" style="grid-template-columns: repeat({conflict.terms.length}, minmax(0, 1fr));">
              {#each conflict.terms as t (t.role + ':' + t.sha)}
                <div class="overflow-hidden rounded-md border border-border">
                  <div class="border-b border-border bg-secondary px-2 py-1 text-micro">
                    <span class="font-medium">{t.role}</span>
                    {#if t.path}<span class="text-muted-foreground"> @ {t.path}</span>{/if}
                    <span class="text-muted-foreground"> · {t.kind}</span>
                  </div>
                  {#if t.kind === 'blob' && t.is_text}
                    <pre class="max-h-96 overflow-auto p-2 font-mono text-micro leading-relaxed">{@html highlight(t.content ?? '', extOf(path))}</pre>
                  {:else if t.kind === 'blob' && t.is_binary}
                    <p class="p-2 text-micro text-muted-foreground">{i18n.t('ns.binaryFile')} · {fmtSize(t.size ?? 0)} · <a class="text-primary hover:underline" href={conflictObjectUrl(t.sha)}>{i18n.t('ns.download')}</a></p>
                  {:else if t.kind === 'dir'}
                    <p class="p-2 text-micro text-muted-foreground">{i18n.t('ns.directory')}: {(t.entries ?? []).join(', ')}</p>
                  {:else if t.kind === 'absent'}
                    <p class="p-2 text-micro text-muted-foreground">{i18n.t('ns.deleted')}</p>
                  {:else}
                    <p class="p-2 text-micro text-muted-foreground">{t.kind}</p>
                  {/if}
                </div>
              {/each}
            </div>
            <details class="border-t border-border px-3 py-2">
              <summary class="cursor-pointer text-micro text-muted-foreground">{i18n.t('ns.markerText')}</summary>
              <pre class="mt-2 overflow-auto font-mono text-micro leading-relaxed">{blob.content}</pre>
            </details>
          {:else if blob.is_text}
            <pre class="overflow-auto p-3 font-mono text-meta leading-relaxed">{@html highlight(blob.content ?? '', extOf(path))}</pre>
          {:else}
            <p class="p-3 text-meta text-muted-foreground">{i18n.t('ns.binaryFile')}</p>
          {/if}
        </div>
      {:else if !path && readme}
        <div class="mb-4 overflow-hidden rounded-lg border border-border bg-card">
          <div class="border-b border-border px-3 py-2 text-micro text-muted-foreground">{readme.path}</div>
          <pre class="overflow-auto p-4 text-meta leading-relaxed whitespace-pre-wrap">{readme.content}</pre>
        </div>
      {/if}
    </section>
  </div>
{:else if tab === 'branches'}
  <div class="overflow-hidden rounded-lg border border-border bg-card">
    <table class="w-full border-collapse"><tbody>
      {#each branches as b (b.name)}
        <tr class="border-b border-border/60 last:border-0"><td class="px-3 py-2 font-mono text-meta">{b.name}</td><td class="px-3 py-2 text-right font-mono text-micro text-muted-foreground">{(b.sha ?? b.target ?? '').slice(0, 12)}</td></tr>
      {/each}
    </tbody></table>
  </div>
{:else if tab === 'tags'}
  <div class="overflow-hidden rounded-lg border border-border bg-card">
    <table class="w-full border-collapse"><tbody>
      {#each tags as t (t.name)}
        <tr class="border-b border-border/60 last:border-0"><td class="px-3 py-2 font-mono text-meta">{t.name}</td><td class="px-3 py-2 text-right font-mono text-micro text-muted-foreground">{(t.sha ?? t.target ?? '').slice(0, 12)}</td></tr>
      {/each}
    </tbody></table>
  </div>
{:else if tab === 'requests'}
  <Requests {api} {repo} />
{:else}
  <ul class="overflow-hidden rounded-lg border border-border bg-card">
    {#each commits as c (c.sha)}
      <li class="flex items-center gap-3 border-b border-border/60 px-3 py-2 last:border-0">
        <span class="font-mono text-micro text-primary">{c.sha.slice(0, 8)}</span>
        <span class="flex-1 truncate text-meta">{c.description ?? c.message ?? i18n.t('common.none')}</span>
        <span class="text-micro text-muted-foreground">{c.author ?? ''}</span>
        <span class="text-micro text-muted-foreground">{when(c.created)}</span>
      </li>
    {/each}
  </ul>
{/if}
