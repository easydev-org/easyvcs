<script lang="ts">
  let { lines = 3 }: { lines?: number } = $props()
</script>

<div class="space-y-2">
  {#each Array(lines) as _, i (i)}
    <div class="h-4 animate-pulse rounded bg-secondary" style="width: {70 + ((i * 13) % 30)}%"></div>
  {/each}
</div>
