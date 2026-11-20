// Small helpers shared by the record forms.
import { fromDateInput, toDateInput } from '#lib/dates.js';

/**
 * The timestamp to store for a date input when correcting a record: an
 * unchanged day keeps its stored time (so the edit does not touch the date),
 * a new day is stored as local noon. '' stays ''.
 */
export function storedDate(input: string, before?: string | null): string {
  return before && toDateInput(before) === input ? before : fromDateInput(input);
}

/** Joins the parts that have a value with a middle dot: "Blue · own breeding". */
export function dotted(...parts: (string | number | null | undefined | false)[]): string {
  return parts.filter((p) => p !== null && p !== undefined && p !== false && p !== '').join(' · ');
}

/**
 * The fields of `now` whose value differs from `opened`, the values the form
 * was opened with. An edit form saves only these: a field the user did not
 * touch is not written, so it cannot overwrite a change made to it on another
 * device in the meantime (fields merge one by one, the newest write wins).
 * An empty string and an unset value count as equal.
 */
export function changedFrom<T extends Record<string, unknown>>(opened: T, now: T): Partial<T> {
  const norm = (v: unknown) => (v === '' || v === undefined ? null : v);
  const out: Partial<T> = {};
  for (const key of Object.keys(now) as (keyof T)[]) {
    if (norm(opened[key]) !== norm(now[key])) out[key] = now[key];
  }
  return out;
}
