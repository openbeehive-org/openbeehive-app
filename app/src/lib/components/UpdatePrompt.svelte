<script lang="ts">
  // "New version available" banner. The running page keeps its version until
  // the user reloads, so nothing changes under an open form. The root layout
  // does the watching (lib/update.ts); this only shows the result.
  import { _ } from 'svelte-i18n';
  import { applyUpdate, updateReady } from '#lib/update.js';

  let dismissed = $state(false);
  let busy = $state(false);

  function reload() {
    busy = true;
    applyUpdate();
  }
</script>

<!-- The live region stays in the DOM so the notice is announced when it appears. -->
<div role="status">
  {#if $updateReady && !dismissed}
    <div class="update">
      <p>{$_('app.update_available')}</p>
      <button type="button" class="reload" onclick={reload} disabled={busy}>
        {$_('app.update_reload')}
      </button>
      <button type="button" class="later" onclick={() => (dismissed = true)} disabled={busy}>
        {$_('app.update_later')}
      </button>
    </div>
  {/if}
</div>

<style>
  .update {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: center;
    gap: 6px 10px;
    background: var(--surface);
    color: var(--text);
    border-bottom: 1px solid var(--border-strong);
    padding: 8px 12px;
    font-size: 0.88rem;
  }
  p {
    font-weight: 600;
  }
  button {
    font: inherit;
    font-weight: 700;
    cursor: pointer;
    border-radius: 9px;
    padding: 6px 14px;
    min-height: 44px;
  }
  button:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .reload {
    background: var(--accent);
    color: var(--on-accent);
    border: 1px solid var(--accent);
  }
  .later {
    background: transparent;
    color: var(--text);
    border: 1px solid var(--border-strong);
  }
</style>
