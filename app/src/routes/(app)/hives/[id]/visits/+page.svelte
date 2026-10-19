<script lang="ts">
  import { page } from '$app/state';
  import { _ } from 'svelte-i18n';
  import { hives, inspections } from '#lib/local/repo.js';
  import type { HiveRow, InspectionRow } from '../../../rows.js';
  import { dataVersion } from '#lib/local/live.js';

  const id = page.params.id ?? '';
  let hive = $state<HiveRow | null>(null);
  let visits = $state<InspectionRow[]>([]);
  let loaded = $state(false);

  async function load() {
    hive = await hives.get(id);
    if (hive) visits = await inspections.listByHive(id);
    loaded = true;
  }
  $effect(() => {
    $dataVersion;
    load();
  });

  // Same compact summary as the hive page.
  function chips(i: InspectionRow): string[] {
    const out: string[] = [];
    if (i.queen_seen) out.push($_('insp.queen_seen'));
    if (i.eggs_seen) out.push($_('insp.eggs_seen'));
    if (i.youngest_larva) out.push(`${$_('insp.youngest_larva')}: ${i.youngest_larva}d`);
    if (i.covered_larva) out.push($_('insp.covered_larva'));
    if (i.frames) out.push(`${i.frames} ${$_('insp.frames')}`);
    if (i.brood_frames) out.push(`${i.brood_frames} ${$_('insp.brood_frames')}`);
    if (i.stores) out.push(`${$_('insp.stores')}: ${$_('storeslvl.' + i.stores)}`);
    if (i.queen_cells) out.push(`${i.queen_cells} ${$_('insp.queen_cells')}`);
    if (i.temperament) out.push(`${$_('insp.temperament')}: ${$_('temperament.' + i.temperament)}`);
    if (i.calmness) out.push(`${$_('insp.calmness')}: ${$_('calmlvl.' + i.calmness)}`);
    if (i.fed_kg) out.push(`${$_('insp.fed_kg')} ${i.fed_kg} kg`);
    if (i.frames_added) out.push(`+${i.frames_added} ${$_('insp.frames')}`);
    if (i.frames_removed) out.push(`−${i.frames_removed} ${$_('insp.frames')}`);
    if (i.drone_frame_cut) out.push($_('insp.drone_frame_cut'));
    if (i.super_added) out.push($_('insp.super_added'));
    if (i.weight_kg) out.push(`${i.weight_kg} kg`);
    if (i.honey_kg) out.push(`${$_('insp.honey_kg')} ${i.honey_kg} kg`);
    if (i.temp_hive != null) out.push(`${$_('insp.temp_hive')} ${i.temp_hive}°C`);
    if (i.humidity_hive != null) out.push(`${$_('insp.humidity_hive')} ${i.humidity_hive}%`);
    if (i.varroa) out.push(`${$_('insp.varroa')}: ${i.varroa}`);
    return out;
  }
</script>

<div class="page">
  <a class="back" href={`/hives/${id}`}>‹ {hive?.name ?? $_('hives.title')}</a>

  {#if !loaded}
    <p class="muted">...</p>
  {:else if !hive}
    <p class="muted">{$_('hives.not_found')}</p>
  {:else}
    <h1>{$_('insp.log')}</h1>
    <p class="muted sub">{hive.name} · {visits.length} {$_('stats.visits')}</p>

    {#if visits.length === 0}
      <p class="muted empty">{$_('insp.no_visits')}</p>
    {:else}
      <ul class="timeline">
        {#each visits as i}
          <li class="card visit">
            <div class="vhead">
              <strong>{new Date(i.date).toLocaleDateString()}</strong>
              {#if i.weather}<span class="muted sm">{i.weather}</span>{/if}
            </div>
            {#if chips(i).length}
              <div class="chips">
                {#each chips(i) as c}<span class="chip">{c}</span>{/each}
              </div>
            {/if}
            {#if i.note}<p class="vnote">{i.note}</p>{/if}
            {#if i.photos?.length}
              <div class="vphotos">
                {#each i.photos as ph}
                  <a class="vphoto" href={ph} target="_blank" rel="noreferrer"
                    ><img src={ph} alt="" /></a
                  >
                {/each}
              </div>
            {/if}
          </li>
        {/each}
      </ul>
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
  .muted.sm {
    font-size: 0.82rem;
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
  .timeline {
    list-style: none;
    display: grid;
    gap: 12px;
  }
  .card {
    background: var(--cream2);
    border: 1px solid var(--line);
    border-radius: 16px;
  }
  .visit {
    padding: 14px 16px;
  }
  .vhead {
    display: flex;
    align-items: baseline;
    gap: 10px;
    margin-bottom: 8px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chip {
    background: rgba(92, 107, 74, 0.12);
    color: #41502f;
    font-size: 0.76rem;
    font-weight: 600;
    padding: 4px 10px;
    border-radius: 999px;
  }
  .vnote {
    margin-top: 8px;
    color: var(--ink-soft);
    font-size: 0.88rem;
    white-space: pre-wrap;
  }
  .vphotos {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 10px;
  }
  .vphoto {
    width: 76px;
    height: 76px;
    border-radius: 10px;
    overflow: hidden;
    border: 1px solid var(--line);
  }
  .vphoto img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
</style>
