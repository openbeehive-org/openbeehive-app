<script lang="ts">
  import { page } from '$app/state';
  import { tick } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { ChartLine } from '@lucide/svelte';
  import { hives, queens, inspections, apiaries, harvests, treatments } from '#lib/local/repo.js';
  import type {
    ApiaryRow,
    HarvestRow,
    HiveRow,
    InspectionRow,
    PlacementRow,
    QueenRow,
    TreatmentRow
  } from '../../rows.js';
  import { dataVersion } from '#lib/local/live.js';
  import { formatDate } from '#lib/dates.js';
  import { parseVarroa, reading, type NumericKey } from '#lib/components/hive/readings.js';
  import type { Undo } from '#lib/local/history.js';
  import HiveHeader from '#lib/components/hive/HiveHeader.svelte';
  import QueenSection from '#lib/components/hive/QueenSection.svelte';
  import VisitForm from '#lib/components/hive/VisitForm.svelte';
  import VisitList from '#lib/components/hive/VisitList.svelte';
  import HarvestSection from '#lib/components/hive/HarvestSection.svelte';
  import TreatmentSection from '#lib/components/hive/TreatmentSection.svelte';
  import UndoBar from '#lib/components/hive/UndoBar.svelte';
  import '#lib/components/hive/hive.css';

  const id = page.params.id ?? '';
  let hive = $state<HiveRow | null>(null);
  let apiaryName = $state('');
  let queen = $state<QueenRow | null>(null);
  let queenLog = $state<QueenRow[]>([]);
  // Visits a person recorded, newest first; sensor readings only feed the charts.
  let visits = $state<InspectionRow[]>([]);
  let locations = $state<PlacementRow[]>([]);
  let allApiaries = $state<ApiaryRow[]>([]);
  let harvestList = $state<HarvestRow[]>([]);
  let harvestTotal = $state(0);
  let treatmentList = $state<TreatmentRow[]>([]);
  let loaded = $state(false);

  // The visit form: closed, recording a new visit, or correcting one.
  let visitForm = $state<'' | 'new' | InspectionRow>('');
  let formEl = $state<HTMLElement>();
  let undoBar = $state<UndoBar>();

  // Latest-reading pills (link into the development charts) + capped visit list.
  const PILL_KEYS: { key: NumericKey; label: string; unit?: boolean }[] = [
    { key: 'frames', label: 'insp.frames' },
    { key: 'brood_frames', label: 'insp.brood_frames' },
    { key: 'queen_cells', label: 'insp.queen_cells' },
    { key: 'weight_kg', label: 'insp.weight_kg', unit: true }
  ];
  const latestVisit = $derived(visits[0]);
  const statPills = $derived.by(() => {
    if (!latestVisit) return [];
    const pills: { key: string; label: string; value: number; unit?: boolean }[] = [];
    for (const p of PILL_KEYS) {
      const value = reading(latestVisit, p.key);
      if (value !== null) pills.push({ ...p, value });
    }
    const varroa = parseVarroa(latestVisit.varroa);
    if (varroa !== null) pills.push({ key: 'varroa', label: 'insp.varroa', value: varroa });
    return pills;
  });
  const recentVisits = $derived(visits.slice(0, 5));

  async function load() {
    hive = await hives.get(id);
    if (hive) {
      const ap = hive.apiary_id ? await apiaries.get(hive.apiary_id) : null;
      apiaryName = ap?.name ?? '';
      queen = (await queens.current(id)) ?? null;
      queenLog = await queens.listByHive(id);
      visits = await inspections.listByHive(id);
      locations = await hives.locationHistory(id);
      allApiaries = (await apiaries.list()) as ApiaryRow[];
      harvestList = await harvests.listByHive(id);
      harvestTotal = await harvests.totalByHive(id);
      treatmentList = await treatments.listByHive(id);
    }
    loaded = true;
  }

  async function openVisitForm(what: 'new' | InspectionRow) {
    visitForm = what;
    await tick();
    formEl?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
  async function visitSaved() {
    visitForm = '';
    await load();
  }
  const offerUndo = (text: string, undo: Undo) => undoBar?.offer(text, undo);
  const day = (iso: string | null) => formatDate(iso, $locale);

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<div class="hv-page">
  <a class="hv-back" href={hive?.apiary_id ? `/apiaries/${hive.apiary_id}` : '/hives'}>
    ‹ {apiaryName || $_('hives.title')}
  </a>

  {#if !loaded}
    <p class="hv-muted">…</p>
  {:else if !hive}
    <p class="hv-muted">{$_('hives.not_found')}</p>
  {:else}
    <HiveHeader {hive} apiaries={allApiaries} reload={load} onrecord={() => openVisitForm('new')} />

    {#if statPills.length}
      <div class="pills">
        {#each statPills as p}
          <a class="pill" href={`/hives/${id}/stats#${p.key}`}>
            <span class="pl">{$_(p.label)}</span>
            <strong>{p.value}{p.unit ? ' ' + $_('common.kg') : ''}</strong>
          </a>
        {/each}
        <a class="pill dev" href={`/hives/${id}/stats`}
          ><ChartLine size={16} aria-hidden="true" /> {$_('stats.title')}</a
        >
      </div>
    {/if}

    <QueenSection
      hiveId={id}
      apiaryId={hive.apiary_id}
      {queen}
      {queenLog}
      reload={load}
      {offerUndo}
    />

    <!-- ---- Visit log ---- -->
    <section class="hv-section">
      <div class="hv-sec-head">
        <h2>{$_('insp.log')}</h2>
        <button
          type="button"
          class="hv-btn"
          onclick={() => (visitForm === 'new' ? (visitForm = '') : openVisitForm('new'))}
          >+ {$_('insp.record')}</button
        >
      </div>

      {#if visitForm}
        <div class="form-anchor" bind:this={formEl}>
          {#key visitForm === 'new' ? 'new' : visitForm.id}
            <VisitForm
              hiveId={id}
              editing={visitForm === 'new' ? null : visitForm}
              last={latestVisit ?? null}
              onsaved={visitSaved}
              oncancel={() => (visitForm = '')}
            />
          {/key}
        </div>
      {/if}

      {#if visits.length === 0}
        <p class="hv-empty">{$_('insp.no_visits')}</p>
      {:else}
        <VisitList visits={recentVisits} onedit={openVisitForm} reload={load} {offerUndo} />
        {#if visits.length > recentVisits.length}
          <a class="hv-link" href={`/hives/${id}/visits`}>
            {$_('insp.see_all', { values: { n: visits.length } })}
          </a>
        {/if}
      {/if}
    </section>

    <HarvestSection
      hiveId={id}
      harvests={harvestList}
      total={harvestTotal}
      reload={load}
      {offerUndo}
    />

    <TreatmentSection
      hiveId={id}
      hiveName={hive.name}
      {apiaryName}
      treatments={treatmentList}
      reload={load}
      {offerUndo}
    />

    <!-- ---- Location history (placements; appended on every move) ---- -->
    {#if locations.length}
      <section class="hv-section">
        <h2>{$_('hives.locations')}</h2>
        <ul class="locs">
          {#each locations as p, idx}
            <li>
              <span class="loc-dot" class:cur={idx === 0 && !p.end_at}></span>
              <div class="loc-body">
                <strong>{p.apiary_name ?? '–'}</strong>
                <span class="hv-muted hv-sm">
                  {day(p.start_at)} – {p.end_at ? day(p.end_at) : $_('hives.present')}
                </span>
              </div>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  {/if}
</div>

<UndoBar bind:this={undoBar} />

<style>
  .pills {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    margin: 4px 0 2px;
  }
  .pill {
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 1px;
    min-height: 48px;
    padding: 6px 13px;
    border-radius: 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    text-decoration: none;
    color: var(--text);
  }
  .pill .pl {
    font-size: 0.72rem;
    color: var(--text-soft);
    font-weight: 600;
  }
  .pill strong {
    font-size: 1rem;
  }
  .pill:hover {
    border-color: var(--accent);
  }
  .pill.dev {
    flex-direction: row;
    align-items: center;
    gap: 6px;
    font-weight: 700;
    color: var(--accent-text);
    border-color: var(--accent);
  }

  /* Clears the page top when "Record visit" scrolls the form into view. */
  .form-anchor {
    scroll-margin-top: 12px;
  }

  .locs {
    list-style: none;
    display: grid;
    gap: 2px;
    margin-top: 10px;
  }
  .locs li {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    padding: 10px 0;
    border-bottom: 1px solid var(--border);
  }
  .locs li:last-child {
    border-bottom: none;
  }
  .loc-dot {
    width: 11px;
    height: 11px;
    border-radius: 50%;
    background: var(--text-soft);
    margin-top: 4px;
    flex-shrink: 0;
  }
  .loc-dot.cur {
    background: var(--accent);
  }
  .loc-body strong {
    display: block;
    font-weight: 600;
  }
</style>
