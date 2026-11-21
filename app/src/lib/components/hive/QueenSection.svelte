<script lang="ts">
  // The hive's queen: set, replace (a real re-queening), correct her details
  // (no re-queening) or delete one recorded by mistake.
  import { focusOnOpen } from '#lib/focus.js';
  import { _, locale } from 'svelte-i18n';
  import { markingForYear, MARKING_COLORS } from '#lib/local/repo.js';
  import { setQueen, editQueen, deleteQueen } from '#lib/local/history.js';
  import { formatDate, fromDateInput, toDateInput, todayLocal } from '#lib/dates.js';
  import type { QueenRow } from '../../../routes/(app)/rows.js';
  import { createSaver } from './saver.js';
  import { changedFrom, dotted, storedDate } from './forms.js';
  import { deleteWithUndo, type OfferUndo } from './deleting.js';
  import FormFooter from './FormFooter.svelte';
  import RecordActions from './RecordActions.svelte';
  import './hive.css';

  let {
    hiveId,
    apiaryId,
    queen,
    queenLog,
    reload,
    offerUndo
  }: {
    hiveId: string;
    apiaryId: string;
    /** The reigning queen, if any. */
    queen: QueenRow | null;
    /** All queens of the hive, newest first. */
    queenLog: QueenRow[];
    reload: () => Promise<void> | void;
    offerUndo: OfferUndo;
  } = $props();

  const MARKINGS = [1, 2, 3, 4, 5];
  const colorName = (m: number | null | undefined) => (m ? $_(`queens.color.${m}`) : '');
  const day = (iso: string | null) => formatDate(iso, $locale);

  function draft(row: QueenRow | null) {
    const year = row?.year ?? new Date().getFullYear();
    return {
      year: year as number | null,
      number: row?.breeder_number ?? '',
      // The colour follows the year unless the beekeeper chose another one.
      markingAuto: !row || row.marking === markingForYear(row.year),
      marking: row?.marking ?? markingForYear(year),
      introduced: row ? toDateInput(row.introduced_at) : todayLocal(),
      origin: row?.origin ?? '',
      note: row?.note ?? ''
    };
  }

  let open = $state(false);
  let editing = $state<QueenRow | null>(null);
  let f = $state(draft(null));
  // What the form showed when it was opened; a correction saves only what differs.
  let opened: ReturnType<typeof corrected> | null = null;
  const saver = createSaver();
  const { busy, error } = saver;

  const year = $derived(Number.isInteger(f.year) ? (f.year as number) : null);
  const effMarking = $derived(f.markingAuto && year !== null ? markingForYear(year) : f.marking);
  const valid = $derived(year !== null && year >= 1990 && year <= 2100 && !!f.introduced);
  const former = $derived(queenLog.filter((q) => q.id !== queen?.id));

  function show(row: QueenRow | null) {
    editing = row;
    f = draft(row);
    opened = row ? corrected() : null;
    open = true;
  }
  function close() {
    open = false;
    editing = null;
  }

  function fields() {
    return {
      year: year ?? 0,
      marking: effMarking,
      origin: f.origin.trim(),
      breeder_number: f.number.trim(),
      note: f.note.trim()
    };
  }
  // A correction also carries the introduction date, keeping its stored time.
  function corrected() {
    return { ...fields(), introduced_at: storedDate(f.introduced, editing?.introduced_at) };
  }

  async function save() {
    if (!valid || year === null) return;
    const ok = await saver.run(async () => {
      if (editing && opened) {
        await editQueen(editing.id, changedFrom(opened, corrected()));
      } else if (f.introduced === todayLocal()) {
        await setQueen(hiveId, apiaryId, fields()); // introduced right now
      } else {
        await setQueen(hiveId, apiaryId, {
          ...fields(),
          introduced_at: fromDateInput(f.introduced)
        });
      }
      await reload();
    });
    if (ok) close();
  }

  function del(q: QueenRow) {
    const values = { year: q.year };
    return deleteWithUndo({
      ask: $_('queens.confirm_delete', { values }),
      done: $_('queens.deleted', { values }),
      run: () => deleteQueen(q.id),
      reload,
      offer: offerUndo
    });
  }
</script>

<section class="hv-section">
  <div class="hv-sec-head">
    <h2>{$_('queens.title')}</h2>
    {#if queen}
      <button type="button" class="hv-btn" onclick={() => show(null)}>{$_('queens.replace')}</button
      >
    {/if}
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
      <h3>
        {editing ? $_('queens.edit') : queen ? $_('queens.replace') : $_('queens.set')}
      </h3>
      {#if editing}<p class="hv-muted hv-sm">{$_('queens.edit_hint')}</p>{/if}
      <div class="hv-grid2">
        <label class="hv-field">
          <span>{$_('queens.year')}</span>
          <input
            type="number"
            inputmode="numeric"
            bind:value={f.year}
            min="1990"
            max="2100"
            step="1"
            required
          />
        </label>
        <label class="hv-field">
          <span>{$_('queens.number')}</span>
          <input bind:value={f.number} inputmode="numeric" placeholder={$_('queens.number_ph')} />
        </label>
        <label class="hv-field">
          <span>{$_('queens.introduced')}</span>
          <input type="date" bind:value={f.introduced} max={todayLocal()} required />
        </label>
        <label class="hv-field">
          <span>{$_('queens.origin')}</span>
          <input bind:value={f.origin} placeholder={$_('queens.origin_ph')} />
        </label>
      </div>

      <fieldset class="marking">
        <legend>{$_('queens.marking')}</legend>
        <span class="now">
          <span class="hv-dot lg" style:background={MARKING_COLORS[effMarking]}></span>
          {colorName(effMarking)}
        </span>
        <label class="hv-check">
          <input type="checkbox" bind:checked={f.markingAuto} />
          {$_('queens.marking_auto')}
        </label>
        {#if !f.markingAuto}
          <label class="hv-field">
            <span>{$_('queens.marking_color')}</span>
            <select bind:value={f.marking}>
              {#each MARKINGS as m}<option value={m}>{colorName(m)}</option>{/each}
            </select>
          </label>
        {/if}
      </fieldset>

      <label class="hv-field">
        <span>{$_('queens.comment')}</span>
        <textarea bind:value={f.note} rows="2" placeholder={$_('queens.comment_ph')}></textarea>
      </label>
      <FormFooter busy={$busy} error={$error} invalid={!valid} oncancel={close} />
    </form>
  {/if}

  {#if queen}
    <div class="hv-card hv-record">
      <div class="queen">
        <span class="hv-dot lg" style:background={MARKING_COLORS[queen.marking]}></span>
        <div>
          <strong>
            {dotted(
              `${$_('queens.title')} ${queen.year}`,
              queen.breeder_number && `#${queen.breeder_number}`
            )}
          </strong>
          <div class="hv-muted hv-sm">{dotted(colorName(queen.marking), queen.origin)}</div>
          {#if queen.note}<div class="hv-note">{queen.note}</div>{/if}
        </div>
      </div>
      <RecordActions
        name={`${$_('queens.title')} ${queen.year}`}
        onedit={() => show(queen)}
        ondelete={() => del(queen)}
      />
    </div>
  {:else if !open}
    <div class="empty-inline">
      <span class="hv-muted">{$_('queens.none')}</span>
      <button type="button" class="hv-btn" onclick={() => show(null)}>{$_('queens.set')}</button>
    </div>
  {/if}

  {#if former.length}
    <details class="qhist">
      <summary>{$_('queens.history')}</summary>
      <ul>
        {#each former as q (q.id)}
          <li>
            <span class="who">
              <span class="hv-dot" style:background={MARKING_COLORS[q.marking]}></span>
              {$_('queens.title')}
              {q.year}
              <span class="hv-muted hv-sm">
                {day(q.introduced_at)}{#if q.replaced_at}
                  – {day(q.replaced_at)}{/if}
              </span>
            </span>
            <RecordActions
              name={`${$_('queens.title')} ${q.year}`}
              onedit={() => show(q)}
              ondelete={() => del(q)}
            />
          </li>
        {/each}
      </ul>
    </details>
  {/if}
</section>

<style>
  h3 {
    font-size: 1.15rem;
  }
  .marking {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px 14px;
    border: none;
    min-width: 0;
  }
  .marking legend {
    font-size: 0.86rem;
    font-weight: 600;
    color: var(--text-soft);
    margin-bottom: 5px;
  }
  .now {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
  }
  .queen {
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .empty-inline {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: center;
    gap: 10px;
    padding: 14px 16px;
    border: 1px dashed var(--border-strong);
    border-radius: 14px;
  }
  .qhist {
    margin-top: 12px;
  }
  .qhist summary {
    padding: 12px 0;
    cursor: pointer;
    color: var(--text-soft);
    font-weight: 600;
    font-size: 0.9rem;
  }
  .qhist ul {
    list-style: none;
    display: grid;
    gap: 4px;
  }
  .qhist li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 4px 10px;
    border-top: 1px solid var(--border);
  }
  .who {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
</style>
