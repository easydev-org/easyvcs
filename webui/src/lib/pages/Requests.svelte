<script lang="ts">
  import { GitPullRequest, MessageSquare } from '@lucide/svelte'
  import Button from '$lib/components/ui/button.svelte'
  import Badge from '$lib/components/ui/badge.svelte'
  import Skeleton from '$lib/components/ui/skeleton.svelte'
  import Confirm from '$lib/components/ui/confirm.svelte'
  import type { Api, RR, RRDiff, ConflictView } from '$lib/api'
  import { i18n } from '$lib/i18n/store.svelte'
  import { router } from '$lib/router.svelte'
  import { toasts } from '$lib/toast.svelte'
  import { parseUnified, highlight, extOf } from '$lib/diff'
  import { renderMarkdown } from '$lib/markdown'

  let { api, ns, iid }: { api: Api; ns: string; iid: string } = $props()
  let list = $state<RR[]>([])
  let open = $state<RR | null>(null)
  let diffs = $state<RRDiff[]>([])
  let comment = $state('')
  let err = $state('')
  let loading = $state(false)
  let conflictViews = $state<Record<string, ConflictView>>({})
  let creating = $state(false)
  let branches = $state<{ name: string; sha?: string }[]>([])
  let form = $state({ source: '', target: 'main', title: '', body: '' })
  let confirmClose = $state(false)

  async function load() {
    err = ''
    loading = true
    try {
      list = (await api.rrList(ns)).requests ?? []
      branches = (await api.branches(ns)).branches ?? []
      if (!form.target) form.target = branches[0]?.name ?? 'main'
    } catch (e) {
      err = String(e)
    } finally {
      loading = false
    }
  }
  async function show(n: number) {
    err = ''
    loading = true
    try {
      open = await api.rrGet(ns, n)
      diffs = (await api.rrDiff(ns, n)).files ?? []
      await loadConflicts()
    } catch (e) {
      err = String(e)
    } finally {
      loading = false
    }
  }
  async function loadConflicts() {
    conflictViews = {}
    if (!open?.pending_revision) return
    for (const p of open.conflicts ?? []) {
      try {
        conflictViews[p] = await api.conflict(ns, open.pending_revision!, p)
      } catch {}
    }
  }
  function select(i: number) {
    router.navigate({ ns, iid: String(i) })
  }
  function back() {
    router.navigate({ ns, iid: '' })
  }
  async function resolve(path: string, side: number) {
    if (!open) return
    try {
      await api.rrResolve(ns, open.iid, path, { side })
      toasts.success(i18n.t('rr.resolved'))
      await show(open.iid)
    } catch (e) {
      toasts.error(String(e))
    }
  }
  async function doComment() {
    if (!open || !comment.trim()) return
    try {
      open = await api.rrComment(ns, open.iid, comment.trim())
      comment = ''
    } catch (e) {
      toasts.error(String(e))
    }
  }
  async function doReview(state: string) {
    if (!open) return
    try {
      open = await api.rrReview(ns, open.iid, state, '')
      toasts.success(i18n.t('rr.reviewed'))
    } catch (e) {
      toasts.error(String(e))
    }
  }
  async function doMerge(allowConflict = false) {
    if (!open) return
    try {
      await api.rrMerge(ns, open.iid, allowConflict)
      toasts.success(i18n.t('rr.merged', { iid: open.iid }))
      await show(open.iid)
      await load()
    } catch (e) {
      toasts.error(String(e))
    }
  }
  async function doClose() {
    if (!open) return
    confirmClose = false
    try {
      open = await api.rrClose(ns, open.iid)
      await load()
    } catch (e) {
      toasts.error(String(e))
    }
  }
  function sourceSha(name: string): string {
    return branches.find((b) => b.name === name)?.sha ?? ''
  }
  async function create() {
    const src = sourceSha(form.source)
    if (!src) {
      toasts.error(i18n.t('rr.pickSource'))
      return
    }
    try {
      const r = await api.rrCreate(ns, { source_snapshot: src, target_branch: form.target, title: form.title, body: form.body })
      creating = false
      form = { source: '', target: 'main', title: '', body: '' }
      await load()
      select(r.iid)
    } catch (e) {
      toasts.error(String(e))
    }
  }
  $effect(() => {
    ns
    load()
  })
  $effect(() => {
    const n = iid
    if (n) show(Number(n))
    else open = null
  })
  const when = (ms: number) => new Date(ms).toLocaleString()
  const parsed = (d: RRDiff) => parseUnified(d.content ?? '', d.path, d.status, false, d.added, d.removed)
</script>

{#if err}<p class="mb-3 text-meta text-destructive">{i18n.t('common.error')}: {err}</p>{/if}

{#if !open}
  <div class="mb-3 flex items-center">
    <Button onclick={() => (creating = !creating)}>{i18n.t('rr.new')}</Button>
  </div>
  {#if creating}
    <div class="mb-4 grid gap-2 rounded-lg border border-border bg-card p-4 sm:grid-cols-2">
      <label class="text-micro text-muted-foreground">{i18n.t('rr.sourceBranch')}
        <select bind:value={form.source} class="mt-1 block h-8 w-full rounded-md border border-input bg-secondary px-2 text-meta">
          <option value="">—</option>
          {#each branches as b (b.name)}<option value={b.name}>{b.name}</option>{/each}
        </select>
      </label>
      <label class="text-micro text-muted-foreground">{i18n.t('rr.targetBranch')}
        <select bind:value={form.target} class="mt-1 block h-8 w-full rounded-md border border-input bg-secondary px-2 text-meta">
          {#each branches as b (b.name)}<option value={b.name}>{b.name}</option>{/each}
        </select>
      </label>
      <label class="text-micro text-muted-foreground sm:col-span-2">{i18n.t('rr.title')}
        <input bind:value={form.title} class="mt-1 block h-8 w-full rounded-md border border-input bg-secondary px-2 text-meta" />
      </label>
      <label class="text-micro text-muted-foreground sm:col-span-2">{i18n.t('rr.body')}
        <input bind:value={form.body} class="mt-1 block h-8 w-full rounded-md border border-input bg-secondary px-2 text-meta" />
      </label>
      <div class="sm:col-span-2"><Button onclick={create}>{i18n.t('common.create')}</Button></div>
    </div>
  {/if}
  {#if loading}
    <div class="rounded-lg border border-border bg-card p-4"><Skeleton lines={4} /></div>
  {:else}
    <div class="overflow-hidden rounded-lg border border-border bg-card">
      <table class="w-full border-collapse"><tbody>
        {#each list as r (r.iid)}
          <tr class="border-b border-border/60 last:border-0 hover:bg-secondary">
            <td class="px-3 py-2">
              <button onclick={() => select(r.iid)} class="flex items-center gap-2 text-meta hover:underline">
                <GitPullRequest class="size-3.5 shrink-0 text-muted-foreground" />
                <span class="font-mono text-micro text-muted-foreground">#{r.iid}</span>
                <span class="truncate font-medium">{r.title}</span>
              </button>
            </td>
            <td class="hidden px-3 py-2 text-micro text-muted-foreground sm:table-cell">{r.target_branch}</td>
            <td class="px-3 py-2 text-right">
              <Badge variant="muted">{r.state}</Badge>
              {#if r.conflicts && r.conflicts.length}<Badge variant="destructive">{i18n.t('ns.conflict')}</Badge>{/if}
            </td>
          </tr>
        {/each}
        {#if list.length === 0}<tr><td class="px-3 py-3 text-meta text-muted-foreground">{i18n.t('rr.none')}</td></tr>{/if}
      </tbody></table>
    </div>
  {/if}
{:else}
  <div class="mb-3 flex flex-wrap items-center gap-2">
    <button onclick={back} class="text-meta text-muted-foreground hover:text-foreground">← {i18n.t('rr.back')}</button>
    <h2 class="text-body font-semibold">#{open.iid} {open.title}</h2>
    <Badge variant="muted">{open.state}</Badge>
  </div>
  {#if open.body}<div class="mb-3 text-meta text-muted-foreground">{@html renderMarkdown(open.body)}</div>{/if}
  <p class="mb-3 font-mono text-micro text-muted-foreground">{i18n.t('rr.source')} {open.source_snapshot.slice(0, 12)} → {open.target_branch}</p>

  {#if open.conflicts && open.conflicts.length}
    <div class="mb-3 rounded-md border border-destructive/40 bg-destructive/5 p-3">
      <p class="mb-2 text-meta text-destructive">{i18n.t('rr.conflictsAt')}: {open.conflicts.join(', ')}</p>
      {#each open.conflicts as cp (cp)}
        {@const cv = conflictViews[cp]}
        <div class="mb-2 rounded-md border border-border bg-card p-2">
          <div class="mb-1 font-mono text-micro">{cp}</div>
          {#if cv}
            <div class="grid gap-2" style="grid-template-columns: repeat({Math.min(cv.terms.length, 3)}, minmax(0, 1fr));">
              {#each cv.terms as t (t.role + ':' + t.sha)}
                <div class="overflow-hidden rounded border border-border">
                  <div class="flex items-center gap-1 border-b border-border bg-secondary px-2 py-1 text-micro">
                    <span class="font-medium">{t.role}</span>
                    {#if t.path}<span class="text-muted-foreground">@ {t.path}</span>{/if}
                    {#if t.role !== 'base'}
                      <button class="ml-auto text-primary hover:underline" onclick={() => resolve(cp, cv.terms.filter((x) => x.role !== 'base').indexOf(t))}>{i18n.t('ns.use')}</button>
                    {/if}
                  </div>
                  {#if t.is_text}<pre class="max-h-48 overflow-auto p-2 font-mono text-micro leading-relaxed">{@html highlight(t.content ?? '', extOf(cp))}</pre>
                  {:else if t.is_binary}<p class="p-2 text-micro text-muted-foreground">{i18n.t('ns.binaryFile')} · {t.size ?? 0}B</p>
                  {:else if t.kind === 'dir'}<p class="p-2 text-micro text-muted-foreground">{i18n.t('ns.directory')}: {(t.entries ?? []).join(', ')}</p>
                  {:else if t.kind === 'absent'}<p class="p-2 text-micro text-muted-foreground">{i18n.t('ns.deleted')}</p>
                  {:else}<p class="p-2 text-micro text-muted-foreground">{t.kind}</p>{/if}
                </div>
              {/each}
            </div>
          {:else}<p class="text-micro text-muted-foreground">{i18n.t('common.loading')}</p>{/if}
        </div>
      {/each}
      <div class="mt-2 flex gap-2"><Button onclick={() => doMerge(true)}>{i18n.t('rr.mergeAnyway')}</Button></div>
    </div>
  {/if}

  <div class="mb-3 flex flex-wrap gap-2">
    <Button onclick={() => doMerge(false)}>{i18n.t('common.merge')}</Button>
    <Button onclick={() => doReview('approved')}>{i18n.t('rr.approve')}</Button>
    <Button onclick={() => doReview('request_changes')}>{i18n.t('rr.requestChanges')}</Button>
    <Button onclick={() => (confirmClose = true)}>{i18n.t('common.close')}</Button>
  </div>

  <h3 class="mb-2 text-meta font-medium">{i18n.t('rr.diff')}</h3>
  <div class="mb-4 overflow-hidden rounded-lg border border-border bg-card">
    {#each diffs as d (d.path)}
      {@const pf = parsed(d)}
      <div class="border-b border-border/60 last:border-0">
        <div class="flex flex-wrap items-center gap-2 border-b border-border bg-secondary px-3 py-1.5 text-micro">
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
    {#if diffs.length === 0}<p class="px-3 py-3 text-meta text-muted-foreground">{i18n.t('common.noChanges')}</p>{/if}
  </div>

  <h3 class="mb-2 text-meta font-medium">{i18n.t('rr.comments')}</h3>
  <ul class="mb-2 overflow-hidden rounded-lg border border-border bg-card">
    {#each open.comments ?? [] as c (c.id)}
      <li class="flex items-start gap-2 border-b border-border/60 px-3 py-2 last:border-0 text-meta">
        <MessageSquare class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        <span class="min-w-0 flex-1">{@html renderMarkdown(c.body)}{#if c.path}<span class="text-micro text-muted-foreground"> @ {c.path}</span>{/if}</span>
        <span class="text-micro text-muted-foreground">{when(c.created)}</span>
      </li>
    {/each}
    {#if (open.comments ?? []).length === 0}<li class="px-3 py-2 text-meta text-muted-foreground">{i18n.t('rr.noComments')}</li>{/if}
  </ul>
  <div class="flex gap-2">
    <input bind:value={comment} placeholder={i18n.t('rr.addComment')} class="h-8 flex-1 rounded-md border border-input bg-secondary px-2 text-meta" />
    <Button onclick={doComment}>{i18n.t('common.comment')}</Button>
  </div>

  {#if confirmClose}
    <Confirm title={i18n.t('common.close')} message={i18n.t('rr.confirmClose')} onconfirm={doClose} oncancel={() => (confirmClose = false)} />
  {/if}
{/if}
