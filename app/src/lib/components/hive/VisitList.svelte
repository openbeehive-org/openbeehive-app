<script lang="ts">
  // Visit cards with their summary chips, photos and Edit / Delete.
  import { _, locale } from 'svelte-i18n';
  import { Radio } from '@lucide/svelte';
  import { deleteInspection } from '#lib/local/history.js';
  import { formatDate } from '#lib/dates.js';
  import type { InspectionRow } from '../../../routes/(app)/rows.js';
  import { isSensor, visitChips } from './readings.js';
  import { units } from '#lib/prefs.js';
  import { deleteWithUndo, type OfferUndo } from './deleting.js';
  import RecordActions from './RecordActions.svelte';
  import Photos from './Photos.svelte';
  import './hive.css';

  let {
    visits,
    onedit,
    reload,
    offerUndo
  }: {
    visits: InspectionRow[];
    onedit: (visit: InspectionRow) => void;
    reload: () => Promise<void> | void;
    offerUndo: OfferUndo;
  } = $props();

  const day = (i: InspectionRow) => formatDate(i.date, $locale);

  function del(i: InspectionRow) {
    const values = { date: day(i) };
    return deleteWithUndo({
      ask: $_('insp.confirm_delete', { values }),
      done: $_('insp.deleted', { values }),
      run: () => deleteInspection(i.id),
      reload,
      offer: offerUndo
    });
  }
</script>

<ul class="hv-timeline">
  {#each visits as i (i.id)}
    {@const chips = visitChips(i, $_, $units, $locale)}
    <li class="hv-card hv-record">
      <div class="hv-rhead">
        <strong>{day(i)}</strong>
        {#if isSensor(i)}
          <span class="hv-chip plain">
            <Radio size={12} aria-hidden="true" />
            {$_('insp.sensor')}
          </span>
        {/if}
        {#if i.weather}<span class="hv-muted hv-sm">{i.weather}</span>{/if}
      </div>
      {#if chips.length}
        <div class="hv-chips">
          {#each chips as c}<span class="hv-chip">{c}</span>{/each}
        </div>
      {/if}
      {#if i.note}<p class="hv-note">{i.note}</p>{/if}
      <Photos kind="gallery" photos={i.photos ?? []} label={day(i)} onchange={reload} />
      <RecordActions
        name={`${$_('insp.visit')} ${day(i)}`}
        onedit={() => onedit(i)}
        ondelete={() => del(i)}
      />
    </li>
  {/each}
</ul>
