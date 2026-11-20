// Units for showing and entering weights and temperatures. Records, sync
// payloads, exports and the API always hold metric values (kg, °C); the
// conversion happens only on screen, here. Numbers are written and read with
// the decimal separator of the app's language.

export type WeightUnit = 'kg' | 'lb';
export type TempUnit = 'c' | 'f';
export type Units = { weight: WeightUnit; temp: TempUnit };
/** What a value measures. */
export type Quantity = 'weight' | 'temp';

export const METRIC: Units = { weight: 'kg', temp: 'c' };
export const WEIGHT_UNITS: readonly WeightUnit[] = ['kg', 'lb'];
export const TEMP_UNITS: readonly TempUnit[] = ['c', 'f'];

export const isWeightUnit = (v: unknown): v is WeightUnit => v === 'kg' || v === 'lb';
export const isTempUnit = (v: unknown): v is TempUnit => v === 'c' || v === 'f';

const KG_PER_LB = 0.45359237; // exact, by definition of the pound

// Regions that weigh in pounds, and those that measure in Fahrenheit.
const POUND_REGIONS = new Set(['US', 'LR', 'MM']);
const FAHRENHEIT_REGIONS = new Set(['US', 'LR']);

function regionOf(tag: string | undefined): string {
  try {
    return tag ? (new Intl.Locale(tag).region ?? '') : '';
  } catch {
    return ''; // not a language tag
  }
}

/**
 * The units a device uses before the account says otherwise: pounds and
 * Fahrenheit where the browser's locale names a region that uses them
 * ("en-US"), metric everywhere else, also for a tag without a region ("en").
 */
export function localeUnits(tag: string | undefined): Units {
  const region = regionOf(tag);
  return {
    weight: POUND_REGIONS.has(region) ? 'lb' : 'kg',
    temp: FAHRENHEIT_REGIONS.has(region) ? 'f' : 'c'
  };
}

/** The unit symbol shown after a value: kg, lb, °C or °F. */
export function unitSymbol(q: Quantity, u: Units): string {
  if (q === 'weight') return u.weight;
  return u.temp === 'f' ? '°F' : '°C';
}

// Fraction digits shown: weights to 10 g / a hundredth of a pound,
// temperatures to a tenth of a degree.
const SHOWN_DIGITS: Record<Quantity, number> = { weight: 2, temp: 1 };
// Fraction digits kept when a value entered in pounds or Fahrenheit is
// stored in metric: fine enough that it reads back as entered.
const STORED_DIGITS = 4;

// Rounds to `digits` decimals; never returns -0 (a frost of -0.04 °C
// shown to one decimal is 0, not "-0").
function round(v: number, digits: number): number {
  const f = 10 ** digits;
  return Math.round(v * f) / f || 0;
}

/** A metric value in the chosen unit, unrounded. */
export function fromMetric(q: Quantity, metric: number, u: Units): number {
  if (q === 'weight') return u.weight === 'lb' ? metric / KG_PER_LB : metric;
  return u.temp === 'f' ? (metric * 9) / 5 + 32 : metric;
}

/**
 * A value in the chosen unit as the metric value to store. A metric entry is
 * stored as entered; a converted one is rounded to 4 decimals.
 */
export function toMetric(q: Quantity, shown: number, u: Units): number {
  if (q === 'weight') return u.weight === 'lb' ? round(shown * KG_PER_LB, STORED_DIGITS) : shown;
  return u.temp === 'f' ? round(((shown - 32) * 5) / 9, STORED_DIGITS) : shown;
}

/** A number in the conventions of `locale`, with at most `digits` decimals. */
export function formatNumber(
  n: number,
  locale: string | null | undefined,
  digits: number,
  grouping = true
): string {
  const r = round(n, digits);
  try {
    return new Intl.NumberFormat(locale ?? undefined, {
      maximumFractionDigits: digits,
      useGrouping: grouping
    }).format(r);
  } catch {
    return String(r); // unknown locale
  }
}

/**
 * A stored metric value as text in the chosen unit with its symbol, e.g.
 * "34,5 kg" or "76.06 lb". `digits` overrides the fraction digits shown.
 */
export function formatQuantity(
  q: Quantity,
  metric: number,
  u: Units,
  locale: string | null | undefined,
  digits = SHOWN_DIGITS[q]
): string {
  return `${formatNumber(fromMetric(q, metric, u), locale, digits)} ${unitSymbol(q, u)}`;
}

/** A stored metric value as a bare number in the chosen unit (tables, charts). */
export function formatValue(
  q: Quantity,
  metric: number,
  u: Units,
  locale: string | null | undefined
): string {
  return formatNumber(fromMetric(q, metric, u), locale, SHOWN_DIGITS[q]);
}

/** A stored metric value converted for a chart axis (rounded as shown). */
export const chartValue = (q: Quantity, metric: number, u: Units): number =>
  round(fromMetric(q, metric, u), SHOWN_DIGITS[q]);

function decimalSeparator(locale: string | null | undefined): string {
  try {
    const part = new Intl.NumberFormat(locale ?? undefined)
      .formatToParts(1.5)
      .find((p) => p.type === 'decimal');
    return part?.value ?? '.';
  } catch {
    return '.';
  }
}

/**
 * Reads a number typed by the user: null when the text is empty, undefined
 * when it is not a number. Either "." or "," is taken as the decimal
 * separator; where both occur, the last one is, and the other one groups
 * thousands. Several of one kind group thousands when that is the language's
 * grouping character ("1.234.567" in German).
 */
export function parseNumber(text: string, locale?: string | null): number | null | undefined {
  // \s covers the no-break spaces some languages group thousands with.
  let s = text.replace(/[\s']/g, '').replace(/^\u2212/, '-');
  if (s === '') return null;
  const dot = s.lastIndexOf('.');
  const comma = s.lastIndexOf(',');
  if (dot >= 0 && comma >= 0) {
    const dec = dot > comma ? '.' : ',';
    s = s.split(dec === '.' ? ',' : '.').join('');
    s = s.replace(dec, '.');
  } else if (dot >= 0 || comma >= 0) {
    const sep = dot >= 0 ? '.' : ',';
    if (s.split(sep).length > 2) {
      // Repeated: only the language's grouping character can repeat.
      if (decimalSeparator(locale) === sep) return undefined;
      s = s.split(sep).join('');
    } else {
      s = s.replace(sep, '.');
    }
  }
  if (!/^[-+]?(\d+\.?\d*|\.\d+)$/.test(s)) return undefined;
  const n = Number(s);
  return Number.isFinite(n) ? n : undefined;
}

/**
 * The state of one input for a weight or temperature: the text in the field
 * and what it started as. Reading it back (readField) returns the stored
 * value unchanged as long as the text is untouched, so opening a record and
 * saving it again never changes a number by converting it twice.
 */
export type QuantityField = {
  text: string;
  readonly shown: string;
  readonly metric: number | null;
};

export function quantityField(
  q: Quantity,
  metric: number | null | undefined,
  u: Units,
  locale: string | null | undefined
): QuantityField {
  const shown =
    metric == null || !Number.isFinite(metric)
      ? ''
      : formatNumber(fromMetric(q, metric, u), locale, SHOWN_DIGITS[q], false);
  return { text: shown, shown, metric: metric ?? null };
}

/**
 * The metric value to store for a field: null when it is empty, undefined
 * when the text is not a number (or a weight below zero).
 */
export function readField(
  q: Quantity,
  f: QuantityField,
  u: Units,
  locale: string | null | undefined
): number | null | undefined {
  if (f.text.trim() === f.shown) return f.shown === '' ? null : f.metric;
  const n = parseNumber(f.text, locale);
  if (n == null) return n;
  return q === 'weight' && n < 0 ? undefined : toMetric(q, n, u);
}
