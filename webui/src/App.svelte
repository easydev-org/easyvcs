<script lang="ts">
  // EasyVCS web UI — a browser over the hosting REST API (/api/v1). Reads are
  // anonymous on public namespaces; a token (kept locally) is sent as a Bearer
  // header for private namespaces / admin actions. State lives in the URL hash
  // (see router.svelte.ts) so links are shareable and back/forward work.
  import { FolderGit2, Users as UsersIcon, Languages, Menu, X } from '@lucide/svelte'
  import Button from '$lib/components/ui/button.svelte'
  import Input from '$lib/components/ui/input.svelte'
  import Toaster from '$lib/components/ui/toaster.svelte'
  import { Api, type Repo } from '$lib/api'
  import { i18n } from '$lib/i18n/store.svelte'
  import { router } from '$lib/router.svelte'
  import { toasts } from '$lib/toast.svelte'
  import Repos from '$lib/pages/Repos.svelte'
  import RepoView from '$lib/pages/Repo.svelte'
  import Users from '$lib/pages/Users.svelte'

  router.init()

  const KEY = 'easyvcsToken'
  let token = $state(localStorage.getItem(KEY) ?? '')
  let page = $state<'namespaces' | 'users'>('namespaces')
  let menuOpen = $state(false)
  const api = new Api(() => token)

  // The namespace open in the route (if any). A RepoView is shown when set.
  let ns = $state('')
  $effect(() => {
    ns = router.route.ns
    if (ns) page = 'namespaces'
  })

  function save() {
    localStorage.setItem(KEY, token.trim())
    toasts.success(i18n.t('common.connected'))
  }
  function openNs(r: Repo) {
    router.navigate({ ns: r.namespace, ref: r.default_branch ?? 'main', path: '', tab: 'code', iid: '' })
  }
  function goHome() {
    router.navigate({ ns: '', ref: '', path: '', tab: '', iid: '' })
    page = 'namespaces'
  }
</script>

<div class="flex min-h-dvh flex-col bg-background text-foreground">
  <header class="flex h-12 shrink-0 items-center gap-3 border-b border-border bg-background/80 px-3 backdrop-blur sm:px-5">
    <button class="text-muted-foreground hover:text-foreground sm:hidden" onclick={() => (menuOpen = !menuOpen)} aria-label="menu">
      {#if menuOpen}<X class="size-4" />{:else}<Menu class="size-4" />{/if}
    </button>
    <button class="flex size-7 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground" onclick={goHome}><FolderGit2 class="size-4" /></button>
    <button class="hidden text-left leading-tight sm:block" onclick={goHome}>
      <span class="block text-body font-semibold">EasyVCS</span>
      <span class="block text-micro text-muted-foreground">{i18n.t('common.tagline')}</span>
    </button>
    <div class="ml-auto flex items-center gap-2">
      <button
        onclick={() => i18n.set(i18n.lang === 'en' ? 'zh' : 'en')}
        class="flex h-8 items-center gap-1 rounded-md border border-border px-2 text-micro text-muted-foreground hover:text-foreground"
        title={i18n.t('common.language')}
      >
        <Languages class="size-3.5" />{i18n.lang === 'en' ? '中文' : 'EN'}
      </button>
      <Input type="password" bind:value={token} placeholder={i18n.t('common.tokenPlaceholder')} class="hidden w-56 sm:block md:w-72" />
      <Button onclick={save}>{i18n.t('common.connect')}</Button>
    </div>
  </header>

  <nav class="flex shrink-0 gap-1 border-b border-border px-2 sm:px-3">
    {#each [['namespaces', FolderGit2], ['users', UsersIcon]] as [id, Icon] (id)}
      <button onclick={() => { page = id as typeof page; goHome() }}
        class="flex items-center gap-1.5 border-b-2 px-3 py-2 text-meta transition {page === id ? 'border-primary text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground'}">
        <Icon class="size-3.5" />{i18n.t(id === 'namespaces' ? 'nav.namespaces' : 'nav.users')}
      </button>
    {/each}
  </nav>

  <main class="min-h-0 flex-1 overflow-auto px-3 py-4 sm:px-5 sm:py-5">
    {#key page + token + i18n.lang + ns}
      {#if page === 'users'}<Users {api} />
      {:else if ns}<RepoView {api} ns={ns} onBack={goHome} />
      {:else}<Repos {api} onOpen={openNs} />{/if}
    {/key}
  </main>
</div>

<Toaster />
