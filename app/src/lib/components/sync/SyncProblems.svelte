<script lang="ts">
  // The changes the server refused, with what to do about each: try again
  // (after fixing the cause, e.g. switching to the right workspace) or
  // discard the upload. Meant for the Settings page; renders nothing but a
  // short "no problems" line while the list is empty.
  import { _, locale } from 'svelte-i18n';
  import {
    syncStatus,
    listFailed,
    retryFailed,
    discardFailed,
    type FailedChange
  } from '#lib/local/sync.js';
  import { formatDate } from '#lib/dates.js';
  import { codeKey, entityKey } from './status.js';

  let items = $state<FailedChange[]>([]);
  let busy = $state(false);

  async function load() {
    items = await listFailed();
  }
  // The failed count moves whenever the list does (a sync run parked or
  // re-accepted something), so it doubles as the reload signal.
  $effect(() => {
    $syncStatus.failed;
    void load();
  });

  async function run(action: () => Promise<void>) {
    busy = true;
    try {
      await action();
    } finally {
      busy = false;
      await load();
    }
  }
  const retry = (id?: string) => run(() => retryFailed(id));
  function discard(f: FailedChange) {
    const ask =
      f.kind === 'unapplied'
        ? 'sync.problems_discard_unapplied_confirm'
        : 'sync.problems_discard_confirm';
    if (!confirm($_(ask))) return;
    void run(() => discardFailed(f.id));
  }

  const title = (f: FailedChange) =>
    $_(
      f.kind === 'unapplied'
        ? 'sync.problems_unapplied'
        : f.op === 2
          ? 'sync.problems_deleted'
          : 'sync.problems_changed',
      { values: { entity: $_(entityKey(f.entity)) } }
    );
</script>

<div class="problems">
  <h2>{$_('sync.problems_title')}</h2>
  {#if items.length === 0}
    <p class="help">{$_('sync.problems_none')}</p>
  {:else}
    <p class="help">{$_('sync.problems_help')}</p>
    <ul>
      {#each items as f (f.id)}
        <li>
          <div class="what">
            <strong>{title(f)}</strong>
            <span class="why" title={f.reason}>{$_(codeKey(f.code))}</span>
            <span class="when"
              >{formatDate(f.failedAt, $locale, { dateStyle: 'medium', timeStyle: 'short' })}</span
            >
          </div>
          <div class="actions">
            {#if f.kind === 'refused'}
              <button type="button" disabled={busy} onclick={() => retry(f.id)}
                >{$_('sync.problems_retry')}</button
              >
            {/if}
            <button type="button" class="danger" disabled={busy} onclick={() => discard(f)}
              >{$_('sync.problems_discard')}</button
            >
          </div>
        </li>
      {/each}
    </ul>
    {#if items.filter((f) => f.kind === 'refused').length > 1}
      <button type="button" disabled={busy} onclick={() => retry()}
        >{$_('sync.problems_retry_all')}</button
      >
    {/if}
  {/if}
</div>

<style>
  h2 {
    font-size: 1.1rem;
    margin-bottom: 6px;
  }
  .help {
    color: var(--text-soft);
    font-size: 0.88rem;
    margin-bottom: 10px;
  }
  ul {
    list-style: none;
    display: grid;
    gap: 8px;
    margin-bottom: 10px;
  }
  li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px 12px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
  }
  .what {
    display: grid;
    gap: 2px;
    min-width: 0;
    font-size: 0.9rem;
    color: var(--text);
  }
  .why {
    color: var(--danger);
  }
  .when {
    color: var(--text-soft);
    font-size: 0.78rem;
  }
  .actions {
    display: flex;
    gap: 8px;
  }
  button {
    font: inherit;
    font-size: 0.85rem;
    font-weight: 600;
    padding: 7px 12px;
    border-radius: 10px;
    border: 1px solid var(--border-strong);
    background: var(--surface);
    color: var(--text);
    cursor: pointer;
  }
  button:disabled {
    opacity: 0.6;
    cursor: default;
  }
  button.danger {
    color: var(--danger);
    border-color: var(--danger);
  }
</style>
