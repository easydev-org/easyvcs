<script lang="ts">
  import Button from '$lib/components/ui/button.svelte'
  import Modal from '$lib/components/ui/modal.svelte'
  import { i18n } from '$lib/i18n/store.svelte'
  let { title, message, onconfirm, oncancel }: { title: string; message: string; onconfirm: () => void; oncancel: () => void } = $props()
</script>

<Modal {title} onclose={oncancel}>
  <p class="mb-4 text-meta text-muted-foreground">{message}</p>
  <div class="flex justify-end gap-2">
    <Button variant="outline" onclick={oncancel}>{i18n.t('common.cancel')}</Button>
    <Button onclick={onconfirm}>{i18n.t('common.confirm')}</Button>
  </div>
</Modal>
