<script lang="ts">
  // The visit form, built for use at the hive: large controls, counts that
  // start empty, rarely used blocks folded away and Save always in reach.
  import { untrack } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { History } from '@lucide/svelte';
  import { recordInspection, editInspection } from '#lib/local/history.js';
  import { InspectionSource } from '#lib/local/repo.js';
  import { formatDate, toDateInput, todayLocal } from '#lib/dates.js';
  import type { InspectionRow } from '../../../routes/(app)/rows.js';
  import type { PreparedPhoto } from '#lib/image.js';
  import { carryOver, isSensor, level, reading, CLIMATE_KEYS, COUNT_KEYS } from './readings.js';
  import { createSaver } from './saver.js';
  import { changedFrom, storedDate } from './forms.js';
  import FormFooter from './FormFooter.svelte';
  import Stepper from './Stepper.svelte';
  import Segmented from './Segmented.svelte';
  import Photos, { attachVisitPhotos } from './Photos.svelte';
  import './hive.css';

  let {
    hiveId,
    editing = null,
    last = null,
    onsaved,
    oncancel
  }: {
    hiveId: string;
    /** The visit being corrected; null records a new one. */
    editing?: InspectionRow | null;
    /** The previous visit, offered as a starting point for a new one. */
    last?: InspectionRow | null;
    onsaved: () => void;
    oncancel: () => void;
  } = $props();

  const STORES = [1, 2, 3, 4];
  const TEMPERAMENTS = [1, 2, 3, 4, 5];
  const CALM = [1, 2, 3, 4];
  const options = (ns: string, codes: number[]) =>
    codes.map((value) => ({ value, label: $_(`${ns}.${value}`) }));

  // Empty number inputs hand back null or undefined depending on the browser path.
  const num = (v: number | null | undefined) => (v == null || Number.isNaN(v) ? null : v);

  function draft(row: InspectionRow | null) {
    return {
      date: row ? toDateInput(row.date) : todayLocal(),
      weather: row?.weather ?? '',
      queen_seen: !!row?.queen_seen,
      eggs_seen: !!row?.eggs_seen,
      covered_larva: !!row?.covered_larva,
      youngest_larva: row ? reading(row, 'youngest_larva') : null,
      frames: row ? reading(row, 'frames') : null,
      brood_frames: row ? reading(row, 'brood_frames') : null,
      queen_cells: row ? reading(row, 'queen_cells') : null,
      stores: row ? (level(row, 'stores') ?? 0) : 0,
      temperament: row ? (level(row, 'temperament') ?? 0) : 0,
      calmness: row ? (level(row, 'calmness') ?? 0) : 0,
      varroa: row?.varroa ?? '',
      weight_kg: row ? reading(row, 'weight_kg') : null,
      temp_hive: row?.temp_hive ?? null,
      temp_outside: row?.temp_outside ?? null,
      humidity_hive: row?.humidity_hive ?? null,
      humidity_outside: row?.humidity_outside ?? null,
      fed_kg: row ? reading(row, 'fed_kg') : null,
      frames_added: row ? reading(row, 'frames_added') : null,
      frames_removed: row ? reading(row, 'frames_removed') : null,
      drone_frame_cut: !!row?.drone_frame_cut,
      super_added: !!row?.super_added,
      note: row?.note ?? ''
    };
  }

  // The parent remounts the form per visit, so the draft is built once.
  let v = $state(untrack(() => draft(editing)));
  // Photos chosen here; they are stored once the visit itself is saved.
  let picked = $state<PreparedPhoto[]>([]);
  // Folded away unless the visit being edited has something in them.
  let climateOpen = $state(CLIMATE_KEYS.some((k) => v[k] !== null));
  let activitiesOpen = $state(
    v.fed_kg !== null ||
      v.frames_added !== null ||
      v.frames_removed !== null ||
      v.drone_frame_cut ||
      v.super_added
  );

  const saver = createSaver();
  const { busy, error } = saver;

  function startFromLast() {
    if (last) Object.assign(v, carryOver(last));
  }

  function fields() {
    return {
      date: storedDate(v.date, editing?.date),
      weather: v.weather.trim(),
      queen_seen: v.queen_seen ? 1 : 0,
      eggs_seen: v.eggs_seen ? 1 : 0,
      covered_larva: v.covered_larva ? 1 : 0,
      youngest_larva: num(v.youngest_larva),
      frames: num(v.frames),
      brood_frames: num(v.brood_frames),
      queen_cells: num(v.queen_cells),
      stores: v.stores,
      temperament: v.temperament,
      calmness: v.calmness,
      varroa: v.varroa.trim(),
      weight_kg: num(v.weight_kg),
      temp_hive: num(v.temp_hive),
      temp_outside: num(v.temp_outside),
      humidity_hive: num(v.humidity_hive),
      humidity_outside: num(v.humidity_outside),
      fed_kg: num(v.fed_kg),
      frames_added: num(v.frames_added),
      frames_removed: num(v.frames_removed),
      drone_frame_cut: v.drone_frame_cut ? 1 : 0,
      super_added: v.super_added ? 1 : 0,
      note: v.note.trim()
    };
  }

  // What the form showed when it was opened; a correction saves only what differs.
  const opened = untrack(() => (editing ? fields() : null));

  async function save() {
    if (!v.date) return;
    const ok = await saver.run(async () => {
      let id = editing?.id;
      if (editing && opened) {
        const now = fields();
        const changes: Record<string, unknown> = changedFrom(opened, now);
        // Saved with this form, every count is explicit: empty or a number.
        // When that changes how the stored counts read (an older visit,
        // where 0 meant "not recorded"), all counts are written with it.
        const source = isSensor(editing) ? InspectionSource.SENSOR : InspectionSource.MANUAL;
        if (Object.keys(changes).length && source !== editing.source) {
          for (const k of COUNT_KEYS) changes[k] = now[k];
          changes.source = source;
        }
        await editInspection(editing.id, changes);
      } else {
        id = await recordInspection(hiveId, fields());
      }
      if (id) await attachVisitPhotos(id, picked);
    });
    if (ok) onsaved();
  }
</script>

<form
  class="hv-card hv-form"
  onsubmit={(e) => {
    e.preventDefault();
    save();
  }}
>
  <div class="top">
    <h3>{editing ? $_('insp.edit') : $_('insp.record')}</h3>
    {#if !editing && last}
      <button type="button" class="hv-btn ghost" onclick={startFromLast}>
        <History size={18} aria-hidden="true" />
        {$_('insp.from_last')}
        <span class="hv-muted hv-sm">{formatDate(last.date, $locale)}</span>
      </button>
    {/if}
  </div>

  <div class="hv-grid2">
    <label class="hv-field">
      <span>{$_('insp.date')}</span>
      <input type="date" bind:value={v.date} max={todayLocal()} required />
    </label>
    <label class="hv-field">
      <span>{$_('insp.weather')}</span>
      <input bind:value={v.weather} placeholder={$_('insp.weather_ph')} />
    </label>
  </div>

  <h4>{$_('insp.colony')}</h4>
  <div class="hv-checks">
    <label class="hv-check">
      <input type="checkbox" bind:checked={v.queen_seen} />
      {$_('insp.queen_seen')}
    </label>
    <label class="hv-check">
      <input type="checkbox" bind:checked={v.eggs_seen} />
      {$_('insp.eggs_seen')}
    </label>
    <label class="hv-check">
      <input type="checkbox" bind:checked={v.covered_larva} />
      {$_('insp.covered_larva')}
    </label>
  </div>
  <div class="hv-grid2">
    <Stepper bind:value={v.frames} label={$_('insp.frames')} />
    <Stepper bind:value={v.brood_frames} label={$_('insp.brood_frames')} />
    <Stepper bind:value={v.queen_cells} label={$_('insp.queen_cells')} />
    <Stepper
      bind:value={v.youngest_larva}
      label={`${$_('insp.youngest_larva')} (${$_('insp.unit_days')})`}
    />
  </div>
  <Segmented
    bind:value={v.stores}
    label={$_('insp.stores')}
    options={options('storeslvl', STORES)}
  />
  <Segmented
    bind:value={v.temperament}
    label={$_('insp.temperament')}
    options={options('temperament', TEMPERAMENTS)}
  />
  <Segmented
    bind:value={v.calmness}
    label={$_('insp.calmness')}
    options={options('calmlvl', CALM)}
  />
  <div class="hv-grid2">
    <label class="hv-field">
      <span>{$_('insp.varroa')}</span>
      <input bind:value={v.varroa} placeholder={$_('insp.varroa_ph')} />
      <span class="hv-hint">{$_('insp.varroa_hint')}</span>
    </label>
    <label class="hv-field">
      <span>{$_('insp.weight_kg')} ({$_('common.kg')})</span>
      <input type="number" inputmode="decimal" min="0" step="0.1" bind:value={v.weight_kg} />
    </label>
  </div>

  <details bind:open={climateOpen}>
    <summary>{$_('insp.climate')}</summary>
    <!-- No inputmode on temperatures: the decimal keypad of iOS has no minus key. -->
    <div class="hv-grid2">
      <label class="hv-field">
        <span>{$_('insp.temp_hive')} (°C)</span>
        <input type="number" step="0.1" bind:value={v.temp_hive} />
      </label>
      <label class="hv-field">
        <span>{$_('insp.temp_outside')} (°C)</span>
        <input type="number" step="0.1" bind:value={v.temp_outside} />
      </label>
      <label class="hv-field">
        <span>{$_('insp.humidity_hive')} (%)</span>
        <input
          type="number"
          inputmode="decimal"
          min="0"
          max="100"
          step="1"
          bind:value={v.humidity_hive}
        />
      </label>
      <label class="hv-field">
        <span>{$_('insp.humidity_outside')} (%)</span>
        <input
          type="number"
          inputmode="decimal"
          min="0"
          max="100"
          step="1"
          bind:value={v.humidity_outside}
        />
      </label>
    </div>
  </details>

  <details bind:open={activitiesOpen}>
    <summary>{$_('insp.activities')}</summary>
    <div class="hv-grid2">
      <label class="hv-field">
        <span>{$_('insp.fed_kg')} ({$_('common.kg')})</span>
        <input type="number" inputmode="decimal" min="0" step="0.1" bind:value={v.fed_kg} />
      </label>
      <Stepper bind:value={v.frames_added} label={$_('insp.frames_added')} />
      <Stepper bind:value={v.frames_removed} label={$_('insp.frames_removed')} />
    </div>
    <div class="hv-checks">
      <label class="hv-check">
        <input type="checkbox" bind:checked={v.drone_frame_cut} />
        {$_('insp.drone_frame_cut')}
      </label>
      <label class="hv-check">
        <input type="checkbox" bind:checked={v.super_added} />
        {$_('insp.super_added')}
      </label>
    </div>
  </details>

  <label class="hv-field">
    <span>{$_('insp.note')}</span>
    <textarea bind:value={v.note} rows="3" placeholder={$_('insp.note_ph')}></textarea>
  </label>

  <Photos
    kind="picker"
    visitId={editing?.id}
    label={editing ? formatDate(editing.date, $locale) : ''}
    bind:picked
  />

  <div class="savebar">
    <FormFooter busy={$busy} error={$error} invalid={!v.date} {oncancel} />
  </div>
</form>

<style>
  .top {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: center;
    gap: 10px;
  }
  h3 {
    font-size: 1.15rem;
  }
  h4,
  summary {
    font-family: inherit;
    font-size: 0.86rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-soft);
    font-weight: 700;
  }
  details {
    border-top: 1px solid var(--border);
  }
  summary {
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 48px;
    cursor: pointer;
    list-style: none;
  }
  summary::-webkit-details-marker {
    display: none;
  }
  summary::after {
    content: '+';
    font-size: 1.4rem;
    font-weight: 400;
    line-height: 1;
  }
  details[open] summary::after {
    content: '−';
  }
  summary:focus-visible {
    outline: 2px solid var(--accent-text);
    outline-offset: 2px;
  }
  details > :global(div) {
    margin-bottom: 14px;
  }

  /* Stays in view while the form scrolls: above the tab bar on phones. */
  .savebar {
    position: sticky;
    bottom: calc(74px + env(safe-area-inset-bottom));
    z-index: 5;
    display: grid;
    gap: 8px;
    margin: 0 -16px -16px;
    padding: 10px 16px;
    background: var(--surface);
    border-top: 1px solid var(--border);
    border-radius: 0 0 16px 16px;
  }
  @media (min-width: 900px) {
    .savebar {
      bottom: 0;
    }
  }
</style>
