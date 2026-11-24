<script lang="ts">
  // Treatments of a hive: record / correct / delete, and the printable register.
  import { focusOnOpen } from '#lib/focus.js';
  import { _, locale } from 'svelte-i18n';
  import { Pill, Printer } from '@lucide/svelte';
  import { recordTreatment, editTreatment, deleteTreatment } from '#lib/local/history.js';
  import { formatDate, toDateInput, todayLocal } from '#lib/dates.js';
  import { toast } from '#lib/toast.js';
  import type { TreatmentRow } from '../../../routes/(app)/rows.js';
  import { createSaver } from './saver.js';
  import { changedFrom, storedDate } from './forms.js';
  import { deleteWithUndo, type OfferUndo } from './deleting.js';
  import { printTreatmentRegister, withdrawalActive } from './treatmentRegister.js';
  import FormFooter from './FormFooter.svelte';
  import RecordActions from './RecordActions.svelte';
  import './hive.css';

  let {
    hiveId,
    hiveName,
    apiaryName = '',
    treatments,
    reload,
    offerUndo
  }: {
    hiveId: string;
    hiveName: string;
    apiaryName?: string;
    treatments: TreatmentRow[];
    reload: () => Promise<void> | void;
    offerUndo: OfferUndo;
  } = $props();

  function draft(row: TreatmentRow | null) {
    return {
      date: row ? toDateInput(row.date) : todayLocal(),
      product: row?.product ?? '',
      active_ingredient: row?.active_ingredient ?? '',
      dose: row?.dose ?? '',
      method: row?.method ?? '',
      batch_number: row?.batch_number ?? '',
      withdrawal_until: toDateInput(row?.withdrawal_until),
      reason: row ? (row.reason ?? '') : 'varroa',
      note: row?.note ?? ''
    };
  }

  let open = $state(false);
  let editing = $state<TreatmentRow | null>(null);
  let f = $state(draft(null));
  // What the form showed when it was opened; a correction saves only what differs.
  let opened: ReturnType<typeof fields> | null = null;
  const saver = createSaver();
  const { busy, error } = saver;
  const valid = $derived(!!f.date && !!f.product.trim());
  const day = (iso: string | null) => formatDate(iso, $locale);

  function show(row: TreatmentRow | null) {
    editing = row;
    f = draft(row);
    opened = row ? fields() : null;
    open = true;
  }
  function close() {
    open = false;
    editing = null;
  }

  function fields() {
    return {
      date: storedDate(f.date, editing?.date),
      product: f.product.trim(),
      active_ingredient: f.active_ingredient.trim(),
      dose: f.dose.trim(),
      method: f.method.trim(),
      batch_number: f.batch_number.trim(),
      withdrawal_until: storedDate(f.withdrawal_until, editing?.withdrawal_until),
      reason: f.reason.trim(),
      note: f.note.trim()
    };
  }

  async function save() {
    if (!valid) return;
    const ok = await saver.run(async () => {
      if (editing && opened) await editTreatment(editing.id, changedFrom(opened, fields()));
      else await recordTreatment({ hiveId, ...fields() });
      await reload();
    });
    if (ok) close();
  }

  function del(t: TreatmentRow) {
    const values = { product: t.product, date: day(t.date) };
    return deleteWithUndo({
      ask: $_('treat.confirm_delete', { values }),
      done: $_('treat.deleted', { values }),
      run: () => deleteTreatment(t.id),
      reload,
      offer: offerUndo
    });
  }

  function print() {
    const hive = { name: hiveName, apiary: apiaryName, treatments };
    if (!printTreatmentRegister([hive], $_, $locale ?? 'en')) toast($_('treat.print_blocked'));
  }
</script>

<section class="hv-section">
  <div class="hv-sec-head">
    <h2>{$_('treat.title')}</h2>
    <div class="hv-sec-acts">
      {#if treatments.length}
        <button type="button" class="hv-btn ghost" onclick={print}>
          <Printer size={16} aria-hidden="true" />
          {$_('treat.print_register')}
        </button>
      {/if}
      <button type="button" class="hv-btn" onclick={() => (open ? close() : show(null))}>
        + {$_('treat.record')}
      </button>
    </div>
  </div>

  {#if open}
    <form
      class="hv-card hv-form"
      use:focusOnOpen
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
          <span>{$_('treat.product')}</span>
          <input bind:value={f.product} placeholder={$_('treat.product_ph')} required />
        </label>
        <label class="hv-field">
          <span>{$_('treat.active')}</span>
          <input bind:value={f.active_ingredient} placeholder={$_('treat.active_ph')} />
        </label>
        <label class="hv-field">
          <span>{$_('treat.method')}</span>
          <input bind:value={f.method} placeholder={$_('treat.method_ph')} />
        </label>
        <label class="hv-field">
          <span>{$_('treat.dose')}</span>
          <input bind:value={f.dose} placeholder={$_('treat.dose_ph')} />
        </label>
        <label class="hv-field">
          <span>{$_('treat.batch')}</span>
          <input bind:value={f.batch_number} />
        </label>
        <label class="hv-field">
          <span>{$_('treat.reason')}</span>
          <input bind:value={f.reason} placeholder={$_('treat.reason_ph')} />
        </label>
        <label class="hv-field">
          <span>{$_('treat.withdrawal')}</span>
          <input type="date" bind:value={f.withdrawal_until} />
        </label>
      </div>
      <label class="hv-field">
        <span>{$_('insp.note')}</span>
        <textarea bind:value={f.note} rows="2" placeholder={$_('insp.note_ph')}></textarea>
      </label>
      <FormFooter busy={$busy} error={$error} invalid={!valid} oncancel={close} />
    </form>
  {/if}

  {#if treatments.length === 0}
    <p class="hv-empty">{$_('treat.none')}</p>
  {:else}
    <ul class="hv-timeline">
      {#each treatments as t (t.id)}
        <li class="hv-card hv-record">
          <div class="hv-rhead">
            <strong><Pill size={16} aria-hidden="true" /> {t.product}</strong>
            <span class="hv-muted hv-sm">{day(t.date)}</span>
          </div>
          <div class="hv-chips">
            {#if t.active_ingredient}<span class="hv-chip">{t.active_ingredient}</span>{/if}
            {#if t.dose}<span class="hv-chip">{$_('treat.dose')}: {t.dose}</span>{/if}
            {#if t.method}<span class="hv-chip">{t.method}</span>{/if}
            {#if t.batch_number}
              <span class="hv-chip">{$_('treat.batch')}: {t.batch_number}</span>
            {/if}
            {#if t.withdrawal_until}
              <!-- A warning only while honey must not be taken; plain afterwards. -->
              <span class="hv-chip" class:warn={withdrawalActive(t.withdrawal_until)}>
                {$_('treat.withdrawal')}: {day(t.withdrawal_until)}
              </span>
            {/if}
            {#if t.amended_at}
              <span class="hv-chip plain">
                {$_('treat.amended', { values: { date: day(t.amended_at) } })}
              </span>
            {/if}
          </div>
          {#if t.note}<p class="hv-note">{t.note}</p>{/if}
          <RecordActions
            name={`${t.product}, ${day(t.date)}`}
            onedit={() => show(t)}
            ondelete={() => del(t)}
          />
        </li>
      {/each}
    </ul>
  {/if}
</section>
