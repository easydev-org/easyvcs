<script lang="ts">
  import { File as FileIcon, Folder, GitCommitHorizontal, GitBranch, Tag, ArrowUp, Copy, Check } from '@lucide/svelte'
  import Badge from '$lib/components/ui/badge.svelte'
  import type { Api, Repo, Entry, Commit, Ref } from '$lib/api'

  let { api, repo, onBack }: { api: Api; repo: Repo; onBack: () => void } = $props()
  let tab = $state<'code' | 'branches' | 'tags' | 'commits'>('code')
  let ref = $state(repo.default_branch || 'main')
  let path = $state('')
  let entries = $state<Entry[]>([])
  let blob = $state<{ is_text: boolean; content?: string; size: number } | null>(null)
  let branches = $state<Ref[]>([])
  let tags = $state<Ref[]>([])
  let commits = $state<Commit[]>([])
  let meta = $state<{ description?: string; visibility?: string }>({})
  let copied = $state('')
  let err = $state('')

  async function loadMeta() {
    try {
      meta = await api.repo(repo.org, repo.repo)
    } catch {}
  }
  async function loadTab() {
    err = ''
    try {
      if (tab === 'code') await loadTree()
      else if (tab === 'branches') branches = (await api.branches(repo.org, repo.repo)).branches ?? []
      else if (tab === 'tags') tags = (await api.tags(repo.org, repo.repo)).tags ?? []
      else commits = (await api.log(repo.org, repo.repo, ref)).commits ?? []
    } catch (e) {
      err = String(e)
    }
  }
  async function loadTree() {
    entries = (await api.tree(repo.org, repo.repo, ref, path)).entries ?? []
  }
  async function open(e: Entry) {
    if (e.kind === 'dir') {
      path = e.path
      blob = null
      await loadTree()
    } else {
      blob = await api.blob(repo.org, repo.repo, ref, e.path)
    }
  }
  function up() {
    const i = path.lastIndexOf('/')
    path = i < 0 ? '' : path.slice(0, i)
    blob = null
    loadTree()
  }
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      copied = text
      setTimeout(() => (copied = ''), 1200)
    } catch {}
  }
  const cloneUrl = $derived(`${location.origin}/api/v1/repos/${repo.org}/${repo.repo}`)
  const fmtSize = (n: number) => (n < 1024 ? n + ' B' : (n / 1024).toFixed(1) + ' KB')
  const when = (ms?: number) => (ms ? new Date(ms).toLocaleString() : '')

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
  <button onclick={onBack} class="text-meta text-muted-foreground hover:text-foreground">← repositories</button>
  <h2 class="text-body font-semibold">{repo.org}/{repo.repo}</h2>
  <Badge variant="muted"><GitBranch class="mr-1 size-3" />{ref}</Badge>
  {#if meta.visibility}<Badge variant="muted">{meta.visibility}</Badge>{/if}
</div>
{#if meta.description}<p class="mb-3 text-meta text-muted-foreground">{meta.description}</p>{/if}

<div class="mb-4 flex items-center gap-2 rounded-md border border-border bg-secondary px-3 py-1.5">
  <span class="text-micro text-muted-foreground">clone</span>
  <code class="truncate font-mono text-micro">git clone {cloneUrl}</code>
  <button onclick={() => copy(`git clone ${cloneUrl}`)} class="ml-auto text-muted-foreground hover:text-foreground">
    {#if copied === `git clone ${cloneUrl}`}<Check class="size-3.5 text-success" />{:else}<Copy class="size-3.5" />{/if}
  </button>
</div>

<nav class="mb-4 flex gap-1">
  {#each [['code', FileIcon], ['branches', GitBranch], ['tags', Tag], ['commits', GitCommitHorizontal]] as [id, Icon] (id)}
    <button onclick={() => (tab = id as typeof tab)}
      class="flex items-center gap-1.5 rounded-md border px-3 py-1 text-meta {tab === id ? 'border-primary text-foreground' : 'border-border text-muted-foreground hover:text-foreground'}">
      <Icon class="size-3.5" />{id}
    </button>
  {/each}
</nav>

{#if err}<p class="mb-3 text-meta text-destructive">{err}</p>{/if}

{#if tab === 'code'}
  {#if path}
    <div class="mb-2 flex items-center gap-2">
      <button onclick={up} class="flex items-center gap-1 text-micro text-primary hover:underline"><ArrowUp class="size-3" /> up</button>
      <span class="font-mono text-micro text-muted-foreground">{repo.repo}/{path}</span>
    </div>
  {/if}
  {#if blob}
    <div class="overflow-hidden rounded-lg border border-border bg-card">
      <div class="border-b border-border px-3 py-2 font-mono text-micro text-muted-foreground">{path} · {fmtSize(blob.size)}</div>
      {#if blob.is_text}<pre class="overflow-auto p-3 font-mono text-meta leading-relaxed">{blob.content}</pre>
      {:else}<p class="p-3 text-meta text-muted-foreground">binary file</p>{/if}
    </div>
  {:else}
    <div class="overflow-hidden rounded-lg border border-border bg-card">
      <table class="w-full border-collapse">
        <tbody>
          {#each entries as e (e.path)}
            <tr class="border-b border-border/60 last:border-0 hover:bg-secondary">
              <td class="px-3 py-2">
                <button onclick={() => open(e)} class="flex items-center gap-2 text-meta hover:underline">
                  {#if e.kind === 'dir'}<Folder class="size-3.5 text-accent" />{:else}<FileIcon class="size-3.5 text-muted-foreground" />{/if}{e.name}
                </button>
              </td>
              <td class="px-3 py-2 text-right text-micro text-muted-foreground">{e.kind === 'file' ? fmtSize(e.size) : ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
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
{:else}
  <ul class="overflow-hidden rounded-lg border border-border bg-card">
    {#each commits as c (c.sha)}
      <li class="flex items-center gap-3 border-b border-border/60 px-3 py-2 last:border-0">
        <span class="font-mono text-micro text-primary">{c.sha.slice(0, 8)}</span>
        <span class="flex-1 truncate text-meta">{c.message ?? '(no message)'}</span>
        <span class="text-micro text-muted-foreground">{c.author ?? ''}</span>
        <span class="text-micro text-muted-foreground">{when(c.created)}</span>
      </li>
    {/each}
  </ul>
{/if}
