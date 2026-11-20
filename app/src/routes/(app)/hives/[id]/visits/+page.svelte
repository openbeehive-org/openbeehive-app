<script lang="ts">
  import { page } from '$app/state';
  import { tick } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { hives, inspections } from '#lib/local/repo.js';
  import type { Undo } from '#lib/local/history.js';
  import type { HiveRow, InspectionRow } from '../../../rows.js';
  import { dataVersion } from '#lib/local/live.js';
  import { isSensor } from '#lib/components/hive/readings.js';
  import VisitForm from '#lib/components/hive/VisitForm.svelte';
  import VisitList from '#lib/components/hive/VisitList.svelte';
  import UndoBar from '#lib/components/hive/UndoBar.svelte';
  import '#lib/components/hive/hive.css';

  const id = page.params.id ?? '';
  let hive = $state<HiveRow | null>(null);
  // Visits and sensor readings, newest first.
  let all = $state<InspectionRow[]>([]);
  let loaded = $state(false);
  // Sensor readings are measurements, not visits: hidden unless asked for.
  let showSensors = $state(false);
  let editing = $state<InspectionRow | null>(null);
  let formEl = $state<HTMLElement>();
  let undoBar = $state<UndoBar>();

  const visits = $derived(all.filter((i) => !isSensor(i)));
  const sensorCount = $derived(all.length - visits.length);
  const shown = $derived(showSensors ? all : visits);

  async function load() {
    hive = await hives.get(id);
    if (hive) all = await inspections.listByHive(id, { sensors: true });
    loaded = true;
  }
  $effect(() => {
    $dataVersion;
    load();
  });

  async function edit(visit: InspectionRow) {
    editing = visit;
    await tick();
    formEl?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
  async function saved() {
    editing = null;
    await load();
  }
  const offerUndo = (text: string, undo: Undo) => undoBar?.offer(text, undo);
</script>

<div class="hv-page">
  <a class="hv-back" href={`/hives/${id}`}>‹ {hive?.name ?? $_('hives.title')}</a>

  {#if !loaded}
    <p class="hv-muted">…</p>
  {:else if !hive}
    <p class="hv-muted">{$_('hives.not_found')}</p>
  {:else}
    <h1>{$_('insp.log')}</h1>
    <p class="hv-sub">{hive.name} · {visits.length} {$_('stats.visits')}</p>

    {#if sensorCount}
      <label class="hv-check toggle">
        <input type="checkbox" bind:checked={showSensors} />
        {$_('insp.show_sensors', { values: { n: sensorCount } })}
      </label>
    {/if}

    {#if editing}
      <div class="form-anchor" bind:this={formEl}>
        {#key editing.id}
          <VisitForm hiveId={id} {editing} onsaved={saved} oncancel={() => (editing = null)} />
        {/key}
      </div>
    {/if}

    {#if shown.length === 0}
      <p class="hv-empty">{$_('insp.no_visits')}</p>
    {:else}
      <VisitList visits={shown} onedit={edit} reload={load} {offerUndo} />
    {/if}
  {/if}
</div>

<UndoBar bind:this={undoBar} />

<style>
  h1 {
    margin: 4px 0 2px;
  }
  .toggle {
    margin-bottom: 16px;
  }
  .form-anchor {
    scroll-margin-top: 12px;
  }
</style>
