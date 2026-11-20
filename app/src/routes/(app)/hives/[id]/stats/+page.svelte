<script lang="ts">
  import { page } from '$app/state';
  import { _, locale } from 'svelte-i18n';
  import { TrendingDown, TrendingUp } from '@lucide/svelte';
  import { hives, inspections, harvests } from '#lib/local/repo.js';
  import type { HarvestRow, HiveRow, InspectionRow } from '../../../rows.js';
  import { dataVersion } from '#lib/local/live.js';
  import { formatDate } from '#lib/dates.js';
  import {
    ORDINALS,
    isSensor,
    levelSeries,
    numericSeries,
    varroaSeries,
    type NumericKey,
    type OrdinalKey,
    type Point
  } from '#lib/components/hive/readings.js';
  import Sparkline from '#lib/components/Sparkline.svelte';
  import LevelChart from '#lib/components/hive/LevelChart.svelte';
  import { dotted } from '#lib/components/hive/forms.js';
  import { timeOf } from '#lib/components/hive/chartScale.js';
  import '#lib/components/hive/hive.css';

  const id = page.params.id ?? '';
  let hive = $state<HiveRow | null>(null);
  // Visits and sensor readings together, oldest to newest.
  let readings = $state<InspectionRow[]>([]);
  let harvestRows = $state<HarvestRow[]>([]);
  let loaded = $state(false);

  // What is charted over the inspection history, in card order. Numbers are
  // plotted as recorded (empty = no point, 0 and frost are real points);
  // ratings as named levels; varroa only where a plain number was entered.
  type Metric =
    | { kind: 'number'; key: NumericKey; unit?: 'kg' | 'd' | '°C' | '%' }
    | { kind: 'level'; key: OrdinalKey }
    | { kind: 'varroa'; key: 'varroa' };
  const METRICS: Metric[] = [
    { kind: 'number', key: 'frames' },
    { kind: 'number', key: 'brood_frames' },
    { kind: 'level', key: 'stores' },
    { kind: 'number', key: 'queen_cells' },
    { kind: 'level', key: 'temperament' },
    { kind: 'level', key: 'calmness' },
    { kind: 'varroa', key: 'varroa' },
    { kind: 'number', key: 'weight_kg', unit: 'kg' },
    { kind: 'number', key: 'fed_kg', unit: 'kg' },
    { kind: 'number', key: 'youngest_larva', unit: 'd' },
    { kind: 'number', key: 'temp_hive', unit: '°C' },
    { kind: 'number', key: 'temp_outside', unit: '°C' },
    { kind: 'number', key: 'humidity_hive', unit: '%' },
    { kind: 'number', key: 'humidity_outside', unit: '%' }
  ];

  const BOOLEANS: (keyof InspectionRow)[] = [
    'queen_seen',
    'eggs_seen',
    'covered_larva',
    'drone_frame_cut',
    'super_added'
  ];

  const day = (iso: string) => formatDate(iso, $locale);
  const unitText = (unit?: string) =>
    unit === 'kg' ? $_('common.kg') : unit === 'd' ? $_('insp.unit_d') : (unit ?? '');
  const withUnit = (v: number | string, unit?: string) =>
    unit ? `${v} ${unitText(unit)}` : `${v}`;
  const levelName = (key: OrdinalKey, code: number) => $_(`${ORDINALS[key].ns}.${code}`);

  function trend(vals: number[]): '' | 'up' | 'down' {
    if (vals.length < 2) return '';
    const d = (vals[vals.length - 1] ?? 0) - (vals[0] ?? 0);
    return d > 0 ? 'up' : d < 0 ? 'down' : '';
  }
  const span = (pts: Point[]) => {
    const first = pts[0];
    const last = pts[pts.length - 1];
    if (!first || !last) return '';
    return first.date === last.date ? day(first.date) : `${day(first.date)} – ${day(last.date)}`;
  };

  // One view per metric that has at least one recorded point.
  const metricViews = $derived.by(() => {
    const out = [];
    for (const m of METRICS) {
      const label = $_('insp.' + m.key);
      if (m.kind === 'level') {
        const pts = levelSeries(readings, m.key);
        const last = pts[pts.length - 1];
        if (!last) continue;
        out.push({
          kind: m.kind,
          key: m.key,
          label,
          count: pts.length,
          span: span(pts),
          times: pts.map((p) => timeOf(p.date)),
          latest: levelName(m.key, last.value),
          levels: ORDINALS[m.key].bestFirst.map((code) => ({
            code,
            label: levelName(m.key, code)
          })),
          points: pts.map((p) => ({
            value: p.value,
            title: `${day(p.date)}: ${levelName(m.key, p.value)}`
          }))
        });
        continue;
      }
      const varroa = m.kind === 'varroa' ? varroaSeries(readings) : null;
      const pts = varroa ? varroa.points : numericSeries(readings, m.key as NumericKey);
      const last = pts[pts.length - 1];
      if (!last) continue;
      const unit = m.kind === 'number' ? m.unit : undefined;
      const vals = pts.map((p) => p.value);
      // Varroa is shown the way it was typed in.
      const shown = (i: number) => varroa?.points[i]?.entered ?? withUnit(vals[i] ?? 0, unit);
      out.push({
        kind: 'number' as const,
        key: m.key,
        label,
        count: pts.length,
        span: span(pts),
        times: pts.map((p) => timeOf(p.date)),
        latest: shown(pts.length - 1),
        vals,
        titles: pts.map((p, i) => `${day(p.date)}: ${shown(i)}`),
        min: withUnit(Math.min(...vals), unit),
        max: withUnit(Math.max(...vals), unit),
        trend: trend(vals),
        skipped: varroa?.skipped ?? 0
      });
    }
    return out;
  });

  const honeyView = $derived.by(() => {
    const s = harvestRows.filter((h) => h.amount_kg > 0);
    const total = Math.round(s.reduce((a, h) => a + h.amount_kg, 0) * 100) / 100;
    return {
      vals: s.map((h) => h.amount_kg),
      times: s.map((h) => timeOf(h.date)),
      titles: s.map((h) => `${day(h.date)}: ${withUnit(h.amount_kg, 'kg')}`),
      total
    };
  });

  // Observations exist only on visits; a sensor never sees the queen.
  const visits = $derived(readings.filter((r) => !isSensor(r)));
  const sensorCount = $derived(readings.length - visits.length);
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
      const insp = await inspections.listByHive(id, { sensors: true }); // newest first
      readings = [...insp].reverse();
      harvestRows = [...(await harvests.listByHive(id))].reverse();
    }
    loaded = true;
  }

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<div class="hv-page">
  <a class="hv-back" href={`/hives/${id}`}>‹ {hive?.name ?? $_('hives.title')}</a>

  {#if !loaded}
    <p class="hv-muted">…</p>
  {:else if !hive}
    <p class="hv-muted">{$_('hives.not_found')}</p>
  {:else}
    <h1>{$_('stats.title')}</h1>
    <p class="hv-sub">
      {dotted(
        hive.name,
        `${visits.length} ${$_('stats.visits')}`,
        sensorCount ? `${sensorCount} ${$_('stats.sensor_readings')}` : ''
      )}
    </p>

    {#if metricViews.length === 0 && honeyView.vals.length === 0 && boolViews.length === 0}
      <p class="hv-empty">{$_('stats.no_data')}</p>
    {/if}

    {#each metricViews as m (m.key)}
      <section id={m.key} class="hv-card metric">
        <div class="m-head">
          <h2>{m.label}</h2>
          <div class="latest">
            <strong>{m.latest}</strong>
            {#if m.kind === 'number' && m.trend}
              <span class="tr">
                {#if m.trend === 'up'}
                  <TrendingUp size={18} aria-hidden="true" />
                {:else}
                  <TrendingDown size={18} aria-hidden="true" />
                {/if}
                <span class="hv-sr">{$_('stats.trend_' + m.trend)}</span>
              </span>
            {/if}
          </div>
        </div>
        {#if m.kind === 'level'}
          <LevelChart
            levels={m.levels}
            points={m.points}
            times={m.times}
            label={`${m.label}: ${m.latest}`}
          />
        {:else}
          <Sparkline
            values={m.vals}
            times={m.times}
            titles={m.titles}
            label={`${m.label}: ${m.latest}`}
          />
        {/if}
        <div class="mm">
          {#if m.kind === 'number'}
            <span>{$_('stats.min')} {m.min}</span>
            <span>{$_('stats.max')} {m.max}</span>
          {/if}
          <span>{m.count} {$_('stats.points')}</span>
          <span>{m.span}</span>
        </div>
        {#if m.kind === 'number' && m.skipped}
          <p class="mm">{$_('stats.varroa_skipped', { values: { n: m.skipped } })}</p>
        {/if}
      </section>
    {/each}

    {#if honeyView.vals.length}
      <section id="honey" class="hv-card metric">
        <div class="m-head">
          <h2>{$_('harvest.title')}</h2>
          <div class="latest"><strong>{withUnit(honeyView.total, 'kg')}</strong></div>
        </div>
        <Sparkline
          values={honeyView.vals}
          times={honeyView.times}
          titles={honeyView.titles}
          label={`${$_('harvest.title')}: ${withUnit(honeyView.total, 'kg')}`}
        />
        <div class="mm"><span>{honeyView.vals.length} {$_('stats.harvests')}</span></div>
      </section>
    {/if}

    {#if boolViews.length}
      <section class="hv-card metric">
        <h2>{$_('stats.observations')}</h2>
        {#each boolViews as b}
          <div class="obs">
            <span class="obs-label">{$_('insp.' + b.key)}</span>
            <span class="dots" aria-hidden="true">
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
  h1 {
    margin: 4px 0 2px;
  }
  .metric {
    padding: 16px 18px;
    margin-bottom: 14px;
    scroll-margin-top: 80px;
  }
  .metric h2 {
    font-size: 1.15rem;
  }
  .m-head {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: baseline;
    gap: 4px 12px;
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
    overflow-wrap: anywhere;
  }
  .tr {
    display: inline-flex;
    color: var(--text-soft);
  }
  .mm {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    color: var(--text-soft);
    font-size: 0.8rem;
    margin-top: 8px;
  }

  .obs {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 9px 0;
    border-top: 1px solid var(--border);
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
    border: 1px solid var(--border-strong);
    background: var(--surface);
  }
  .dots .d.on {
    background: var(--accent);
    border-color: var(--accent);
  }
  .count {
    color: var(--text-soft);
    font-size: 0.8rem;
    font-weight: 600;
    min-width: 38px;
    text-align: right;
  }
</style>
