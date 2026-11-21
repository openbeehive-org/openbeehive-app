<script lang="ts">
  import { _ } from 'svelte-i18n';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { apiaries, hives } from '#lib/local/repo.js';
  import { createHive } from '#lib/local/history.js';
  import type { ApiaryRow } from '../rows.js';
  import { dataVersion, initialSyncPending } from '#lib/local/live.js';
  import {
    filterHives,
    listStateFromParams,
    listStateToSearch,
    sortHives,
    NO_APIARY,
    type HiveOverview,
    type HiveSort
  } from '#lib/hivelist.js';
  import HiveCard from '#lib/components/HiveCard.svelte';
  import Skeleton from '#lib/components/Skeleton.svelte';
  import Syncing from '#lib/components/Syncing.svelte';

  let rows = $state<HiveOverview[]>([]);
  let apiaryList = $state<ApiaryRow[]>([]);
  let loaded = $state(false);
  // "/hives?new=1" (dashboard, first-run checklist) opens the form directly.
  let showForm = $state(page.url.searchParams.has('new'));
  let name = $state('');
  let apiaryId = $state('');

  // Search, filters and sort start from the URL and are written back to it
  // (without a new history entry), so going back from a hive restores them.
  const initial = listStateFromParams(page.url.searchParams);
  let query = $state(initial.query);
  let status = $state(initial.status);
  let apiary = $state(initial.apiary);
  let sort = $state<HiveSort>(initial.sort);
  // A real (replacing) navigation, not shallow routing: SvelteKit then keeps
  // the new URL in the history entry it returns to.
  $effect(() => {
    const search = listStateToSearch({ query, status, apiary, sort });
    if (search === location.search) return;
    void goto(`${location.pathname}${search}`, {
      replaceState: true,
      keepFocus: true,
      noScroll: true
    });
  });

  const STATUSES = ['1', '2', '3', '4', '5'];
  const hasOrphans = $derived(rows.some((h) => h.apiary_name === null));
  const filtering = $derived(!!(query.trim() || status || apiary));
  const shown = $derived(sortHives(filterHives(rows, { query, status, apiary }), sort));

  // One query for the whole list; it runs again when a sync batch lands.
  async function load() {
    apiaryList = (await apiaries.list()) as ApiaryRow[];
    rows = await hives.overview();
    const first = apiaryList[0];
    if (!apiaryId && first) apiaryId = first.id;
    loaded = true;
  }

  async function create() {
    if (!name.trim() || !apiaryId) return;
    await createHive({ name: name.trim(), apiary_id: apiaryId });
    name = '';
    showForm = false;
    await load();
  }

  function clearFilters() {
    query = '';
    status = '';
    apiary = '';
  }

  // Run on mount and again whenever the sync engine applies pulled changes.
  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<svelte:head><title>{$_('hives.title')}</title></svelte:head>

<div class="page">
  <header>
    <h1>{$_('hives.title')}</h1>
    {#if apiaryList.length}
      <button class="primary" aria-expanded={showForm} onclick={() => (showForm = !showForm)}
        >+ {$_('common.new')}</button
      >
    {/if}
  </header>

  {#if showForm && apiaryList.length}
    <form
      class="card form"
      onsubmit={(e) => {
        e.preventDefault();
        create();
      }}
    >
      <label class="field">
        <span>{$_('hives.name')}</span>
        <input bind:value={name} placeholder={$_('hives.name_ph')} />
      </label>
      <label class="field">
        <span>{$_('hivelist.apiary')}</span>
        <select bind:value={apiaryId}>
          {#each apiaryList as a}<option value={a.id}>{a.name}</option>{/each}
        </select>
      </label>
      <div class="actions">
        <button type="button" class="ghost" onclick={() => (showForm = false)}
          >{$_('common.cancel')}</button
        >
        <button type="submit" class="primary" disabled={!name.trim()}>{$_('common.save')}</button>
      </div>
    </form>
  {/if}

  {#if !loaded}
    <Skeleton n={3} height={80} />
  {:else if rows.length === 0 && $initialSyncPending}
    <Syncing />
  {:else if rows.length === 0 && apiaryList.length === 0}
    <p class="muted empty">
      {$_('hives.need_apiary')} <a href="/apiaries?new=1">{$_('apiaries.new')}</a>
    </p>
  {:else if rows.length === 0}
    <p class="muted empty">{$_('hives.empty')}</p>
  {:else}
    <div class="tools" role="search">
      <label class="field wide">
        <span>{$_('hivelist.search')}</span>
        <input type="search" bind:value={query} placeholder={$_('hivelist.search_ph')} />
      </label>
      <label class="field">
        <span>{$_('hives.status')}</span>
        <select bind:value={status}>
          <option value="">{$_('hivelist.all_statuses')}</option>
          {#each STATUSES as s}<option value={s}>{$_('hivestatus.' + s)}</option>{/each}
        </select>
      </label>
      <label class="field">
        <span>{$_('hivelist.apiary')}</span>
        <select bind:value={apiary}>
          <option value="">{$_('hivelist.all_apiaries')}</option>
          {#each apiaryList as a}<option value={a.id}>{a.name}</option>{/each}
          {#if hasOrphans}<option value={NO_APIARY}>{$_('hivelist.no_apiary')}</option>{/if}
        </select>
      </label>
      <label class="field">
        <span>{$_('hivelist.sort')}</span>
        <select bind:value={sort}>
          <option value="name">{$_('hivelist.sort_name')}</option>
          <option value="apiary">{$_('hivelist.sort_apiary')}</option>
          <option value="inspected">{$_('hivelist.sort_inspected')}</option>
        </select>
      </label>
    </div>

    <p class="count muted" role="status">
      {#if filtering}
        {$_('hivelist.shown', { values: { shown: shown.length, total: rows.length } })}
        <button class="link" onclick={clearFilters}>{$_('hivelist.clear')}</button>
      {:else}
        {$_('apiaries.hives_count', { values: { n: rows.length } })}
      {/if}
    </p>

    {#if shown.length === 0}
      <p class="muted empty">{$_('hivelist.no_match')}</p>
    {:else}
      <ul class="list">
        {#each shown as h (h.id)}
          <li><HiveCard hive={h} showApiary /></li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

<style>
  .page {
    max-width: 720px;
    margin: 0 auto;
    padding: 26px 18px 96px;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 18px;
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.9rem;
  }
  .muted {
    color: var(--text-soft);
  }
  .empty {
    text-align: center;
    padding: 40px 0;
  }
  .empty a {
    color: var(--accent-text);
    font-weight: 600;
  }
  .primary,
  .ghost {
    min-height: 44px;
    border-radius: 11px;
    padding: 10px 16px;
    font: inherit;
    font-weight: 700;
    cursor: pointer;
  }
  .primary {
    background: var(--accent);
    color: var(--on-accent);
    border: none;
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .ghost {
    background: transparent;
    border: 1px solid var(--border-strong);
    color: var(--text);
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 16px;
  }
  .form {
    padding: 16px;
    display: grid;
    gap: 12px;
    margin-bottom: 18px;
  }
  .field {
    display: grid;
    gap: 4px;
    min-width: 0;
  }
  .field span {
    font-size: 0.82rem;
    font-weight: 600;
    color: var(--text-soft);
  }
  .field input,
  .field select {
    font: inherit;
    width: 100%;
    min-height: 44px;
    padding: 10px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    background: var(--surface);
    color: var(--text);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
    margin-top: 4px;
  }
  .tools {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px;
    margin-bottom: 10px;
  }
  .tools .wide {
    grid-column: 1 / -1;
  }
  @media (max-width: 520px) {
    .tools {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .tools .field:last-child {
      grid-column: 1 / -1;
    }
  }
  .count {
    font-size: 0.85rem;
    margin-bottom: 10px;
    display: flex;
    align-items: center;
    gap: 6px;
    min-height: 44px;
  }
  .link {
    font: inherit;
    font-weight: 600;
    color: var(--accent-text);
    background: none;
    border: none;
    text-decoration: underline;
    cursor: pointer;
    min-height: 44px;
    padding: 0 8px;
  }
  .list {
    list-style: none;
    display: grid;
    gap: 12px;
  }
</style>
