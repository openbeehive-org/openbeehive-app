<script lang="ts">
  import { _ } from 'svelte-i18n';
  import { MapPin } from '@lucide/svelte';
  import {
    apiaries,
    hives,
    queens,
    inspections,
    MARKING_COLORS,
    MARKING_NAMES
  } from '#lib/local/repo.js';
  import { createHive } from '#lib/local/history.js';
  import type { ApiaryRow, HiveRow, QueenRow } from '../rows.js';
  import { dataVersion, initialSyncPending } from '#lib/local/live.js';
  import Skeleton from '#lib/components/Skeleton.svelte';
  import Syncing from '#lib/components/Syncing.svelte';

  type HiveCard = HiveRow & { queen: QueenRow | undefined; lastInspection: string | undefined };
  let rows = $state<HiveCard[]>([]);
  let apiaryList = $state<ApiaryRow[]>([]);
  let apiaryName = $state<Record<string, string>>({});
  let loaded = $state(false);
  let showForm = $state(false);
  let name = $state('');
  let apiaryId = $state('');

  function daysAgo(iso?: string) {
    if (!iso) return -1;
    return Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000);
  }

  async function load() {
    apiaryList = (await apiaries.list()) as ApiaryRow[];
    apiaryName = Object.fromEntries(apiaryList.map((a) => [a.id, a.name]));
    const list = (await hives.list()) as HiveRow[];
    rows = await Promise.all(
      list.map(async (h) => {
        const q = await queens.current(h.id);
        const insp = await inspections.listByHive(h.id);
        return { ...h, queen: q, lastInspection: insp[0]?.date };
      })
    );
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

  // Run on mount and again whenever the sync engine applies pulled changes.
  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<div class="page">
  <header>
    <h1>{$_('hives.title')}</h1>
    {#if apiaryList.length}
      <button class="primary" onclick={() => (showForm = !showForm)}>+ {$_('common.new')}</button>
    {/if}
  </header>

  {#if showForm}
    <form
      class="card form"
      onsubmit={(e) => {
        e.preventDefault();
        create();
      }}
    >
      <input bind:value={name} placeholder={$_('hives.name_ph')} />
      <select bind:value={apiaryId}>
        {#each apiaryList as a}<option value={a.id}>{a.name}</option>{/each}
      </select>
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
  {:else if apiaryList.length === 0}
    <p class="muted empty">
      {$_('hives.need_apiary')} <a href="/apiaries">{$_('apiaries.new')}</a>
    </p>
  {:else if rows.length === 0}
    <p class="muted empty">{$_('hives.empty')}</p>
  {:else}
    <ul class="list">
      {#each rows as h}
        <li class="card">
          <a href={`/hives/${h.id}`}>
            <span class="thumb">
              {#if h.photo}<img src={h.photo} alt={h.name} />{:else}<img
                  class="ph"
                  src="/favicon.svg"
                  alt=""
                />{/if}
            </span>
            <div class="body">
              <strong>{h.name}</strong>
              <div class="meta">
                {#if apiaryName[h.apiary_id]}<span class="loc"
                    ><MapPin size={14} aria-hidden="true" /> {apiaryName[h.apiary_id]}</span
                  >{/if}
                <span class="badge status-{h.status}">{$_('hivestatus.' + (h.status ?? 0))}</span>
                {#if h.queen}
                  <span class="queen">
                    <span
                      class="dot"
                      style={`background:${MARKING_COLORS[h.queen.marking] ?? '#ccc'}`}
                      title={MARKING_NAMES[h.queen.marking] ?? ''}
                    ></span>
                    {h.queen.year}
                  </span>
                {/if}
                <span class="insp">
                  {daysAgo(h.lastInspection) < 0
                    ? $_('hives.never')
                    : $_('dashboard.days_ago', { values: { days: daysAgo(h.lastInspection) } })}
                </span>
              </div>
            </div>
            <span class="chev">›</span>
          </a>
        </li>
      {/each}
    </ul>
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
    color: var(--ink-soft);
  }
  .empty {
    text-align: center;
    padding: 40px 0;
  }
  .empty a {
    color: var(--honey-d);
    font-weight: 600;
  }
  .primary {
    background: var(--honey);
    color: #fff;
    border: none;
    border-radius: 11px;
    padding: 10px 16px;
    font-weight: 700;
    font-family: inherit;
    cursor: pointer;
    box-shadow: 0 4px 12px rgba(199, 127, 34, 0.28);
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: default;
    box-shadow: none;
  }
  .ghost {
    background: transparent;
    border: 1px solid var(--line);
    border-radius: 11px;
    padding: 10px 16px;
    font-weight: 600;
    font-family: inherit;
    cursor: pointer;
    color: var(--ink);
  }
  .card {
    background: var(--cream2);
    border: 1px solid var(--line);
    border-radius: 16px;
  }
  .form {
    padding: 16px;
    display: grid;
    gap: 10px;
    margin-bottom: 18px;
  }
  .form input,
  .form select {
    font: inherit;
    padding: 11px 13px;
    border: 1px solid var(--line);
    border-radius: 10px;
    background: #fff;
    color: var(--ink);
  }
  .form .actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
    margin-top: 4px;
  }
  .list {
    list-style: none;
    display: grid;
    gap: 12px;
  }
  .list li a {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 12px 14px;
    text-decoration: none;
    color: var(--ink);
  }
  .thumb {
    width: 54px;
    height: 54px;
    border-radius: 12px;
    overflow: hidden;
    flex-shrink: 0;
    background: linear-gradient(150deg, #fff5e3, #ffe9c6);
    display: grid;
    place-items: center;
    border: 1px solid var(--line);
  }
  .thumb img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .thumb .ph {
    width: 24px;
    height: 24px;
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .body strong {
    display: block;
    font-weight: 700;
    margin-bottom: 5px;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    font-size: 0.8rem;
    color: var(--ink-soft);
  }
  .meta .loc {
    font-weight: 600;
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }
  .badge {
    font-weight: 700;
    font-size: 0.72rem;
    padding: 3px 9px;
    border-radius: 999px;
    background: rgba(92, 107, 74, 0.15);
    color: #41502f;
  }
  .badge.status-3,
  .badge.status-4 {
    background: rgba(181, 64, 47, 0.13);
    color: #b5402f;
  }
  .queen {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-weight: 600;
  }
  .queen .dot {
    width: 11px;
    height: 11px;
    border-radius: 50%;
    border: 1px solid rgba(0, 0, 0, 0.18);
  }
  .chev {
    color: var(--ink-soft);
    font-size: 1.4rem;
  }
</style>
