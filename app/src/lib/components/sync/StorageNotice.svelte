<script lang="ts">
  // Tells the user where their data is, in the two cases where the answer is
  // not "safely on this device":
  //  - another tab owns the local database: a full-screen notice with a
  //    button that takes the database over for this tab;
  //  - the browser gives the app no persistent storage: a banner that stays
  //    for as long as the app runs on its in-memory fallback.
  // It also says, once, when the server had to be given this device's data
  // again (it was restored from a backup): a line the user can dismiss.
  import { _ } from 'svelte-i18n';
  import { resyncNotice, storageState } from '#lib/local/live.js';
  import { dismissResyncNotice } from '#lib/local/sync.js';
  import { requestTakeover } from '#lib/local/tablock.js';
</script>

{#if $storageState === 'elsewhere'}
  <div class="tab-notice" role="alertdialog" aria-modal="true" aria-labelledby="tab-notice-title">
    <div class="box">
      <h2 id="tab-notice-title">{$_('sync.tab_title')}</h2>
      <p>{$_('sync.tab_text')}</p>
      <button type="button" onclick={requestTakeover}>{$_('sync.tab_use_here')}</button>
    </div>
  </div>
{:else if $storageState === 'volatile'}
  <div class="volatile" role="status">{$_('sync.volatile_banner')}</div>
{/if}
{#if $resyncNotice && $storageState !== 'elsewhere'}
  <div class="resynced" role="status">
    <span>{$_('sync.resynced')}</span>
    <button type="button" onclick={() => void dismissResyncNotice()}>
      {$_('sync.resynced_dismiss')}
    </button>
  </div>
{/if}

<style>
  .tab-notice {
    position: fixed;
    inset: 0;
    z-index: 2000;
    display: grid;
    place-items: center;
    padding: 24px;
    background: var(--bg);
  }
  .box {
    max-width: 420px;
    display: grid;
    gap: 14px;
    text-align: center;
    color: var(--text);
  }
  .box p {
    color: var(--text-soft);
    line-height: 1.45;
  }
  .box button {
    justify-self: center;
    font: inherit;
    font-weight: 700;
    padding: 12px 22px;
    border: none;
    border-radius: 12px;
    background: var(--accent);
    color: var(--on-accent);
    cursor: pointer;
  }
  .volatile {
    position: sticky;
    top: 0;
    z-index: 32;
    padding: 9px 12px;
    font-size: 0.82rem;
    font-weight: 600;
    text-align: center;
    background: var(--warn-bg);
    color: var(--warn-text);
  }
  .resynced {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: center;
    gap: 6px 12px;
    padding: 8px 12px;
    font-size: 0.85rem;
    background: var(--surface);
    color: var(--text);
    border-bottom: 1px solid var(--border-strong);
  }
  .resynced button {
    font: inherit;
    font-weight: 700;
    min-height: 32px;
    padding: 4px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 9px;
    background: transparent;
    color: var(--text);
    cursor: pointer;
  }
</style>
