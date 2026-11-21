<script lang="ts">
  // Compact sync status: a dot and one line saying whether this device's
  // changes are on the server (waiting uploads, refused changes, offline,
  // signed out, app too old, time of the last sync). Tapping it opens a
  // small sheet with the details: changes and photos still to upload, the
  // last sync, the refused changes, and "sync now". Signed out it leads to
  // the sign-in page; while the server refuses this app version it loads the
  // new one.
  import { tick } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { syncStatus, syncNow } from '#lib/local/sync.js';
  import { uploadPhotosNow, waitingUploads } from '#lib/photos/index.js';
  import { applyUpdate } from '#lib/update.js';
  import { chipView, ago } from './status.js';

  let {
    // Where <SyncProblems/> is shown; the sheet links there while there are any.
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
  const outdated = $derived($syncStatus.state === 'update_required');
  const signedOut = $derived($syncStatus.state === 'signed_out');

  let open = $state(false);
  let root = $state<HTMLElement>();
  let chip = $state<HTMLButtonElement>();
  let heading = $state<HTMLElement>();

  async function toggle() {
    if (outdated) return applyUpdate();
    open = !open;
    if (!open) return;
    await tick();
    heading?.focus();
  }
  function close(refocus = false) {
    open = false;
    if (refocus) chip?.focus();
  }
  function onWindowClick(e: MouseEvent) {
    if (open && root && !root.contains(e.target as Node)) close();
  }
  function onKey(e: KeyboardEvent) {
    if (open && e.key === 'Escape') close(true);
  }
  // Starts the work and gets out of the way: the chip shows the progress.
  function retry() {
    close(true);
    void syncNow();
    void uploadPhotosNow();
  }
</script>

<svelte:window onclick={onWindowClick} onkeydown={onKey} />

{#snippet body()}
  <span class="dot" aria-hidden="true"></span><span class="txt">{text}</span>
{/snippet}

{#if passive}
  <span class="chip {view.tone}" title={synced}>{@render body()}</span>
{:else if signedOut}
  <a class="hit" href="/login"><span class="chip pill {view.tone}">{@render body()}</span></a>
{:else}
  <span class="wrap" bind:this={root}>
    <button
      type="button"
      class="hit"
      bind:this={chip}
      title={outdated ? $_('sync.update_now') : synced}
      aria-expanded={outdated ? undefined : open}
      aria-controls={outdated ? undefined : 'sync-sheet'}
      onclick={toggle}
    >
      <span class="chip pill {view.tone}">{@render body()}</span>
    </button>
    {#if open}
      <div id="sync-sheet" class="sheet" role="dialog" aria-labelledby="sync-sheet-title">
        <h2 id="sync-sheet-title" tabindex="-1" bind:this={heading}>{$_('sync.sheet_title')}</h2>
        <p class="state {view.tone}">{label}</p>
        <dl>
          <dt>{$_('sync.sheet_last')}</dt>
          <dd>{when || $_('sync.sheet_never')}</dd>
          <dt>{$_('sync.sheet_changes')}</dt>
          <dd>{$syncStatus.pending}</dd>
          <dt>{$_('sync.sheet_photos')}</dt>
          <dd>{$waitingUploads}</dd>
        </dl>
        {#if $syncStatus.pending > 1}
          <p class="hint">{$_('sync.sheet_changes_hint')}</p>
        {/if}
        {#if $syncStatus.failed > 0}
          <p class="bad">{$_('sync.sheet_problems', { values: { n: $syncStatus.failed } })}</p>
          {#if problemsHref}
            <a class="action" href={problemsHref} onclick={() => close()}
              >{$_('sync.sheet_problems_link')} ›</a
            >
          {/if}
        {/if}
        <div class="buttons">
          <button type="button" class="action primary" onclick={retry}
            >{$_('sync.chip_sync_now')}</button
          >
          <button type="button" class="action" onclick={() => close(true)}
            >{$_('sync.sheet_close')}</button
          >
        </div>
      </div>
    {/if}
  </span>
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
  /* The visible pill stays small; the button around it is the 44 px target. */
  .hit {
    display: inline-flex;
    align-items: center;
    max-width: 100%;
    min-width: 0;
    min-height: 44px;
    padding: 0;
    border: none;
    background: none;
    font: inherit;
    cursor: pointer;
    text-decoration: none;
  }
  .pill {
    padding: 5px 10px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface);
  }
  .hit:hover .pill {
    border-color: var(--border-strong);
  }
  .wrap {
    position: relative;
    display: inline-flex;
    max-width: 100%;
    min-width: 0;
  }
  .sheet {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 50;
    width: min(320px, calc(100vw - 28px));
    padding: 16px;
    border: 1px solid var(--border-strong);
    border-radius: 14px;
    background: var(--surface);
    color: var(--text);
    box-shadow: 0 10px 30px color-mix(in srgb, var(--text) 18%, transparent);
    font-size: 0.9rem;
    text-align: left;
  }
  .sheet h2 {
    font-size: 1.05rem;
    margin-bottom: 4px;
    outline: none;
  }
  .sheet .state {
    font-weight: 600;
    margin-bottom: 10px;
  }
  .sheet dl {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 4px 12px;
    margin-bottom: 8px;
  }
  .sheet dt {
    color: var(--text-soft);
  }
  .sheet dd {
    font-weight: 600;
    text-align: right;
  }
  .sheet .hint {
    color: var(--text-soft);
    font-size: 0.8rem;
    margin-bottom: 8px;
  }
  .sheet .bad {
    margin-bottom: 4px;
  }
  .buttons {
    display: flex;
    gap: 8px;
    margin-top: 10px;
  }
  .action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 44px;
    padding: 8px 14px;
    border: 1px solid var(--border-strong);
    border-radius: 11px;
    background: var(--surface);
    color: var(--text);
    font: inherit;
    font-weight: 600;
    text-decoration: none;
    cursor: pointer;
  }
  a.action {
    padding-left: 0;
    border: none;
    color: var(--accent-text);
  }
  .action.primary {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--on-accent);
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
