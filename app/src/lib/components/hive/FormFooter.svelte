<script lang="ts">
  // Error line plus Cancel / Save. Save is disabled while a save is running,
  // so a second tap cannot store the record twice, and in the read-only demo.
  import { _ } from 'svelte-i18n';
  import { demoSession } from '#lib/demo.js';
  import DemoNote from '../DemoNote.svelte';
  import './hive.css';

  let {
    busy,
    error = '',
    invalid = false,
    oncancel
  }: { busy: boolean; error?: string; invalid?: boolean; oncancel: () => void } = $props();
</script>

{#if error}
  <p class="hv-err" role="alert">{$_('common.save_failed', { values: { msg: $_(error) } })}</p>
{/if}
<DemoNote />
<div class="hv-actions">
  <button type="button" class="hv-btn ghost" onclick={oncancel} disabled={busy}
    >{$_('common.cancel')}</button
  >
  <button type="submit" class="hv-btn save" disabled={busy || invalid || $demoSession}
    >{busy ? $_('common.saving') : $_('common.save')}</button
  >
</div>

<style>
  .save {
    min-width: 120px;
  }
</style>
