<script lang="ts">
  // Compact sync status: a dot and one line saying whether this device's
  // changes are on the server (pending uploads, refused changes, offline,
  // signed out, time of the last sync). Tapping it syncs now; while the
  // server refuses changes it leads to the list of them instead.
  import { _, locale } from 'svelte-i18n';
  import { syncStatus, syncNow } from '#lib/local/sync.js';
  import { chipView, ago } from './status.js';

  let {
    // Where <SyncProblems/> is shown; the chip links there while there are any.
    problemsHref = '',
    // Render as plain text, for use inside another link or button.
    passive = false
  }: { problemsHref?: string; passive?: boolean } = $props();

  // Re-evaluates "x min ago" while the page stays open.
  let now = $state(Date.now());
  $effect(() => {
    const timer = setInterval(() => (now = Date.now()), 30_000);
    return () => clearInterval(timer);
  });

  const view = $derived(chipView($syncStatus));
  const label = $derived($_(view.key, { values: { n: view.n } }));
  const when = $derived.by(() => {
    const at = $syncStatus.lastSyncedAt;
    if (at === null) return '';
    return ago(at, now, $locale) ?? $_('sync.just_now');
  });
  const synced = $derived(
    when ? $_('sync.last_synced', { values: { when } }) : $_('sync.never_synced')
  );
  // The label of a fully synced device already says so; add the time to it.
  const text = $derived(view.tone === 'ok' && when ? `${label} · ${when}` : label);
  const href = $derived(
    $syncStatus.state === 'signed_out' ? '/login' : $syncStatus.failed > 0 ? problemsHref : ''
  );
</script>

{#snippet body()}
  <span class="dot" aria-hidden="true"></span><span class="txt">{text}</span>
{/snippet}

{#if passive}
  <span class="chip {view.tone}" title={synced}>{@render body()}</span>
{:else if href}
  <a class="chip {view.tone}" {href} title={synced}>{@render body()}</a>
{:else}
  <button
    type="button"
    class="chip {view.tone}"
    title={`${synced} · ${$_('sync.chip_sync_now')}`}
    onclick={() => void syncNow()}
  >
    {@render body()}
  </button>
{/if}

<style>
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 100%;
    min-width: 0;
    font: inherit;
    font-size: 0.76rem;
    font-weight: 600;
    line-height: 1.2;
    color: var(--text-soft);
    text-decoration: none;
    background: none;
    border: none;
    padding: 0;
  }
  a.chip,
  button.chip {
    cursor: pointer;
    padding: 5px 10px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface);
  }
  .txt {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex-shrink: 0;
    background: var(--ok);
  }
  .busy .dot {
    background: var(--accent);
  }
  .warn .dot {
    background: var(--warn-text);
  }
  .bad .dot {
    background: var(--danger);
  }
  .bad {
    color: var(--danger);
  }
</style>
