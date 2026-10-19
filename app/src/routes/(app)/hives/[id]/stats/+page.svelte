<script lang="ts">
  import { page } from '$app/state';
  import { _ } from 'svelte-i18n';
  import { hives, inspections, harvests } from '#lib/local/repo.js';
  import { dataVersion } from '#lib/local/live.js';
  import Sparkline from '#lib/components/Sparkline.svelte';

  const id = page.params.id ?? '';
  let hive = $state<any>(null);
  let visits = $state<any[]>([]); // oldest → newest
  let harvestRows = $state<any[]>([]);
  let loaded = $state(false);

  // Numeric metrics charted over the inspection history. A value of 0 means
  // "not recorded" for these fields, so we plot only recorded points.
  const METRICS: { key: string; label: string; unit?: string; parse?: boolean }[] = [
    { key: 'frames', label: 'insp.frames' },
    { key: 'brood_frames', label: 'insp.brood_frames' },
    { key: 'stores', label: 'insp.stores' },
    { key: 'queen_cells', label: 'insp.queen_cells' },
    { key: 'temperament', label: 'insp.temperament' },
    { key: 'calmness', label: 'insp.calmness' },
    { key: 'varroa', label: 'insp.varroa', parse: true },
    { key: 'weight_kg', label: 'insp.weight_kg', unit: 'kg' },
    { key: 'fed_kg', label: 'insp.fed_kg', unit: 'kg' },
    { key: 'youngest_larva', label: 'insp.youngest_larva', unit: 'd' },
    { key: 'temp_hive', label: 'insp.temp_hive', unit: '°C' },
    { key: 'temp_outside', label: 'insp.temp_outside', unit: '°C' },
    { key: 'humidity_hive', label: 'insp.humidity_hive', unit: '%' },
    { key: 'humidity_outside', label: 'insp.humidity_outside', unit: '%' }
  ];

  const BOOLEANS = ['queen_seen', 'eggs_seen', 'covered_larva', 'drone_frame_cut', 'super_added'];

  function seriesFor(m: { key: string; parse?: boolean }) {
    return visits
      .map((v) => ({ date: v.date, value: m.parse ? parseFloat(v[m.key]) : Number(v[m.key]) }))
      .filter((s) => !isNaN(s.value) && s.value > 0);
  }

  function trend(vals: number[]): '' | 'up' | 'down' {
    if (vals.length < 2) return '';
    const d = (vals[vals.length - 1] ?? 0) - (vals[0] ?? 0);
    return d > 0 ? 'up' : d < 0 ? 'down' : '';
  }

  const metricViews = $derived(
    METRICS.map((m) => {
      const s = seriesFor(m);
      const vals = s.map((p) => p.value);
      return { ...m, series: s, vals, latest: vals[vals.length - 1], trend: trend(vals) };
    }).filter((m) => m.vals.length > 0)
  );

  const honeyView = $derived.by(() => {
    const s = harvestRows
      .map((h) => ({ date: h.date, value: h.amount_kg }))
      .filter((p) => p.value > 0);
    return { series: s, vals: s.map((p) => p.value), total: s.reduce((a, b) => a + b.value, 0) };
  });

  const boolViews = $derived(
    BOOLEANS.map((key) => ({
      key,
      seen: visits.filter((v) => v[key]).length,
      total: visits.length,
      dots: visits.map((v) => !!v[key])
    })).filter((b) => b.seen > 0)
  );

  async function load() {
    hive = await hives.get(id);
    if (hive) {
      const insp = await inspections.listByHive(id); // newest first
      visits = [...insp].reverse(); // oldest → newest for charts
      harvestRows = [...(await harvests.listByHive(id))].reverse();
    }
    loaded = true;
  }

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<div class="page">
  <a class="back" href={`/hives/${id}`}>‹ {hive?.name ?? $_('hives.title')}</a>

  {#if !loaded}
    <p class="muted">…</p>
  {:else if !hive}
    <p class="muted">{$_('hives.not_found')}</p>
  {:else}
    <h1>{$_('stats.title')}</h1>
    <p class="muted sub">{hive.name} · {visits.length} {$_('stats.visits')}</p>

    {#if metricViews.length === 0 && honeyView.series.length === 0 && boolViews.length === 0}
      <p class="muted empty">{$_('stats.no_data')}</p>
    {/if}

    {#each metricViews as m}
      <section id={m.key} class="card metric">
        <div class="m-head">
          <h2>{$_(m.label)}</h2>
          <div class="latest">
            <strong>{m.latest}{m.unit ? ' ' + m.unit : ''}</strong>
            {#if m.trend === 'up'}<span class="tr up">▲</span>{:else if m.trend === 'down'}<span
                class="tr down">▼</span
              >{/if}
          </div>
        </div>
        <Sparkline values={m.vals} />
        <div class="mm">
          <span>min {Math.min(...m.vals)}</span>
          <span>max {Math.max(...m.vals)}</span>
          <span>{m.vals.length} {$_('stats.points')}</span>
        </div>
      </section>
    {/each}

    {#if honeyView.series.length}
      <section id="honey" class="card metric">
        <div class="m-head">
          <h2>{$_('harvest.title')}</h2>
          <div class="latest"><strong>{honeyView.total} kg</strong></div>
        </div>
        <Sparkline values={honeyView.vals} color="#a4641a" />
        <div class="mm"><span>{honeyView.series.length} {$_('stats.harvests')}</span></div>
      </section>
    {/if}

    {#if boolViews.length}
      <section class="card">
        <h2>{$_('stats.observations')}</h2>
        {#each boolViews as b}
          <div class="obs">
            <span class="obs-label">{$_('insp.' + b.key)}</span>
            <span class="dots">
              {#each b.dots as on}<span class="d" class:on></span>{/each}
            </span>
            <span class="count">{b.seen}/{b.total}</span>
          </div>
        {/each}
      </section>
    {/if}
  {/if}
</div>

<style>
  .page {
    max-width: 720px;
    margin: 0 auto;
    padding: 22px 18px 96px;
  }
  .back {
    color: var(--ink-soft);
    text-decoration: none;
    font-weight: 600;
    font-size: 0.9rem;
  }
  .muted {
    color: var(--ink-soft);
  }
  .sub {
    margin-bottom: 18px;
  }
  .empty {
    text-align: center;
    padding: 40px 0;
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.9rem;
    margin: 12px 0 2px;
  }
  h2 {
    font-family: 'Fraunces', serif;
    font-size: 1.15rem;
  }

  .card {
    background: var(--cream2);
    border: 1px solid var(--line);
    border-radius: 16px;
    padding: 16px 18px;
    margin-bottom: 14px;
  }
  .metric {
    scroll-margin-top: 80px;
  }
  .m-head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    margin-bottom: 8px;
  }
  .latest {
    display: flex;
    align-items: center;
    gap: 7px;
  }
  .latest strong {
    font-family: 'Fraunces', serif;
    font-size: 1.4rem;
  }
  .tr.up {
    color: #b5402f;
  }
  .tr.down {
    color: #41502f;
  }
  .tr {
    font-size: 0.8rem;
  }
  .mm {
    display: flex;
    gap: 14px;
    color: var(--ink-soft);
    font-size: 0.76rem;
    margin-top: 8px;
  }

  .obs {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 9px 0;
    border-top: 1px solid var(--line);
  }
  .obs:first-of-type {
    border-top: none;
  }
  .obs-label {
    flex: 1;
    font-weight: 600;
    font-size: 0.9rem;
  }
  .dots {
    display: flex;
    gap: 3px;
    flex-wrap: wrap;
    max-width: 45%;
  }
  .dots .d {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    border: 1px solid var(--line);
    background: #fff;
  }
  .dots .d.on {
    background: var(--honey);
    border-color: var(--honey);
  }
  .count {
    color: var(--ink-soft);
    font-size: 0.8rem;
    font-weight: 600;
    min-width: 38px;
    text-align: right;
  }
</style>
