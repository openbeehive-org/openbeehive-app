// Pure helpers behind the hive lists and the dashboard: natural name order,
// search / filter / sort, "days since the last visit", the due list and the
// first-run checklist. No database access here, so all of it is unit-tested.

import { parseStored } from './dates';
import { shortCode } from './qr';

// One row of hives.overview(): the hive plus what the lists show next to it.
export type HiveOverview = {
  id: string;
  apiary_id: string | null;
  name: string;
  status: number | null;
  photo: string | null;
  // null when the hive's apiary is deleted or was never synced to this device.
  apiary_name: string | null;
  // Date of the last visit a person recorded; sensor readings do not count.
  last_inspection: string | null;
  queen_year: number | null;
  queen_marking: number | null;
};

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

/** Name order as people expect it: "Hive 2" before "Hive 10", case ignored. */
export function naturalCompare(a: string | null | undefined, b: string | null | undefined): number {
  return collator.compare(a ?? '', b ?? '');
}

/** Sorts rows by their name in natural order (in place) and returns them. */
export function byName<T extends { name?: unknown }>(rows: T[]): T[] {
  return rows.sort((a, b) => naturalCompare(String(a.name ?? ''), String(b.name ?? '')));
}

const DAY_MS = 86_400_000;
const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();

/**
 * Whole calendar days between a stored date and today; null when there is no
 * (valid) date. A visit recorded today is 0 at any hour, never negative.
 */
export function daysSince(value: string | null | undefined, now: Date = new Date()): number | null {
  if (!value) return null;
  const d = parseStored(value);
  if (!d) return null;
  return Math.max(0, Math.round((startOfDay(now) - startOfDay(d)) / DAY_MS));
}

// Filter value for hives whose apiary no longer exists.
export const NO_APIARY = '-';

export type HiveSort = 'name' | 'apiary' | 'inspected';
export type HiveFilter = {
  query?: string;
  // '' = any status, otherwise the HiveStatus number as a string.
  status?: string;
  // '' = any apiary, NO_APIARY = hives without one, otherwise an apiary id.
  apiary?: string;
};

// Lower-case and strip accents, so "volker" finds "Völker".
const fold = (s: string) => s.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();

/**
 * Hives matching the search text and filters. The text is matched against
 * name and apiary (every word must occur), or, typed as one piece of three
 * or more characters, against the start of the code printed on the label.
 */
export function filterHives<T extends HiveOverview>(rows: T[], f: HiveFilter): T[] {
  const words = fold(f.query ?? '')
    .split(/\s+/)
    .filter(Boolean);
  const code = words.length === 1 && (words[0] ?? '').length >= 3 ? words[0] : undefined;
  return rows.filter((h) => {
    if (f.status && String(h.status ?? 0) !== f.status) return false;
    if (f.apiary === NO_APIARY ? h.apiary_name !== null : f.apiary && h.apiary_id !== f.apiary)
      return false;
    if (words.length === 0) return true;
    if (code && shortCode(h.id).toLowerCase().startsWith(code)) return true;
    const haystack = fold(`${h.name} ${h.apiary_name ?? ''}`);
    return words.every((w) => haystack.includes(w));
  });
}

// Longest since the last visit first; never visited counts as longest.
function byNeglect(a: HiveOverview, b: HiveOverview, now: Date): number {
  const da = daysSince(a.last_inspection, now);
  const db = daysSince(b.last_inspection, now);
  if (da === db) return naturalCompare(a.name, b.name);
  if (da === null) return -1;
  if (db === null) return 1;
  return db - da;
}

/** A sorted copy of the rows. */
export function sortHives<T extends HiveOverview>(
  rows: T[],
  sort: HiveSort,
  now: Date = new Date()
): T[] {
  const out = [...rows];
  if (sort === 'inspected') return out.sort((a, b) => byNeglect(a, b, now));
  if (sort === 'apiary')
    return out.sort(
      (a, b) =>
        // Hives without an apiary go last.
        Number(a.apiary_name === null) - Number(b.apiary_name === null) ||
        naturalCompare(a.apiary_name, b.apiary_name) ||
        naturalCompare(a.name, b.name)
    );
  return out.sort((a, b) => naturalCompare(a.name, b.name));
}

// Lost and dissolved colonies cannot be inspected any more.
const GONE = new Set([4, 5]);

export type DueHive = { id: string; name: string; apiary: string | null; days: number | null };

/** The hives waiting longest for a visit (days = null: never visited). */
export function dueHives(rows: HiveOverview[], now: Date = new Date(), limit = 5): DueHive[] {
  return rows
    .filter((h) => !GONE.has(h.status ?? 0))
    .sort((a, b) => byNeglect(a, b, now))
    .slice(0, limit)
    .map((h) => ({
      id: h.id,
      name: h.name,
      apiary: h.apiary_name,
      days: daysSince(h.last_inspection, now)
    }));
}

export type FirstRunStep = {
  key: 'apiary' | 'hive' | 'visit';
  done: boolean;
  // The first step still open: the only one that is offered as a link.
  current: boolean;
  href: string;
};

/**
 * The getting-started checklist: create an apiary, add a hive, record the
 * first visit. `firstHiveId` lets the last step open that hive directly.
 */
export function firstRunSteps(s: {
  apiaries: number;
  hives: number;
  visits: number;
  firstHiveId?: string;
}): { steps: FirstRunStep[]; complete: boolean } {
  const done = {
    apiary: s.apiaries > 0,
    hive: s.hives > 0,
    visit: s.visits > 0
  };
  const href = {
    apiary: '/apiaries?new=1',
    hive: '/hives?new=1',
    visit: s.firstHiveId ? `/hives/${s.firstHiveId}` : '/hives'
  };
  let open = true; // no open step seen yet
  const steps = (['apiary', 'hive', 'visit'] as const).map((key) => {
    const current = !done[key] && open;
    if (!done[key]) open = false;
    return { key, done: done[key], current, href: href[key] };
  });
  return { steps, complete: steps.every((x) => x.done) };
}
