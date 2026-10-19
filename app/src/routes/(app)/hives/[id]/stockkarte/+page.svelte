<script lang="ts">
  import { page } from '$app/state';
  import { _ } from 'svelte-i18n';
  import { ArrowUp, Check, Crown, Printer, Scissors } from '@lucide/svelte';
  import { hives, apiaries, queens, inspections, MARKING_NAMES } from '#lib/local/repo.js';
  import type { HiveRow, InspectionRow, QueenRow } from '../../../rows.js';
  import { dataVersion } from '#lib/local/live.js';

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
      queen = await queens.current(id);
      visits = await inspections.listByHive(id); // newest first
    }
    loaded = true;
  }
  $effect(() => {
    $dataVersion;
    load();
  });

  const num = (v: number | null | undefined) => v || '';
  const temp = (v: number | null | undefined) => v ?? '';
  const d = (s: string) => (s ? new Date(s).toLocaleDateString() : '');
</script>

<div class="page">
  <a class="back" href={`/hives/${id}`}>‹ {hive?.name ?? ''}</a>

  {#if loaded && hive}
    <div class="head">
      <div>
        <h1>{$_('insp.stockkarte')}</h1>
        <p class="muted">
          {hive.name}{#if apiaryName}
            · {apiaryName}{/if}
          {#if hive.type}
            · {$_('hivetype.' + hive.type)}{/if}
          {#if queen}
            · <Crown size={14} aria-hidden="true" />
            {queen.year}{#if queen.marking}
              {$_('queens.marking')}: {MARKING_NAMES[queen.marking] ?? ''}{/if}{/if}
        </p>
      </div>
      <button class="print" onclick={() => window.print()}
        ><Printer size={16} aria-hidden="true" /> {$_('qr.print')}</button
      >
    </div>

    {#if visits.length === 0}
      <p class="muted empty">{$_('insp.no_visits')}</p>
    {:else}
      <div class="scroll">
        <table class="card-table">
          <thead>
            <tr>
              <th class="sticky">{$_('insp.date')}</th>
              <th>{$_('insp.weather')}</th>
              <th title={$_('insp.queen_seen')}><Crown size={14} aria-hidden="true" /></th>
              <th title={$_('insp.eggs_seen')}>{$_('insp.eggs_seen')}</th>
              <th title={$_('insp.covered_larva')}>{$_('insp.covered_larva')}</th>
              <th>{$_('insp.frames')}</th>
              <th>{$_('insp.brood_frames')}</th>
              <th>{$_('insp.stores')}</th>
              <th>{$_('insp.queen_cells')}</th>
              <th>{$_('insp.temperament')}</th>
              <th>{$_('insp.calmness')}</th>
              <th>{$_('insp.varroa')}</th>
              <th>{$_('insp.fed_kg')}</th>
              <th title={$_('insp.super_added')}><ArrowUp size={14} aria-hidden="true" /></th>
              <th title={$_('insp.drone_frame_cut')}><Scissors size={14} aria-hidden="true" /></th>
              <th>{$_('insp.temp_hive')} °C</th>
              <th>{$_('insp.temp_outside')} °C</th>
              <th>{$_('insp.humidity_hive')} %</th>
              <th>{$_('insp.humidity_outside')} %</th>
              <th>{$_('insp.weight_kg')}</th>
              <th>{$_('insp.honey_kg')}</th>
              <th>{$_('insp.note')}</th>
            </tr>
          </thead>
          <tbody>
            {#each visits as i}
              <tr>
                <td class="sticky">{d(i.date)}</td>
                <td class="t">{i.weather ?? ''}</td>
                <td class="c"
                  >{#if i.queen_seen}<Check size={14} aria-hidden="true" />{/if}</td
                >
                <td class="c"
                  >{#if i.eggs_seen}<Check size={14} aria-hidden="true" />{/if}</td
                >
                <td class="c"
                  >{#if i.covered_larva}<Check size={14} aria-hidden="true" />{/if}</td
                >
                <td class="c">{num(i.frames)}</td>
                <td class="c">{num(i.brood_frames)}</td>
                <td class="c">{i.stores ? $_('storeslvl.' + i.stores) : ''}</td>
                <td class="c">{num(i.queen_cells)}</td>
                <td class="c">{i.temperament ? $_('temperament.' + i.temperament) : ''}</td>
                <td class="c">{i.calmness ? $_('calmlvl.' + i.calmness) : ''}</td>
                <td class="t">{i.varroa ?? ''}</td>
                <td class="c">{num(i.fed_kg)}</td>
                <td class="c"
                  >{#if i.super_added}<Check size={14} aria-hidden="true" />{/if}</td
                >
                <td class="c"
                  >{#if i.drone_frame_cut}<Check size={14} aria-hidden="true" />{/if}</td
                >
                <td class="c">{temp(i.temp_hive)}</td>
                <td class="c">{temp(i.temp_outside)}</td>
                <td class="c">{temp(i.humidity_hive)}</td>
                <td class="c">{temp(i.humidity_outside)}</td>
                <td class="c">{num(i.weight_kg)}</td>
                <td class="c">{num(i.honey_kg)}</td>
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
  .back {
    color: var(--honey-d, #a4641a);
    font-weight: 700;
    text-decoration: none;
    font-size: 0.9rem;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
    gap: 12px;
    margin: 12px 0 16px;
    flex-wrap: wrap;
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.7rem;
    margin: 0;
  }
  .muted {
    color: var(--ink-soft, #6b5e48);
    font-size: 0.9rem;
    margin: 4px 0 0;
  }
  .empty {
    margin-top: 24px;
  }
  .page :global(.lucide-icon) {
    vertical-align: -0.15em;
  }
  .print {
    font: inherit;
    font-weight: 700;
    cursor: pointer;
    border: 1px solid var(--line, #e5dcc6);
    border-radius: 11px;
    padding: 9px 16px;
    background: #fff;
    color: var(--ink, #2c2316);
  }

  .scroll {
    overflow-x: auto;
    border: 1px solid var(--line, #e5dcc6);
    border-radius: 14px;
    background: #fff;
  }
  .card-table {
    border-collapse: collapse;
    font-size: 0.8rem;
    white-space: nowrap;
  }
  .card-table th,
  .card-table td {
    border: 1px solid var(--line, #e5dcc6);
    padding: 7px 9px;
    text-align: center;
  }
  .card-table thead th {
    background: var(--cream, #fbf6ea);
    color: var(--ink-soft, #6b5e48);
    font-weight: 700;
    position: sticky;
    top: 0;
    z-index: 1;
  }
  .card-table tbody tr:nth-child(even) td {
    background: rgba(199, 127, 34, 0.04);
  }
  .card-table td.t {
    text-align: left;
    white-space: normal;
    max-width: 160px;
  }
  .card-table td.note {
    color: var(--ink-soft, #6b5e48);
  }
  .sticky {
    position: sticky;
    left: 0;
    background: var(--cream2, #fffdf7);
    font-weight: 700;
    z-index: 2;
  }
  .card-table thead th.sticky {
    z-index: 3;
    background: var(--cream, #fbf6ea);
  }

  @media print {
    .back,
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
