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
