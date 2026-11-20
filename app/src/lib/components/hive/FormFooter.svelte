<script lang="ts">
  // Error line plus Cancel / Save. Save is disabled while a save is running,
  // so a second tap cannot store the record twice.
  import { _ } from 'svelte-i18n';
  import './hive.css';

  let {
    busy,
    error = '',
    invalid = false,
    oncancel
  }: { busy: boolean; error?: string; invalid?: boolean; oncancel: () => void } = $props();
</script>

{#if error}
  <p class="hv-err" role="alert">{$_('common.save_failed', { values: { msg: error } })}</p>
{/if}
<div class="hv-actions">
  <button type="button" class="hv-btn ghost" onclick={oncancel} disabled={busy}
    >{$_('common.cancel')}</button
  >
  <button type="submit" class="hv-btn save" disabled={busy || invalid}
    >{busy ? $_('common.saving') : $_('common.save')}</button
  >
</div>

<style>
  .save {
    min-width: 120px;
  }
</style>
