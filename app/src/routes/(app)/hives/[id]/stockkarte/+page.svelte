<script lang="ts">
  import { page } from '$app/state';
  import { _, locale } from 'svelte-i18n';
  import { ArrowUp, Check, Crown, Printer, Scissors } from '@lucide/svelte';
  import { hives, apiaries, queens, inspections } from '#lib/local/repo.js';
  import type { HiveRow, InspectionRow, QueenRow } from '../../../rows.js';
  import { dataVersion } from '#lib/local/live.js';
  import { formatDate } from '#lib/dates.js';
  import { level, reading, type NumericKey } from '#lib/components/hive/readings.js';
  import { dotted } from '#lib/components/hive/forms.js';
  import '#lib/components/hive/hive.css';

  const id = page.params.id ?? '';
  let hive = $state<HiveRow | null>(null);
  let apiaryName = $state('');
  let queen = $state<QueenRow | null>(null);
  let visits = $state<InspectionRow[]>([]);
  let loaded = $state(false);

  async function load() {
    hive = await hives.get(id);
    if (hive) {
      apiaryName = hive.apiary_id ? ((await apiaries.get(hive.apiary_id))?.name ?? '') : '';
      queen = (await queens.current(id)) ?? null;
      visits = await inspections.listByHive(id); // visits only, newest first
    }
    loaded = true;
  }
  $effect(() => {
    $dataVersion;
    load();
  });

  // Empty cell = not recorded; a recorded 0 is printed as 0.
  const num = (i: InspectionRow, key: NumericKey) => reading(i, key) ?? '';
  const d = (s: string) => formatDate(s, $locale, { dateStyle: 'short' });
</script>

{#snippet tick(on: number | null)}
  {#if on}<Check size={14} aria-hidden="true" /><span class="hv-sr">{$_('insp.yes')}</span>{/if}
{/snippet}

<div class="page">
  <a class="hv-back" href={`/hives/${id}`}>‹ {hive?.name ?? ''}</a>

  {#if loaded && hive}
    <div class="head">
      <div>
        <h1>{$_('insp.stockkarte')}</h1>
        <p class="muted">
          {dotted(hive.name, apiaryName, hive.type ? $_('hivetype.' + hive.type) : '')}
          {#if queen}
            &nbsp;· <Crown size={14} aria-hidden="true" />
            <span class="hv-sr">{$_('queens.title')}</span>
            {queen.marking
              ? `${queen.year}, ${$_('queens.marking')}: ${$_('queens.color.' + queen.marking)}`
              : queen.year}
          {/if}
        </p>
      </div>
      <button class="hv-btn ghost print" onclick={() => window.print()}
        ><Printer size={16} aria-hidden="true" /> {$_('qr.print')}</button
      >
    </div>

    {#if visits.length === 0}
      <p class="muted empty">{$_('insp.no_visits')}</p>
    {:else}
      <!-- Focusable, so the table can be scrolled sideways with the keyboard
           (a scrolling region is the documented exception to the rule below). -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="scroll" role="region" aria-label={$_('insp.card_table')} tabindex="0">
        <table class="card-table">
          <thead>
            <tr>
              <th class="sticky">{$_('insp.date')}</th>
              <th>{$_('insp.weather')}</th>
              <th>
                <Crown size={14} aria-hidden="true" />
                <span class="hv-sr">{$_('insp.queen_seen')}</span>
              </th>
              <th>{$_('insp.eggs_seen')}</th>
              <th>{$_('insp.covered_larva')}</th>
              <th>{$_('insp.frames')}</th>
              <th>{$_('insp.brood_frames')}</th>
              <th>{$_('insp.stores')}</th>
              <th>{$_('insp.queen_cells')}</th>
              <th>{$_('insp.temperament')}</th>
              <th>{$_('insp.calmness')}</th>
              <th>{$_('insp.varroa')}</th>
              <th>{$_('insp.fed_kg')} ({$_('common.kg')})</th>
              <th>
                <ArrowUp size={14} aria-hidden="true" />
                <span class="hv-sr">{$_('insp.super_added')}</span>
              </th>
              <th>
                <Scissors size={14} aria-hidden="true" />
                <span class="hv-sr">{$_('insp.drone_frame_cut')}</span>
              </th>
              <th>{$_('insp.temp_hive')} °C</th>
              <th>{$_('insp.temp_outside')} °C</th>
              <th>{$_('insp.humidity_hive')} %</th>
              <th>{$_('insp.humidity_outside')} %</th>
              <th>{$_('insp.weight_kg')} ({$_('common.kg')})</th>
              <th>{$_('insp.honey_kg')} ({$_('common.kg')})</th>
              <th>{$_('insp.note')}</th>
            </tr>
          </thead>
          <tbody>
            {#each visits as i}
              {@const stores = level(i, 'stores')}
              {@const temperament = level(i, 'temperament')}
              {@const calmness = level(i, 'calmness')}
              <tr>
                <td class="sticky">{d(i.date)}</td>
                <td class="t">{i.weather ?? ''}</td>
                <td class="c">{@render tick(i.queen_seen)}</td>
                <td class="c">{@render tick(i.eggs_seen)}</td>
                <td class="c">{@render tick(i.covered_larva)}</td>
                <td class="c">{num(i, 'frames')}</td>
                <td class="c">{num(i, 'brood_frames')}</td>
                <td class="c">{stores ? $_('storeslvl.' + stores) : ''}</td>
                <td class="c">{num(i, 'queen_cells')}</td>
                <td class="c">{temperament ? $_('temperament.' + temperament) : ''}</td>
                <td class="c">{calmness ? $_('calmlvl.' + calmness) : ''}</td>
                <td class="t">{i.varroa ?? ''}</td>
                <td class="c">{num(i, 'fed_kg')}</td>
                <td class="c">{@render tick(i.super_added)}</td>
                <td class="c">{@render tick(i.drone_frame_cut)}</td>
                <td class="c">{num(i, 'temp_hive')}</td>
                <td class="c">{num(i, 'temp_outside')}</td>
                <td class="c">{num(i, 'humidity_hive')}</td>
                <td class="c">{num(i, 'humidity_outside')}</td>
                <td class="c">{num(i, 'weight_kg')}</td>
                <td class="c">{i.honey_kg || ''}</td>
                <td class="t note">{i.note ?? ''}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</div>

<style>
  .page {
    max-width: 100%;
    margin: 0 auto;
    padding: 22px 18px 96px;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
    gap: 12px;
    margin: 4px 0 16px;
    flex-wrap: wrap;
  }
  h1 {
    font-size: 1.7rem;
    margin: 0;
  }
  .muted {
    color: var(--text-soft);
    font-size: 0.9rem;
    margin: 4px 0 0;
  }
  .empty {
    margin-top: 24px;
  }
  .page :global(.lucide-icon) {
    vertical-align: -0.15em;
  }

  .scroll {
    /* containing block for the visually hidden labels inside the table */
    position: relative;
    overflow-x: auto;
    border: 1px solid var(--border);
    border-radius: 14px;
    background: var(--surface);
  }
  .scroll:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .card-table {
    border-collapse: collapse;
    font-size: 0.8rem;
    white-space: nowrap;
  }
  .card-table th,
  .card-table td {
    border: 1px solid var(--border);
    padding: 7px 9px;
    text-align: center;
  }
  .card-table thead th {
    background: var(--bg);
    color: var(--text-soft);
    font-weight: 700;
    position: sticky;
    top: 0;
    z-index: 1;
  }
  .card-table td.t {
    text-align: left;
    white-space: normal;
    max-width: 160px;
  }
  .card-table td.note {
    color: var(--text-soft);
  }
  .sticky {
    position: sticky;
    left: 0;
    background: var(--surface);
    font-weight: 700;
    z-index: 2;
  }
  .card-table thead th.sticky {
    z-index: 3;
    background: var(--bg);
  }

  @media print {
    .page :global(.hv-back),
    .print {
      display: none;
    }
    .scroll {
      overflow: visible;
      border: none;
    }
    .card-table {
      font-size: 9px;
    }
    .page {
      padding: 0;
    }
  }
</style>
