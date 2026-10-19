<script lang="ts">
  import { _ } from 'svelte-i18n';
  import { apiaries, hives, tasks as taskRepo } from '#lib/local/repo.js';
  import { getDB } from '#lib/local/db.js';
  import { honeyByYear } from '#lib/local/history.js';
  import { dataVersion, initialSyncPending } from '#lib/local/live.js';
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
  let due = $state<{ id: string; name: string; apiary: string; days: number }[]>([]);
  let tasks = $state<{ title: string; due: string; overdue: boolean }[]>([]);
  let loaded = $state(false);

  function daysSince(iso?: string): number {
    if (!iso) return -1;
    return Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000);
  }

  async function load() {
    const db = await getDB();
    const apiaryRows = await apiaries.list();
    const hiveRows = await hives.list();
    const queenRow = await db.get<{ n: number }>(
      `SELECT COUNT(*) AS n FROM queen WHERE deleted = 0 AND active = 1`
    );
    const year = new Date().getFullYear().toString();
    const honey = (await honeyByYear()).find((h: any) => h.year === year) as any;

    stats = {
      apiaries: apiaryRows.length,
      hives: hiveRows.length,
      active_queens: queenRow?.n ?? 0,
      open_tasks: await taskRepo.openCount(),
      honey_kg_season: Math.round(honey?.kg ?? 0)
    };

    const apiaryName: Record<string, string> = Object.fromEntries(
      apiaryRows.map((a: any) => [a.id, a.name])
    );
    // Hives sorted by how long since their last inspection (oldest first).
    const withLast = await Promise.all(
      hiveRows.map(async (h: any) => {
        const last = await db.get<{ date: string }>(
          `SELECT MAX(date) AS date FROM inspection WHERE deleted = 0 AND hive_id = ?`,
          [h.id]
        );
        return {
          id: h.id,
          name: h.name,
          apiary: apiaryName[h.apiary_id] ?? '',
          days: daysSince(last?.date)
        };
      })
    );
    due = withLast.sort((a, b) => (b.days < 0 ? 1 : a.days < 0 ? -1 : b.days - a.days)).slice(0, 5);

    const today = new Date().toISOString().slice(0, 10);
    tasks = (await taskRepo.list())
      .filter((t: any) => !t.done)
      .slice(0, 5)
      .map((t: any) => ({
        title: t.title,
        due: t.due_at || '',
        overdue: !!t.due_at && t.due_at.slice(0, 10) < today
      }));
    loaded = true;
  }

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<div class="page">
  <header>
    <div>
      <h1>{$_('dashboard.welcome')}</h1>
      <p>{$_('dashboard.subtitle')}</p>
    </div>
    <a class="primary" href="/apiaries">+ {$_('common.new')}</a>
  </header>

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
        {#if loaded}{stats.honey_kg_season}<small> {$_('common.kg')}</small>{:else}<span
            class="n-skl"
            aria-hidden="true"
          ></span>{/if}
      </span>
      <span class="l">{$_('dashboard.honey_season')}</span>
    </div>
  </div>

  <div class="cols">
    <section class="panel">
      <h2>{$_('dashboard.due')}</h2>
      {#if !loaded}
        <Skeleton n={3} height={44} />
      {:else if due.length === 0}
        {#if $initialSyncPending}<Syncing />{:else}<p class="muted">
            {$_('dashboard.all_current')}
          </p>{/if}
      {/if}
      {#each due as f}
        <a class="row" href={`/hives/${f.id}`}>
          <div><strong>{f.name}</strong><small>{f.apiary}</small></div>
          <span class="badge" class:warn={f.days < 0 || f.days >= 21}>
            {f.days < 0
              ? $_('dashboard.never')
              : $_('dashboard.days_ago', { values: { days: f.days } })}
          </span>
        </a>
      {/each}
    </section>

    <section class="panel">
      <h2>{$_('dashboard.next_tasks')}</h2>
      {#if !loaded}
        <Skeleton n={3} height={44} />
      {:else if tasks.length === 0}
        {#if $initialSyncPending}<Syncing />{:else}<p class="muted">{$_('tasks.empty')}</p>{/if}
      {/if}
      {#each tasks as a}
        <div class="row">
          <div>
            <strong>{a.title}</strong>{#if a.due}<small>{a.due}</small>{/if}
          </div>
          {#if a.overdue}<span class="badge warn">!</span>{/if}
        </div>
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
    color: var(--ink-soft);
  }
  .primary {
    background: var(--honey);
    color: #fff;
    border: none;
    border-radius: 11px;
    padding: 11px 18px;
    font-weight: 700;
    font-family: inherit;
    cursor: pointer;
    box-shadow: 0 4px 12px rgba(199, 127, 34, 0.3);
    text-decoration: none;
    display: inline-block;
  }
  .muted {
    color: var(--ink-soft);
    font-size: 0.9rem;
    padding: 6px 0;
  }

  .stats {
    display: grid;
    gap: 14px;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
    margin-bottom: 22px;
  }
  .stat {
    background: var(--cream2);
    border: 1px solid var(--line);
    border-radius: 16px;
    padding: 18px;
  }
  .stat.honey {
    background: linear-gradient(150deg, #fff5e3, #ffe9c6);
    border-color: #eecf9a;
  }
  .stat .n {
    font-family: 'Fraunces', serif;
    font-size: 2.2rem;
    font-weight: 600;
    display: block;
    line-height: 1;
  }
  .stat .l {
    color: var(--ink-soft);
    font-weight: 600;
    font-size: 0.85rem;
  }
  /* Shimmer placeholder in place of a stat number while the local read runs —
     rendering a hard 0 would claim "you have nothing" before it is known. */
  .stat .n-skl {
    display: inline-block;
    width: 52px;
    height: 0.82em;
    border-radius: 8px;
    background: linear-gradient(
      100deg,
      rgba(199, 127, 34, 0.1) 40%,
      rgba(199, 127, 34, 0.22) 50%,
      rgba(199, 127, 34, 0.1) 60%
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
    background: var(--cream2);
    border: 1px solid var(--line);
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
    padding: 12px 0;
    border-top: 1px solid var(--line);
  }
  .row:first-of-type {
    border-top: none;
  }
  .row small {
    display: block;
    color: var(--ink-soft);
    font-size: 0.82rem;
  }
  a.row {
    text-decoration: none;
    color: inherit;
  }
  a.row:hover strong {
    color: var(--honey-d);
  }
  a.row strong {
    transition: color 0.12s;
  }
  .badge {
    background: rgba(92, 107, 74, 0.15);
    color: #41502f;
    font-weight: 700;
    font-size: 0.78rem;
    padding: 4px 10px;
    border-radius: 999px;
  }
  .badge.warn {
    background: rgba(181, 64, 47, 0.13);
    color: #b5402f;
  }
</style>
