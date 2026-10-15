<script lang="ts">
  import { onMount } from 'svelte'
  import { UserPlus, Copy, Check } from '@lucide/svelte'
  import Button from '$lib/components/ui/button.svelte'
  import Badge from '$lib/components/ui/badge.svelte'
  import type { Api, User } from '$lib/api'
  import { i18n } from '$lib/i18n/store.svelte'
  import { toasts } from '$lib/toast.svelte'

  let { api }: { api: Api } = $props()
  let users = $state<User[]>([])
  let name = $state('')
  let newToken = $state('')
  let copied = $state('')
  let err = $state('')

  async function load() {
    err = ''
    try {
      users = (await api.users()).users ?? []
    } catch (e) {
      err = String(e)
    }
  }
  onMount(load)
  async function create() {
    err = ''
    try {
      const r = await api.createUser(name)
      newToken = r.token ?? ''
      name = ''
      toasts.success(i18n.t('users.created'))
      await load()
    } catch (e) {
      err = String(e)
    }
  }
  async function copy(t: string) {
    try {
      await navigator.clipboard.writeText(t)
      copied = t
      setTimeout(() => (copied = ''), 1200)
    } catch {}
  }
</script>

{#if err}
  <div class="mb-3 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-meta text-destructive">
    {i18n.t('users.adminOnly')} <span class="text-micro text-muted-foreground">({err})</span>
  </div>
{/if}
{#if newToken}
  <div class="mb-4 flex items-center gap-2 rounded-lg border border-success/30 bg-success/10 px-4 py-3">
    <span class="text-meta text-success">{i18n.t('users.newToken')}</span>
    <code class="font-mono text-micro">{newToken}</code>
    <button onclick={() => copy(newToken)} class="text-success">{#if copied === newToken}<Check class="size-3.5" />{:else}<Copy class="size-3.5" />{/if}</button>
  </div>
{/if}
<div class="mb-4 flex items-end gap-2">
  <label class="text-micro text-muted-foreground">{i18n.t('users.username')}
    <input bind:value={name} placeholder={i18n.t('users.username')} class="mt-1 block h-8 w-56 rounded-md border border-input bg-secondary px-2 text-meta" />
  </label>
  <Button onclick={create}><UserPlus class="size-3.5" /> {i18n.t('users.createUser')}</Button>
</div>
<div class="overflow-hidden rounded-lg border border-border bg-card">
  <table class="w-full border-collapse">
    <thead><tr class="border-b border-border text-left text-micro uppercase tracking-wider text-muted-foreground">
      <th class="px-4 py-3 font-medium">id</th><th class="px-4 py-3 font-medium">{i18n.t('users.username')}</th>
      <th class="px-4 py-3 font-medium">{i18n.t('users.display')}</th><th class="px-4 py-3 font-medium">{i18n.t('users.status')}</th>
    </tr></thead>
    <tbody>
      {#each users as u (u.id)}
        <tr class="border-b border-border/60 last:border-0 hover:bg-secondary">
          <td class="px-4 py-2.5 text-meta tabular-nums text-muted-foreground">{u.id}</td>
          <td class="px-4 py-2.5 text-meta font-medium">{u.username}</td>
          <td class="px-4 py-2.5 text-meta text-muted-foreground">{u.display_name ?? ''}</td>
          <td class="px-4 py-2.5">{#if u.disabled}<Badge variant="destructive">{i18n.t('users.disabled')}</Badge>{:else}<Badge variant="success">{i18n.t('users.active')}</Badge>{/if}</td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
