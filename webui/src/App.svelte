<script lang="ts">
  // EasyVCS web UI — a Forgejo-style browser over the hosting REST API
  // (/api/v1). Reads are anonymous on public repos; a token (kept locally) is
  // sent as a Bearer header for private repos / admin actions.
  import { FolderGit2, Users as UsersIcon, Languages } from '@lucide/svelte'
  import Button from '$lib/components/ui/button.svelte'
  import Input from '$lib/components/ui/input.svelte'
  import { Api, type Repo } from '$lib/api'
  import { i18n } from '$lib/i18n/store.svelte'
  import Repos from '$lib/pages/Repos.svelte'
  import RepoView from '$lib/pages/Repo.svelte'
  import Users from '$lib/pages/Users.svelte'

  const KEY = 'easyvcsToken'
  let token = $state(localStorage.getItem(KEY) ?? '')
  let page = $state<'repos' | 'users'>('repos')
  let openRepo = $state<Repo | null>(null)
  const api = new Api(() => token)

  function save() {
    localStorage.setItem(KEY, token.trim())
    const p = page
    page = p === 'repos' ? 'users' : 'repos'
    queueMicrotask(() => (page = p))
  }
</script>

<div class="flex h-dvh flex-col bg-background text-foreground">
  <header class="flex h-12 shrink-0 items-center gap-3 border-b border-border bg-background/80 px-5 backdrop-blur">
    <div class="flex size-7 items-center justify-center rounded-md bg-primary text-primary-foreground"><FolderGit2 class="size-4" /></div>
    <div class="leading-tight"><h1 class="text-body font-semibold">EasyVCS</h1><p class="text-micro text-muted-foreground">{i18n.t('common.tagline')}</p></div>
    <div class="ml-auto flex items-center gap-2">
      <button
        onclick={() => i18n.set(i18n.lang === 'en' ? 'zh' : 'en')}
        class="flex h-8 items-center gap-1 rounded-md border border-border px-2 text-micro text-muted-foreground hover:text-foreground"
        title={i18n.t('common.language')}
      >
        <Languages class="size-3.5" />{i18n.lang === 'en' ? '中文' : 'EN'}
      </button>
      <Input type="password" bind:value={token} placeholder={i18n.t('common.tokenPlaceholder')} class="w-72" />
      <Button onclick={save}>{i18n.t('common.connect')}</Button>
    </div>
  </header>

  <nav class="flex shrink-0 gap-1 border-b border-border px-3">
    {#each [['repos', FolderGit2], ['users', UsersIcon]] as [id, Icon] (id)}
      <button onclick={() => { page = id; openRepo = null }}
        class="flex items-center gap-1.5 border-b-2 px-3 py-2 text-meta transition {page === id ? 'border-primary text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground'}">
        <Icon class="size-3.5" />{i18n.t(id === 'repos' ? 'nav.namespaces' : 'nav.users')}
      </button>
    {/each}
  </nav>

  <main class="min-h-0 flex-1 overflow-auto px-5 py-5">
    {#key page + token + i18n.lang}
      {#if page === 'users'}<Users {api} />
      {:else if openRepo}<RepoView {api} repo={openRepo} onBack={() => (openRepo = null)} />
      {:else}<Repos {api} onOpen={(r) => (openRepo = r)} />{/if}
    {/key}
  </main>
</div>
