<script lang="ts">
  import { X } from '@lucide/svelte'
  import type { Snippet } from 'svelte'
  let { title = '', onclose, children }: { title?: string; onclose: () => void; children: Snippet } = $props()
</script>

<div class="fixed inset-0 z-50 flex items-center justify-center p-4">
  <div class="absolute inset-0 bg-black/50" onclick={onclose} role="presentation"></div>
  <div class="relative z-10 w-full max-w-md overflow-hidden rounded-lg border border-border bg-card shadow-xl">
    <div class="flex items-center gap-2 border-b border-border px-4 py-3">
      <h3 class="flex-1 text-body font-semibold">{title}</h3>
      <button onclick={onclose} class="text-muted-foreground hover:text-foreground"><X class="size-4" /></button>
    </div>
    <div class="p-4">{@render children()}</div>
  </div>
</div>
