<script lang="ts">
  import { toasts } from '$lib/toast.svelte'
  import { CheckCircle2, XCircle, Info, X } from '@lucide/svelte'
</script>

<div class="pointer-events-none fixed inset-x-0 bottom-0 z-50 flex flex-col items-center gap-2 p-4 sm:items-end sm:p-5">
  {#each toasts.items as t (t.id)}
    <div
      class="pointer-events-auto flex w-full max-w-sm items-start gap-2 rounded-lg border px-3 py-2 shadow-lg backdrop-blur {t.kind === 'success'
        ? 'border-success/40 bg-success/10'
        : t.kind === 'error'
          ? 'border-destructive/40 bg-destructive/10'
          : 'border-border bg-card'}"
    >
      {#if t.kind === 'success'}<CheckCircle2 class="mt-0.5 size-4 shrink-0 text-success" />
      {:else if t.kind === 'error'}<XCircle class="mt-0.5 size-4 shrink-0 text-destructive" />
      {:else}<Info class="mt-0.5 size-4 shrink-0 text-muted-foreground" />{/if}
      <span class="flex-1 text-meta">{t.message}</span>
      <button onclick={() => toasts.dismiss(t.id)} class="text-muted-foreground hover:text-foreground"><X class="size-3.5" /></button>
    </div>
  {/each}
</div>
