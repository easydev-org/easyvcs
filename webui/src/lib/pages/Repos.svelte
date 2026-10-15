<script lang="ts">
  import { onMount } from 'svelte'
  import { FolderGit2, Lock, Plus, Search } from '@lucide/svelte'
  import Button from '$lib/components/ui/button.svelte'
  import type { Api, Repo } from '$lib/api'
  import { i18n } from '$lib/i18n/store.svelte'

  let { api, onOpen }: { api: Api; onOpen: (r: Repo) => void } = $props()
  let repos = $state<Repo[]>([])
  let q = $state('')
  let creating = $state(false)
  let form = $state({ ns: '', visibility: 'private', init: true })
  let err = $state('')
  let msg = $state('')

  async function load() {
    try {
      repos = (await api.repos()).repos ?? []
    } catch (e) {
      err = String(e)
    }
  }
  onMount(load)
  const shown = $derived(repos.filter((r) => r.namespace.toLowerCase().includes(q.toLowerCase())))
  async function create() {
    err = ''
    msg = ''
    try {
      await api.createRepo(form.ns.trim(), { visibility: form.visibility, init: form.init })
      msg = i18n.t('namespaces.created', { ns: form.ns.trim() })
      creating = false
      form = { ns: '', visibility: 'private', init: true }
      await load()
    } catch (e) {
      err = String(e)
    }
  }
</script>

{#if err}<p class="mb-3 text-meta text-destructive">{err}</p>{/if}
{#if msg}<p class="mb-3 text-meta text-success">{msg}</p>{/if}
<div class="mb-4 flex items-center gap-2">
  <div class="flex h-8 items-center gap-2 rounded-md border border-input bg-secondary px-3">
    <Search class="size-3.5 text-muted-foreground" />
    <input bind:value={q} placeholder={i18n.t('namespaces.filter')} class="w-56 bg-transparent text-meta outline-none placeholder:text-muted-foreground" />
  </div>
  <Button onclick={() => (creating = !creating)}><Plus class="size-3.5" /> {i18n.t('namespaces.new')}</Button>
  <span class="ml-auto text-micro text-muted-foreground">{i18n.t('namespaces.count', { n: shown.length })}</span>
</div>

{#if creating}
  <div class="mb-4 flex flex-wrap items-end gap-2 rounded-lg border border-border bg-card p-4">
    <label class="text-micro text-muted-foreground">{i18n.t('namespaces.label')}<input bind:value={form.ns} placeholder="team/project" class="mt-1 block h-8 w-56 rounded-md border border-input bg-secondary px-2 text-meta" /></label>
    <label class="text-micro text-muted-foreground">{i18n.t('namespaces.visibility')}
      <select bind:value={form.visibility} class="mt-1 block h-8 rounded-md border border-input bg-secondary px-2 text-meta">
        <option value="private">{i18n.t('namespaces.privateLabel')}</option><option value="public">{i18n.t('namespaces.publicLabel')}</option>
      </select>
    </label>
    <label class="flex items-center gap-1 text-meta"><input type="checkbox" bind:checked={form.init} /> {i18n.t('namespaces.init')}</label>
    <Button onclick={create}>{i18n.t('common.create')}</Button>
  </div>
{/if}

<div class="overflow-hidden rounded-lg border border-border bg-card">
  <table class="w-full border-collapse">
    <tbody>
      {#each shown as r (r.namespace)}
        <tr class="border-b border-border/60 last:border-0 hover:bg-secondary">
          <td class="px-4 py-2.5">
            <button onclick={() => onOpen(r)} class="flex items-center gap-2 text-meta hover:underline">
              <FolderGit2 class="size-3.5 text-muted-foreground" />
              <span class="font-medium">{r.namespace}</span>
            </button>
          </td>
          <td class="px-4 py-2.5 text-right text-micro text-muted-foreground">{r.default_branch}</td>
          <td class="px-4 py-2.5 text-right">{#if r.private}<Lock class="ml-auto size-3 text-muted-foreground" />{/if}</td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
