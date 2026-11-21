// How the hive screens read an inspection row: which values count as
// recorded, how a visit is summarised, and what the charts plot.
import type { InspectionRow } from '../../../routes/(app)/rows.js';
import { formatQuantity, METRIC, type Quantity, type Units } from '../../units.js';

const MANUAL = 1;
const SENSOR = 2;

/** A reading delivered by a sensor rather than recorded on a visit. */
export const isSensor = (row: Pick<InspectionRow, 'source'>) => row.source === SENSOR;

/** Counts and amounts: empty means "not recorded", 0 means "observed none". */
export const COUNT_KEYS = [
  'youngest_larva',
  'frames',
  'brood_frames',
  'queen_cells',
  'fed_kg',
  'frames_added',
  'frames_removed',
  'weight_kg'
] as const;
export const CLIMATE_KEYS = [
  'temp_hive',
  'temp_outside',
  'humidity_hive',
  'humidity_outside'
] as const;
export type CountKey = (typeof COUNT_KEYS)[number];
export type ClimateKey = (typeof CLIMATE_KEYS)[number];
export type NumericKey = CountKey | ClimateKey;

const isClimate = (key: NumericKey): key is ClimateKey =>
  (CLIMATE_KEYS as readonly string[]).includes(key);

/**
 * The recorded value of a numeric field, or null when nothing was recorded.
 *
 * Climate values were always optional, so any stored number counts (0 °C and
 * frost included). Counts are different: visits saved by older app versions
 * stored 0 for every field left empty, so a 0 only means "observed none" on
 * a visit recorded with the current form (source = manual).
 */
export function reading(row: InspectionRow, key: NumericKey): number | null {
  const raw = row[key];
  if (raw == null) return null;
  const v = Number(raw);
  if (Number.isNaN(v)) return null;
  if (v === 0 && !isClimate(key) && row.source !== MANUAL) return null;
  return v;
}

/** A varroa entry that is a plain number ("3", "2,5"); anything else is null. */
export function parseVarroa(text: string | null | undefined): number | null {
  const m = /^\s*(\d+(?:[.,]\d+)?)\s*$/.exec(text ?? '');
  return m?.[1] ? Number(m[1].replace(',', '.')) : null;
}

// --- Ratings: stored as codes, shown as named levels ---

export type OrdinalKey = 'stores' | 'temperament' | 'calmness';

/** Rating scales with their levels ordered from best to worst. */
export const ORDINALS: Record<OrdinalKey, { ns: string; bestFirst: number[] }> = {
  stores: { ns: 'storeslvl', bestFirst: [1, 2, 3, 4] }, // good ... none
  temperament: { ns: 'temperament', bestFirst: [1, 2, 3, 4, 5] }, // very gentle ... aggressive
  calmness: { ns: 'calmlvl', bestFirst: [4, 3, 2, 1] } // very calm ... runs off comb
};

/** The recorded level of a rating, or null (0 = unspecified). */
export function level(row: InspectionRow, key: OrdinalKey): number | null {
  const v = Number(row[key] ?? 0);
  return ORDINALS[key].bestFirst.includes(v) ? v : null;
}

// --- Visit summary ---

type Translate = (key: string) => string;

/** What a numeric field measures, for those shown in the chosen units. */
export function quantityOf(key: NumericKey): Quantity | null {
  if (key === 'fed_kg' || key === 'weight_kg') return 'weight';
  if (key === 'temp_hive' || key === 'temp_outside') return 'temp';
  return null;
}

/** Compact chips summarising a recorded visit, weights and temperatures in `u`. */
export function visitChips(
  i: InspectionRow,
  t: Translate,
  u: Units = METRIC,
  locale?: string | null
): string[] {
  const out: string[] = [];
  const qty = (q: Quantity, v: number) => formatQuantity(q, v, u, locale);
  const n = (key: NumericKey) => reading(i, key);
  const withValue = (key: NumericKey, text: (v: number) => string) => {
    const v = n(key);
    if (v !== null) out.push(text(v));
  };
  const rated = (key: OrdinalKey, label: string) => {
    const v = level(i, key);
    if (v !== null) out.push(`${t(label)}: ${t(`${ORDINALS[key].ns}.${v}`)}`);
  };

  if (i.queen_seen) out.push(t('insp.queen_seen'));
  if (i.eggs_seen) out.push(t('insp.eggs_seen'));
  withValue('youngest_larva', (v) => `${t('insp.youngest_larva')}: ${v} ${t('insp.unit_d')}`);
  if (i.covered_larva) out.push(t('insp.covered_larva'));
  withValue('frames', (v) => `${v} ${t('insp.frames')}`);
  withValue('brood_frames', (v) => `${v} ${t('insp.brood_frames')}`);
  rated('stores', 'insp.stores');
  withValue('queen_cells', (v) => `${v} ${t('insp.queen_cells')}`);
  rated('temperament', 'insp.temperament');
  rated('calmness', 'insp.calmness');
  withValue('fed_kg', (v) => `${t('insp.fed_kg')} ${qty('weight', v)}`);
  withValue('frames_added', (v) => `+${v} ${t('insp.frames')}`);
  withValue('frames_removed', (v) => `−${v} ${t('insp.frames')}`);
  if (i.drone_frame_cut) out.push(t('insp.drone_frame_cut'));
  if (i.super_added) out.push(t('insp.super_added'));
  withValue('weight_kg', (v) => `${t('insp.weight_kg')} ${qty('weight', v)}`);
  // Honey is recorded as a harvest now; older visits keep showing theirs.
  if (i.honey_kg) out.push(`${t('insp.honey_kg')} ${qty('weight', i.honey_kg)}`);
  withValue('temp_hive', (v) => `${t('insp.temp_hive')} ${qty('temp', v)}`);
  withValue('humidity_hive', (v) => `${t('insp.humidity_hive')} ${v} %`);
  withValue('temp_outside', (v) => `${t('insp.temp_outside')} ${qty('temp', v)}`);
  withValue('humidity_outside', (v) => `${t('insp.humidity_outside')} ${v} %`);
  if (i.varroa) out.push(`${t('insp.varroa')}: ${i.varroa}`);
  return out;
}

/**
 * What "Start from last visit" carries into a new visit: the size of the
 * colony and its ratings, which change slowly. Observations of the day
 * (queen, eggs, swarm cells, varroa, weight), climate, activities, notes and
 * photos always start empty.
 */
export function carryOver(last: InspectionRow) {
  return {
    frames: reading(last, 'frames'),
    brood_frames: reading(last, 'brood_frames'),
    stores: level(last, 'stores') ?? 0,
    temperament: level(last, 'temperament') ?? 0,
    calmness: level(last, 'calmness') ?? 0
  };
}

// --- Chart series (rows oldest to newest) ---

export type Point = { date: string; value: number };

/** Recorded values of a numeric field; negative temperatures included. */
export function numericSeries(rows: InspectionRow[], key: NumericKey): Point[] {
  const out: Point[] = [];
  for (const r of rows) {
    const value = reading(r, key);
    if (value !== null) out.push({ date: r.date, value });
  }
  return out;
}

/**
 * Varroa is free text. Only entries that are a plain number are charted;
 * `skipped` counts the entries that say something else ("3 mites/day").
 */
export function varroaSeries(rows: InspectionRow[]) {
  const points: (Point & { entered: string })[] = [];
  let skipped = 0;
  for (const r of rows) {
    const entered = (r.varroa ?? '').trim();
    if (!entered) continue;
    const value = parseVarroa(entered);
    if (value === null) skipped++;
    else points.push({ date: r.date, value, entered });
  }
  return { points, skipped };
}

/** Recorded levels of a rating, as the stored codes. */
export function levelSeries(rows: InspectionRow[], key: OrdinalKey): Point[] {
  const out: Point[] = [];
  for (const r of rows) {
    const value = level(r, key);
    if (value !== null) out.push({ date: r.date, value });
  }
  return out;
}
