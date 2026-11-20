<script lang="ts">
  // Honey harvests of a hive: season total, record / correct / delete.
  import { _, locale } from 'svelte-i18n';
  import { recordHarvest, editHarvest, deleteHarvest } from '#lib/local/history.js';
  import { formatDate, toDateInput, todayLocal } from '#lib/dates.js';
  import type { HarvestRow } from '../../../routes/(app)/rows.js';
  import { createSaver } from './saver.js';
  import { dotted, storedDate } from './forms.js';
  import FormFooter from './FormFooter.svelte';
  import { deleteWithUndo, type OfferUndo } from './deleting.js';
  import RecordActions from './RecordActions.svelte';
  import './hive.css';

  let {
    hiveId,
    harvests,
    total,
    reload,
    offerUndo
  }: {
    hiveId: string;
    harvests: HarvestRow[];
    total: number;
    reload: () => Promise<void> | void;
    offerUndo: OfferUndo;
  } = $props();

  function draft(row: HarvestRow | null) {
    return {
      date: row ? toDateInput(row.date) : todayLocal(),
      amount_kg: row?.amount_kg ?? (null as number | null),
      variety: row?.variety ?? '',
      water_content: row?.water_content || (null as number | null),
      batch_number: row?.batch_number ?? '',
      best_before: toDateInput(row?.best_before),
      note: row?.note ?? ''
    };
  }

  let open = $state(false);
  let editing = $state<HarvestRow | null>(null);
  let f = $state(draft(null));
  const saver = createSaver();
  const { busy, error } = saver;
  const valid = $derived(!!f.date && (f.amount_kg ?? 0) > 0);
  const day = (iso: string | null) => formatDate(iso, $locale);
  const kg = $derived($_('common.kg'));

  function show(row: HarvestRow | null) {
    editing = row;
    f = draft(row);
    open = true;
  }
  function close() {
    open = false;
    editing = null;
  }

  async function save() {
    if (!valid) return;
    const ok = await saver.run(async () => {
      const fields = {
        date: storedDate(f.date, editing?.date),
        amount_kg: f.amount_kg ?? 0,
        variety: f.variety.trim(),
        water_content: f.water_content ?? 0,
        batch_number: f.batch_number.trim(),
        best_before: storedDate(f.best_before, editing?.best_before),
        note: f.note.trim()
      };
      if (editing) await editHarvest(editing.id, fields);
      else await recordHarvest({ hiveId, ...fields });
      await reload();
    });
    if (ok) close();
  }

  function del(h: HarvestRow) {
    const values = { amount: h.amount_kg, date: day(h.date) };
    return deleteWithUndo({
      ask: $_('harvest.confirm_delete', { values }),
      done: $_('harvest.deleted', { values }),
      run: () => deleteHarvest(h.id),
      reload,
      offer: offerUndo
    });
  }
</script>

<section class="hv-section">
  <div class="hv-sec-head">
    <h2>{dotted($_('harvest.title'), total ? `${total} ${kg}` : '')}</h2>
    <button type="button" class="hv-btn" onclick={() => (open ? close() : show(null))}
      >+ {$_('harvest.record')}</button
    >
  </div>

  {#if open}
    <form
      class="hv-card hv-form"
      onsubmit={(e) => {
        e.preventDefault();
        save();
      }}
    >
      <div class="hv-grid2">
        <label class="hv-field">
          <span>{$_('insp.date')}</span>
          <input type="date" bind:value={f.date} max={todayLocal()} required />
        </label>
        <label class="hv-field">
          <span>{$_('harvest.amount')} ({kg})</span>
          <input
            type="number"
            inputmode="decimal"
            min="0"
            step="0.1"
            bind:value={f.amount_kg}
            required
          />
        </label>
        <label class="hv-field">
          <span>{$_('harvest.variety')}</span>
          <input bind:value={f.variety} placeholder={$_('harvest.variety_ph')} />
        </label>
        <label class="hv-field">
          <span>{$_('harvest.water')} (%)</span>
          <input
            type="number"
            inputmode="decimal"
            min="0"
            max="100"
            step="0.1"
            bind:value={f.water_content}
          />
        </label>
        <label class="hv-field">
          <span>{$_('harvest.batch')}</span>
          <input bind:value={f.batch_number} />
        </label>
        <label class="hv-field">
          <span>{$_('harvest.best_before')}</span>
          <input type="date" bind:value={f.best_before} />
        </label>
      </div>
      <label class="hv-field">
        <span>{$_('insp.note')}</span>
        <textarea bind:value={f.note} rows="2" placeholder={$_('insp.note_ph')}></textarea>
      </label>
      <FormFooter busy={$busy} error={$error} invalid={!valid} oncancel={close} />
    </form>
  {/if}

  {#if harvests.length === 0}
    <p class="hv-empty">{$_('harvest.none')}</p>
  {:else}
    <ul class="hv-timeline">
      {#each harvests as h (h.id)}
        <li class="hv-card hv-record">
          <div class="hv-rhead">
            <strong>{dotted(`${h.amount_kg} ${kg}`, h.variety)}</strong>
            <span class="hv-muted hv-sm">{day(h.date)}</span>
          </div>
          <div class="hv-chips">
            {#if h.water_content}
              <span class="hv-chip">{$_('harvest.water')}: {h.water_content} %</span>
            {/if}
            {#if h.batch_number}
              <span class="hv-chip">{$_('harvest.batch')}: {h.batch_number}</span>
            {/if}
            {#if h.best_before}
              <span class="hv-chip">{$_('harvest.best_before')}: {day(h.best_before)}</span>
            {/if}
            {#if h.amended_at}
              <span class="hv-chip plain"
                >{$_('harvest.amended', { values: { date: day(h.amended_at) } })}</span
              >
            {/if}
          </div>
          {#if h.note}<p class="hv-note">{h.note}</p>{/if}
          <RecordActions
            name={`${h.amount_kg} ${kg}, ${day(h.date)}`}
            onedit={() => show(h)}
            ondelete={() => del(h)}
          />
        </li>
      {/each}
    </ul>
  {/if}
</section>
