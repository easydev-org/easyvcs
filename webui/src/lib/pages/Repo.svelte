<script lang="ts">
  import { File as FileIcon, Folder, GitCommitHorizontal, GitBranch, Tag, ArrowUp, Copy, Check, GitPullRequest, ChevronRight, ChevronDown, Trash2 } from '@lucide/svelte'
  import Badge from '$lib/components/ui/badge.svelte'
  import Button from '$lib/components/ui/button.svelte'
  import Skeleton from '$lib/components/ui/skeleton.svelte'
  import Confirm from '$lib/components/ui/confirm.svelte'
  import type { Api, Entry, Commit, Ref, ConflictView, CommitMeta, RRDiff } from '$lib/api'
  import { i18n } from '$lib/i18n/store.svelte'
  import { router } from '$lib/router.svelte'
  import { toasts } from '$lib/toast.svelte'
  import { highlight, extOf, parseUnified } from '$lib/diff'
  import { renderMarkdown } from '$lib/markdown'
  import Requests from '$lib/pages/Requests.svelte'

  let { api, ns, onBack }: { api: Api; ns: string; onBack: () => void } = $props()
  let tab = $state<'code' | 'branches' | 'tags' | 'revisions' | 'requests'>('code')
  let ref = $state('main')
  let path = $state('')
  let scope = $state('')
  let iid = $state('')
  let entries = $state<Entry[]>([])
  let blob = $state<{ is_text: boolean; content?: string; size: number; conflict?: boolean } | null>(null)
  let conflict = $state<ConflictView | null>(null)
  let readme = $state<{ path: string; content: string } | null>(null)
  let branches = $state<Ref[]>([])
  let tags = $state<Ref[]>([])
  let commits = $state<Commit[]>([])
  let meta = $state<{ description?: string; visibility?: string }>({})
  let detail = $state<{ rev: CommitMeta; files: RRDiff[] } | null>(null)
  let loading = $state(false)
  let copied = $state('')
  let err = $state('')
  let confirmDel = $state<string | null>(null)
  let refOpen = $state(false)

  // Sync local view state from the URL route.
  $effect(() => {
    const r = router.route
    if (r.ns !== ns) return
    ref = r.ref || 'main'
    path = r.path
    tab = (r.tab || 'code') as typeof tab
    iid = r.iid
  })
  const set = (patch: Record<string, string>) => router.navigate({ ns, ref, path, tab, iid, ...patch })

  async function loadMeta() {
    try {
      meta = await api.repo(ns)
    } catch {}
  }
  async function loadRefs() {
    try {
      branches = (await api.branches(ns)).branches ?? []
      tags = (await api.tags(ns)).tags ?? []
    } catch {}
  }
  async function loadTab() {
    err = ''
    loading = true
    detail = null
    try {
      if (tab === 'code') await loadTree()
      else if (tab === 'branches') await loadRefs()
      else if (tab === 'tags') tags = (await api.tags(ns)).tags ?? []
      else if (tab === 'revisions') commits = (await api.log(ns, ref)).commits ?? []
    } catch (e) {
      err = String(e)
    } finally {
      loading = false
    }
  }
  const fullPath = (p: string) => (scope ? (p ? scope + '/' + p : scope) : p)
  async function loadTree() {
    entries = (await api.tree(ns, ref, fullPath(path))).entries ?? []
    if (!path) await loadReadme()
  }
  async function loadReadme() {
    readme = null
    for (const name of ['README.md', 'README', 'readme.md', 'README.rst', 'README.txt']) {
      const e = entries.find((x) => x.name === name && x.kind === 'file')
      if (e) {
        try {
          const b = await api.blob(ns, ref, fullPath(e.path))
          if (b.is_text) readme = { path: e.path, content: b.content ?? '' }
        } catch {}
        return
      }
    }
  }
  async function open(e: Entry) {
    if (e.kind === 'dir') {
      set({ path: e.path })
    } else {
      blob = await api.blob(ns, ref, fullPath(e.path))
      conflict = null
      if (blob.conflict) {
        try {
          conflict = await api.conflict(ns, ref, fullPath(e.path))
        } catch {}
      }
    }
  }
  function up() {
    const i = path.lastIndexOf('/')
    set({ path: i < 0 ? '' : path.slice(0, i) })
  }
  function setScope(s: string) {
    scope = s.trim().replace(/^\/+|\/+$/g, '')
    set({ path: '' })
  }
  function pickRef(kind: string, name: string) {
    refOpen = false
    set({ ref: name, path: '' })
  }
  async function showCommit(sha: string) {
    loading = true
    try {
      const rev = await api.commit(ns, sha)
      let files: RRDiff[] = []
      if (rev.parents && rev.parents.length) {
        files = (await api.diff(ns, rev.parents[0], rev.sha)).files ?? []
      }
      detail = { rev, files }
    } catch (e) {
      toasts.error(String(e))
    } finally {
      loading = false
    }
  }
  async function doDeleteBranch(name: string) {
    try {
      await api.deleteBranch(ns, name)
      toasts.success(i18n.t('ns.deleted') + ': ' + name)
      confirmDel = null
      await loadRefs()
    } catch (e) {
      toasts.error(String(e))
    }
  }
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      copied = text
      toasts.success(i18n.t('common.copied'))
      setTimeout(() => (copied = ''), 1200)
    } catch {}
  }
  const cloneUrl = $derived(`${location.origin}/api/v1/namespaces/${ns}`)
  const fmtSize = (n: number) => (n < 1024 ? n + ' B' : (n / 1024).toFixed(1) + ' KB')
  const when = (ms?: number) => (ms ? new Date(ms).toLocaleString() : '')
  const crumbs = $derived(path ? path.split('/') : [])
  const conflictObjectUrl = (sha: string) => api.objectUrl(ns, sha)
  const shortRef = $derived(ref.length > 20 ? ref.slice(0, 12) + '…' : ref)
  const refOptions = $derived([
    ...branches.map((b) => ({ kind: 'branch', name: b.name })),
    ...tags.map((t) => ({ kind: 'tag', name: t.name })),
    ...commits.slice(0, 10).map((c) => ({ kind: 'revision', name: c.sha })),
  ])

  $effect(() => {
    ns
    loadMeta()
    loadRefs()
  })
  $effect(() => {
    tab
    ref
    loadTab()
  })
</script>

<div class="mb-3 flex flex-wrap items-center gap-2">
  <button onclick={onBack} class="text-meta text-muted-foreground hover:text-foreground">← {i18n.t('ns.back')}</button>
  <h2 class="truncate text-body font-semibold">{ns}</h2>
  {#if meta.visibility}<Badge variant="muted">{meta.visibility}</Badge>{/if}
</div>

<div class="mb-4 flex items-center gap-2 overflow-hidden rounded-md border border-border bg-secondary px-3 py-1.5">
  <span class="hidden text-micro text-muted-foreground sm:inline">{i18n.t('ns.clone')}</span>
  <code class="truncate font-mono text-micro">git clone {cloneUrl}</code>
  <button onclick={() => copy(`git clone ${cloneUrl}`)} class="ml-auto text-muted-foreground hover:text-foreground">
    {#if copied === `git clone ${cloneUrl}`}<Check class="size-3.5 text-success" />{:else}<Copy class="size-3.5" />{/if}
  </button>
</div>

<nav class="mb-4 flex gap-1 overflow-x-auto">
  {#each [['code', FileIcon], ['branches', GitBranch], ['tags', Tag], ['revisions', GitCommitHorizontal], ['requests', GitPullRequest]] as [id, Icon] (id)}
    <button onclick={() => set({ tab: id, iid: '', path: '' })}
      class="flex shrink-0 items-center gap-1.5 rounded-md border px-3 py-1 text-meta {tab === id ? 'border-primary text-foreground' : 'border-border text-muted-foreground hover:text-foreground'}">
      <Icon class="size-3.5" />{i18n.t(('tabs.' + id) as 'tabs.code')}
    </button>
  {/each}
</nav>

{#if err}<p class="mb-3 text-meta text-destructive">{i18n.t('common.error')}: {err}</p>{/if}

{#if tab === 'code'}
  <div class="mb-3 flex flex-wrap items-center gap-2">
    <!-- ref switcher -->
    <div class="relative">
      <button onclick={() => (refOpen = !refOpen)} class="flex h-8 items-center gap-1.5 rounded-md border border-input bg-secondary px-2 text-meta">
        <GitBranch class="size-3.5 text-muted-foreground" /><span class="font-mono">{shortRef}</span><ChevronDown class="size-3.5 text-muted-foreground" />
      </button>
      {#if refOpen}
        <div class="absolute left-0 top-9 z-30 max-h-80 w-72 overflow-auto rounded-md border border-border bg-card p-1 shadow-xl">
          {#each [['branch', i18n.t('ns.branch')], ['tag', i18n.t('ns.tag')], ['revision', i18n.t('ns.revision')]] as [kind, label] (kind)}
            {@const opts = refOptions.filter((o) => o.kind === kind)}
            {#if opts.length}
              <div class="px-2 pt-2 pb-1 text-micro uppercase text-muted-foreground">{label}</div>
              {#each opts as o (kind + o.name)}
                <button onclick={() => pickRef(kind, o.name)} class="flex w-full items-center gap-2 rounded px-2 py-1 text-left text-micro hover:bg-secondary {o.name === ref ? 'text-foreground' : 'text-muted-foreground'}">
                  {#if kind === 'branch'}<GitBranch class="size-3" />{:else if kind === 'tag'}<Tag class="size-3" />{:else}<GitCommitHorizontal class="size-3" />{/if}
                  <span class="truncate font-mono">{o.name}</span>
                </button>
              {/each}
            {/if}
          {/each}
        </div>
      {/if}
    </div>
    <span class="text-micro text-muted-foreground">{i18n.t('ns.scope')}</span>
    <input value={scope} onchange={(e) => setScope((e.target as HTMLInputElement).value)} placeholder={i18n.t('ns.scopePlaceholder')} class="h-8 w-48 rounded-md border border-input bg-secondary px-2 font-mono text-micro sm:w-64" />
  </div>

  <div class="grid gap-4 md:grid-cols-[220px_minmax(0,1fr)]">
    <aside class="overflow-hidden rounded-lg border border-border bg-card">
      <div class="border-b border-border px-3 py-2 text-micro font-medium">{scope || ns}</div>
      <div class="max-h-72 overflow-auto p-2 md:max-h-none">
        {#if path}
          <button onclick={up} class="mb-1 flex items-center gap-1 text-micro text-primary hover:underline"><ArrowUp class="size-3" /> {i18n.t('ns.up')}</button>
        {/if}
        {#if loading}<div class="p-1"><Skeleton lines={4} /></div>{/if}
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
          <div class="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2 font-mono text-micro text-muted-foreground">
            {#each crumbs as c, i (i)}
              <button onclick={() => set({ path: crumbs.slice(0, i + 1).join('/') })} class="hover:text-foreground">{c}</button>
              <ChevronRight class="size-3" />
            {/each}
            <span>{path.split('/').pop()}</span>
            <span class="ml-auto">{fmtSize(blob.size)}</span>
            {#if blob.conflict}<Badge variant="destructive">{i18n.t('ns.conflict')}</Badge>{/if}
          </div>
          {#if blob.conflict && conflict}
            <div class="grid gap-3 p-3" style="grid-template-columns: repeat({Math.min(conflict.terms.length, 3)}, minmax(0, 1fr));">
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
        <div class="overflow-hidden rounded-lg border border-border bg-card">
          <div class="border-b border-border px-3 py-2 text-micro text-muted-foreground">{readme.path}</div>
          <div class="p-4 text-meta leading-relaxed">{@html renderMarkdown(readme.content)}</div>
        </div>
      {:else if loading}
        <div class="rounded-lg border border-border bg-card p-4"><Skeleton lines={6} /></div>
      {/if}
    </section>
  </div>

{:else if tab === 'branches'}
  <div class="overflow-hidden rounded-lg border border-border bg-card">
    <table class="w-full border-collapse"><tbody>
      {#each branches as b (b.name)}
        <tr class="border-b border-border/60 last:border-0">
          <td class="px-3 py-2 font-mono text-meta">{b.name}</td>
          <td class="px-3 py-2 text-right font-mono text-micro text-muted-foreground">{(b.sha ?? b.target ?? '').slice(0, 12)}</td>
          <td class="w-10 px-3 py-2 text-right"><button onclick={() => (confirmDel = b.name)} class="text-muted-foreground hover:text-destructive" title={i18n.t('ns.deleteBranch')}><Trash2 class="size-3.5" /></button></td>
        </tr>
      {/each}
      {#if branches.length === 0}<tr><td class="px-3 py-3 text-meta text-muted-foreground">{i18n.t('common.none')}</td></tr>{/if}
    </tbody></table>
  </div>
{:else if tab === 'tags'}
  <div class="overflow-hidden rounded-lg border border-border bg-card">
    <table class="w-full border-collapse"><tbody>
      {#each tags as t (t.name)}
        <tr class="border-b border-border/60 last:border-0"><td class="px-3 py-2 font-mono text-meta">{t.name}</td><td class="px-3 py-2 text-right font-mono text-micro text-muted-foreground">{(t.sha ?? t.target ?? '').slice(0, 12)}</td></tr>
      {/each}
      {#if tags.length === 0}<tr><td class="px-3 py-3 text-meta text-muted-foreground">{i18n.t('common.none')}</td></tr>{/if}
    </tbody></table>
  </div>
{:else if tab === 'requests'}
  <Requests {api} {ns} {iid} />
{:else}
  {#if detail}
    <button onclick={() => (detail = null)} class="mb-3 text-meta text-muted-foreground hover:text-foreground">← {i18n.t('ns.backToList')}</button>
    <div class="mb-4 overflow-hidden rounded-lg border border-border bg-card">
      <div class="border-b border-border px-4 py-3">
        <p class="text-body font-medium">{detail.rev.description || i18n.t('common.none')}</p>
        <p class="mt-1 text-micro text-muted-foreground">{detail.rev.author} · {when(detail.rev.time)} · <span class="font-mono">{detail.rev.sha.slice(0, 12)}</span></p>
        {#if detail.rev.parents && detail.rev.parents.length}
          <p class="mt-1 text-micro text-muted-foreground">{i18n.t('ns.parent')}: {#each detail.rev.parents as p, i (p)}{#if i}, {/if}<button class="font-mono text-primary hover:underline" onclick={() => showCommit(p)}>{p.slice(0, 12)}</button>{/each}</p>
        {/if}
        {#if detail.rev.conflicted}<p class="mt-1"><Badge variant="destructive">{i18n.t('ns.conflict')}: {(detail.rev.conflicts ?? []).join(', ')}</Badge></p>{/if}
      </div>
      {#each detail.files as d (d.path)}
        {@const pf = parseUnified(d.content ?? '', d.path, d.status, false, d.added, d.removed)}
        <div class="border-b border-border/60 last:border-0">
          <div class="flex items-center gap-2 border-b border-border bg-secondary px-3 py-1.5 text-micro">
            <span class="font-mono">{d.path}</span><Badge variant="muted">{d.status}</Badge>
            <span class="text-success">+{d.added}</span><span class="text-destructive">-{d.removed}</span>
          </div>
          {#if pf.lines.length}
            <div class="overflow-auto font-mono text-micro leading-relaxed">
              {#each pf.lines as ln, i (i)}
                {#if ln.kind === 'hunk'}<div class="bg-secondary/60 px-3 py-0.5 text-muted-foreground">{ln.text}</div>
                {:else}
                  <div class="flex {ln.kind === 'add' ? 'bg-success/10' : ln.kind === 'del' ? 'bg-destructive/10' : ''}">
                    <span class="w-10 shrink-0 select-none px-1 text-right text-muted-foreground">{ln.oldNo ?? ''}</span>
                    <span class="w-10 shrink-0 select-none px-1 text-right text-muted-foreground">{ln.newNo ?? ''}</span>
                    <span class="w-4 shrink-0 select-none text-center text-muted-foreground">{ln.kind === 'add' ? '+' : ln.kind === 'del' ? '-' : ' '}</span>
                    <span class="flex-1 whitespace-pre pr-3">{@html highlight(ln.text, extOf(d.path))}</span>
                  </div>
                {/if}
              {/each}
            </div>
          {:else}<p class="px-3 py-2 text-meta text-muted-foreground">{i18n.t('common.noChanges')}</p>{/if}
        </div>
      {/each}
    </div>
  {:else}
    <ul class="overflow-hidden rounded-lg border border-border bg-card">
      {#each commits as c (c.sha)}
        <li class="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border/60 px-3 py-2 last:border-0">
          <button onclick={() => showCommit(c.sha)} class="font-mono text-micro text-primary hover:underline">{c.sha.slice(0, 8)}</button>
          <button onclick={() => showCommit(c.sha)} class="min-w-0 flex-1 truncate text-left text-meta hover:underline">{c.description ?? c.message ?? i18n.t('common.none')}</button>
          <span class="text-micro text-muted-foreground">{c.author ?? ''}</span>
          <span class="text-micro text-muted-foreground">{when(c.created)}</span>
        </li>
      {/each}
      {#if commits.length === 0}<li class="px-3 py-3 text-meta text-muted-foreground">{i18n.t('common.none')}</li>{/if}
    </ul>
  {/if}
{/if}

{#if confirmDel}
  <Confirm
    title={i18n.t('ns.deleteBranch')}
    message={i18n.t('ns.confirmDeleteBranch', { name: confirmDel })}
    onconfirm={() => doDeleteBranch(confirmDel!)}
    oncancel={() => (confirmDel = null)}
  />
{/if}
