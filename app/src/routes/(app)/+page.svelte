<script lang="ts">
  import { onMount } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { goto } from '$app/navigation';
  import { Check } from '@lucide/svelte';
  import { apiaries, hives, tasks as taskRepo } from '#lib/local/repo.js';
  import { getDB } from '#lib/local/db.js';
  import { honeyByYear } from '#lib/local/history.js';
  import { units } from '#lib/prefs.js';
  import { formatNumber, fromMetric, unitSymbol } from '#lib/units.js';
  import type { ApiaryRow, TaskRow } from './rows.js';
  import { dataVersion, initialSyncPending } from '#lib/local/live.js';
  import { formatDate, todayLocal } from '#lib/dates.js';
  import {
    DUE_AFTER_DAYS,
    dueHives,
    firstRunSteps,
    type DueHive,
    type FirstRunStep
  } from '#lib/hivelist.js';
  import { dueDay, isOverdue, splitTasks } from '#lib/tasklist.js';
  import { takeScanTarget } from '#lib/qr.js';
  import Skeleton from '#lib/components/Skeleton.svelte';
  import Syncing from '#lib/components/Syncing.svelte';

  // Local-first dashboard: every figure is read straight from the device DB.
  let stats = $state({
    apiaries: 0,
    hives: 0,
    active_queens: 0,
    open_tasks: 0,
    honey_kg_season: 0
  });
  // The dashboard shows the first few; "see all" leads to the full list.
  const DUE_SHOWN = 5;
  let due = $state<DueHive[]>([]);
  let dueTotal = $state(0);
  let tasks = $state<{ id: string; title: string; day: string; overdue: boolean }[]>([]);
  let checklist = $state<{ steps: FirstRunStep[]; complete: boolean }>({
    steps: [],
    complete: true
  });
  let loaded = $state(false);
  let menuOpen = $state(false);
  let menu = $state<HTMLElement>();

  // While the first download is still running, "nothing here" is not known
  // yet, so the newcomer greeting and checklist wait for it.
  const firstRun = $derived(loaded && !$initialSyncPending && !checklist.complete);

  async function load() {
    const db = await getDB();
    // The repo returns untyped rows; the shapes are those of rows.ts.
    const apiaryRows = (await apiaries.list()) as ApiaryRow[];
    // One query: every hive with its apiary name and last recorded visit.
    const hiveRows = await hives.overview();
    const queenRow = await db.get<{ n: number }>(
      `SELECT COUNT(*) AS n FROM queen WHERE deleted = 0 AND active = 1`
    );
    const year = new Date().getFullYear().toString();
    const honey = (await honeyByYear()).find((h) => h.year === year);

    stats = {
      apiaries: apiaryRows.length,
      hives: hiveRows.length,
      active_queens: queenRow?.n ?? 0,
      open_tasks: await taskRepo.openCount(),
      honey_kg_season: Number(honey?.kg ?? 0)
    };

    // Hives not visited for the due interval, longest first (sensor
    // readings do not count as a visit).
    const allDue = dueHives(hiveRows);
    due = allDue.slice(0, DUE_SHOWN);
    dueTotal = allDue.length;
    const visited = hiveRows.filter((h) => h.last_inspection);
    const firstHiveId = hiveRows[0]?.id;
    checklist = firstRunSteps({
      apiaries: apiaryRows.length,
      hives: hiveRows.length,
      visits: visited.length,
      ...(firstHiveId ? { firstHiveId } : {})
    });

    const today = todayLocal();
    tasks = splitTasks((await taskRepo.list()) as TaskRow[])
      .open.slice(0, 5)
      .map((t) => ({
        id: t.id,
        title: t.title,
        day: dueDay(t.due_at),
        overdue: isOverdue(t.due_at, today)
      }));
    loaded = true;
  }

  onMount(() => {
    // A scanned label led through the sign-in page: open that hive now.
    const scanned = takeScanTarget();
    if (scanned) void goto(`/h/${scanned}`);
  });

  function closeMenuOutside(e: MouseEvent) {
    if (menuOpen && menu && !menu.contains(e.target as Node)) menuOpen = false;
  }
  function closeMenuOnEscape(e: KeyboardEvent) {
    if (e.key === 'Escape') menuOpen = false;
  }

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<svelte:window onclick={closeMenuOutside} onkeydown={closeMenuOnEscape} />

<div class="page">
  <header>
    <div>
      <h1>{firstRun ? $_('dashboard.welcome_first') : $_('dashboard.welcome')}</h1>
      <p>{firstRun ? $_('dashboard.subtitle_first') : $_('dashboard.subtitle')}</p>
    </div>
    <div class="new" bind:this={menu}>
      <button
        class="primary"
        aria-haspopup="true"
        aria-expanded={menuOpen}
        aria-controls="new-menu"
        onclick={() => (menuOpen = !menuOpen)}>+ {$_('common.new')}</button
      >
      {#if menuOpen}
        <ul id="new-menu" class="menu">
          <li><a href="/apiaries?new=1">{$_('dashboard.new_apiary')}</a></li>
          <li><a href="/hives?new=1">{$_('dashboard.new_hive')}</a></li>
          <li><a href="/tasks?new=1">{$_('dashboard.new_task')}</a></li>
        </ul>
      {/if}
    </div>
  </header>

  {#if firstRun}
    <section class="panel start" aria-labelledby="start-title">
      <h2 id="start-title">{$_('dashboard.start_title')}</h2>
      <ol class="steps">
        {#each checklist.steps as step, i (step.key)}
          <li class:done={step.done} class:current={step.current}>
            <span class="mark" aria-hidden="true">
              {#if step.done}<Check size={16} />{:else}{i + 1}{/if}
            </span>
            {#if step.current}
              <a class="what" href={step.href}>
                <strong>{$_('dashboard.step_' + step.key)}</strong>
                <small>{$_('dashboard.step_' + step.key + '_hint')}</small>
              </a>
            {:else}
              <div class="what">
                <strong>{$_('dashboard.step_' + step.key)}</strong>
                {#if step.done}
                  <span class="sr">({$_('dashboard.step_done')})</span>
                {:else}
                  <small>{$_('dashboard.step_' + step.key + '_hint')}</small>
                {/if}
              </div>
            {/if}
          </li>
        {/each}
      </ol>
    </section>
  {/if}

  <div class="stats">
    {#each [['stat_apiaries', stats.apiaries], ['stat_hives', stats.hives], ['stat_queens', stats.active_queens], ['stat_open', stats.open_tasks]] as [key, val]}
      <div class="stat">
        <span class="n"
          >{#if loaded}{val}{:else}<span class="n-skl" aria-hidden="true"></span>{/if}</span
        >
        <span class="l">{$_('dashboard.' + key)}</span>
      </div>
    {/each}
    <div class="stat honey">
      <span class="n">
        {#if loaded}{formatNumber(
            fromMetric('weight', stats.honey_kg_season, $units),
            $locale,
            0
          )}<small> {unitSymbol('weight', $units)}</small>{:else}<span
            class="n-skl"
            aria-hidden="true"
          ></span>{/if}
      </span>
      <span class="l">{$_('dashboard.honey_season')}</span>
    </div>
  </div>

  <div class="cols">
    <section class="panel" aria-labelledby="due-title">
      <h2 id="due-title">{$_('dashboard.due')}</h2>
      {#if loaded && stats.hives > 0}
        <p class="hint">{$_('dashboard.due_help', { values: { days: DUE_AFTER_DAYS } })}</p>
      {/if}
      {#if !loaded}
        <Skeleton n={3} height={44} />
      {:else if due.length === 0}
        {#if $initialSyncPending}<Syncing />{:else}<p class="muted">
            {stats.hives === 0 ? $_('dashboard.due_none') : $_('dashboard.all_current')}
          </p>{/if}
      {/if}
      {#each due as f (f.id)}
        <a class="row" href={`/hives/${f.id}`}>
          <div><strong>{f.name}</strong><small>{f.apiary ?? ''}</small></div>
          <span class="badge" class:warn={f.days === null || f.days >= DUE_AFTER_DAYS}>
            {f.days === null
              ? $_('dashboard.never')
              : f.days === 0
                ? $_('dashboard.today')
                : $_('dashboard.days_ago', { values: { days: f.days } })}
          </span>
        </a>
      {/each}
      {#if dueTotal > due.length}
        <a class="more" href="/hives?sort=inspected"
          >{$_('dashboard.see_all_due', { values: { n: dueTotal } })} ›</a
        >
      {/if}
    </section>

    <section class="panel">
      <h2>{$_('dashboard.next_tasks')}</h2>
      {#if !loaded}
        <Skeleton n={3} height={44} />
      {:else if tasks.length === 0}
        {#if $initialSyncPending}<Syncing />{:else}<p class="muted">{$_('tasks.empty')}</p>{/if}
      {/if}
      {#each tasks as a (a.id)}
        <a class="row" href={`/tasks#task-${a.id}`}>
          <div>
            <strong>{a.title}</strong>
            {#if a.day}<small>{$_('tasks.due')}: {formatDate(a.day, $locale)}</small>{/if}
          </div>
          {#if a.overdue}<span class="badge warn">{$_('tasks.overdue')}</span>{/if}
        </a>
      {/each}
    </section>
  </div>
</div>

<style>
  .page {
    max-width: 1080px;
    margin: 0 auto;
    padding: 32px 20px 80px;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
    margin-bottom: 24px;
    gap: 12px;
  }
  header h1 {
    font-size: 2rem;
  }
  header p {
    color: var(--text-soft);
  }
  .new {
    position: relative;
    flex-shrink: 0;
  }
  .primary {
    min-height: 44px;
    background: var(--accent);
    color: var(--on-accent);
    border: none;
    border-radius: 11px;
    padding: 11px 18px;
    font: inherit;
    font-weight: 700;
    cursor: pointer;
    white-space: nowrap;
  }
  .menu {
    position: absolute;
    right: 0;
    top: calc(100% + 6px);
    z-index: 20;
    list-style: none;
    min-width: 200px;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    padding: 6px;
  }
  .menu a {
    display: flex;
    align-items: center;
    min-height: 44px;
    padding: 0 12px;
    border-radius: 8px;
    color: var(--text);
    font-weight: 600;
    text-decoration: none;
    white-space: nowrap;
  }
  .menu a:hover,
  .menu a:focus-visible {
    background: var(--warn-bg);
    color: var(--warn-text);
  }
  .muted {
    color: var(--text-soft);
    font-size: 0.9rem;
    padding: 6px 0;
  }
  .hint {
    color: var(--text-soft);
    font-size: 0.85rem;
    margin: -4px 0 8px;
  }
  .more {
    display: flex;
    align-items: center;
    min-height: 44px;
    color: var(--accent-text);
    font-weight: 600;
    text-decoration: none;
  }

  .start {
    margin-bottom: 22px;
  }
  .steps {
    list-style: none;
    display: grid;
    gap: 4px;
  }
  .steps li {
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 52px;
    color: var(--text-soft);
  }
  .steps .mark {
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    flex-shrink: 0;
    border-radius: 50%;
    border: 2px solid var(--border-strong);
    font-weight: 700;
    font-size: 0.85rem;
  }
  .steps .done .mark {
    background: var(--ok);
    border-color: var(--ok);
    color: var(--on-accent);
  }
  .steps .current .mark {
    border-color: var(--accent);
    color: var(--accent-text);
  }
  .steps .done strong {
    text-decoration: line-through;
    font-weight: 600;
  }
  .steps .what {
    flex: 1;
    display: flex;
    flex-direction: column;
    justify-content: center;
    min-height: 44px;
    color: inherit;
    text-decoration: none;
  }
  .steps a.what strong {
    color: var(--accent-text);
    text-decoration: underline;
  }
  .steps small {
    font-size: 0.82rem;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }

  .stats {
    display: grid;
    gap: 14px;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
    margin-bottom: 22px;
  }
  .stat {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 16px;
    padding: 18px;
  }
  .stat.honey {
    background: var(--warn-bg);
    color: var(--warn-text);
    border-color: transparent;
  }
  .stat.honey .l {
    color: inherit;
  }
  .stat .n {
    font-family: 'Fraunces', serif;
    font-size: 2.2rem;
    font-weight: 600;
    display: block;
    line-height: 1;
  }
  .stat .l {
    color: var(--text-soft);
    font-weight: 600;
    font-size: 0.85rem;
  }
  /* Shimmer placeholder for a stat number while the local read runs: a hard 0 would claim "you have nothing" before it is known. */
  .stat .n-skl {
    display: inline-block;
    width: 52px;
    height: 0.82em;
    border-radius: 8px;
    background: linear-gradient(
      100deg,
      color-mix(in srgb, var(--accent) 10%, transparent) 40%,
      color-mix(in srgb, var(--accent) 22%, transparent) 50%,
      color-mix(in srgb, var(--accent) 10%, transparent) 60%
    );
    background-size: 200% 100%;
    animation: shimmer 1.4s ease-in-out infinite;
  }
  @keyframes shimmer {
    from {
      background-position: 200% 0;
    }
    to {
      background-position: -200% 0;
    }
  }

  .cols {
    display: grid;
    gap: 16px;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  }
  .panel {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 18px;
    padding: 20px;
  }
  .panel h2 {
    font-size: 1.2rem;
    margin-bottom: 14px;
  }
  .row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 10px;
    min-height: 44px;
    padding: 12px 0;
    border-top: 1px solid var(--border);
    text-decoration: none;
    color: inherit;
  }
  .row:first-of-type {
    border-top: none;
  }
  .row strong {
    transition: color 0.12s;
    overflow-wrap: anywhere;
  }
  .row small {
    display: block;
    color: var(--text-soft);
    font-size: 0.82rem;
  }
  .row:hover strong {
    color: var(--accent-text);
  }
  .badge {
    background: color-mix(in srgb, var(--ok) 16%, transparent);
    color: var(--text);
    font-weight: 700;
    font-size: 0.78rem;
    padding: 4px 10px;
    border-radius: 999px;
    white-space: nowrap;
  }
  .badge.warn {
    background: color-mix(in srgb, var(--danger) 13%, transparent);
    color: var(--danger);
  }
</style>
