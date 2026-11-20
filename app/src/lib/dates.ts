// Calendar dates the way a beekeeper means them: "the 14th" is the 14th in
// the device's time zone, on every device that later reads the record.
//
// Records store an ISO timestamp. A date-only entry is stored as local noon,
// so it stays on the same calendar day for readers up to twelve hours east or
// west of the writer, where UTC midnight used to slip to the previous day west
// of Greenwich.

const pad = (n: number) => String(n).padStart(2, '0');

/** Value for `<input type="date">`: the local calendar day of `d`. */
export function toDateInput(d: Date | string | null | undefined): string {
  if (!d) return '';
  const date = typeof d === 'string' ? parseStored(d) : d;
  if (!date || Number.isNaN(date.getTime())) return '';
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/** Today's local calendar day as a date-input value. */
export function todayLocal(now: Date = new Date()): string {
  return toDateInput(now);
}

/** ISO timestamp to store for a date-input value (local noon); '' stays ''. */
export function fromDateInput(value: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!m) return '';
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 12, 0, 0, 0).toISOString();
}

/**
 * Parses a stored value. Bare `YYYY-MM-DD` strings (older records, imports)
 * are read as a local calendar day rather than as UTC midnight.
 */
export function parseStored(value: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  const d = m ? new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 12) : new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** A stored date formatted for the UI language (not the browser language). */
export function formatDate(
  value: string | null | undefined,
  locale: string | null | undefined,
  options: Intl.DateTimeFormatOptions = { dateStyle: 'medium' }
): string {
  if (!value) return '';
  const d = parseStored(value);
  if (!d) return '';
  return new Intl.DateTimeFormat(locale || undefined, options).format(d);
}
