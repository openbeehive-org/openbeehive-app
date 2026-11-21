import { describe, expect, it } from 'vitest';
import {
  chartValue,
  formatQuantity,
  fromMetric,
  localeUnits,
  defaultUnits,
  METRIC,
  parseNumber,
  quantityField,
  readField,
  toMetric,
  unitSymbol,
  type Quantity,
  type Units
} from './units';

const IMPERIAL: Units = { weight: 'lb', temp: 'f' };
const ALL: Units[] = [METRIC, IMPERIAL, { weight: 'lb', temp: 'c' }, { weight: 'kg', temp: 'f' }];
const LOCALES = ['en', 'en-US', 'de', 'fr', 'es', 'it'];

// Values from far below to far above anything a beekeeper records, with
// awkward fractions in between.
function* samples(q: Quantity): Generator<number> {
  const lo = q === 'temp' ? -60 : 0;
  const hi = q === 'temp' ? 80 : 2500;
  for (let i = 0; i <= 4000; i++) yield lo + ((hi - lo) * i) / 4000 + (i % 7) * 0.00137;
  yield 0;
  yield 0.01;
  yield 1e-9;
  if (q === 'temp') yield -0.04;
}

describe('conversion', () => {
  it('knows the exact factors', () => {
    expect(fromMetric('weight', 0.45359237, IMPERIAL)).toBeCloseTo(1, 12);
    expect(fromMetric('temp', 100, IMPERIAL)).toBe(212);
    expect(fromMetric('temp', -40, IMPERIAL)).toBe(-40);
    expect(toMetric('temp', 212, IMPERIAL)).toBe(100);
    expect(toMetric('weight', 1, IMPERIAL)).toBe(0.4536);
  });

  it('leaves metric values alone', () => {
    expect(toMetric('weight', 34.567891, METRIC)).toBe(34.567891);
    expect(fromMetric('temp', -3.25, METRIC)).toBe(-3.25);
  });

  it('round-trips over a wide range: what is shown reads back to the same display', () => {
    for (const u of ALL) {
      for (const q of ['weight', 'temp'] as const) {
        for (const m of samples(q)) {
          const shown = chartValue(q, m, u);
          const stored = toMetric(q, shown, u);
          // Stored back, the value shows exactly as before ...
          expect(chartValue(q, stored, u), `${q} ${m} in ${u.weight}/${u.temp}`).toBe(shown);
          // ... and lies within half a shown step of the original (plus
          // the 4 decimals kept of a converted value).
          const step = q === 'weight' ? 0.01 : 0.1;
          expect(Math.abs(fromMetric(q, stored, u) - fromMetric(q, m, u))).toBeLessThanOrEqual(
            step / 2 + 2e-4
          );
        }
      }
    }
  });
});

describe('formatting', () => {
  it('uses the language decimal separator and the unit symbol', () => {
    expect(formatQuantity('weight', 34.5, METRIC, 'de')).toBe('34,5 kg');
    expect(formatQuantity('weight', 34.5, IMPERIAL, 'en')).toBe('76.06 lb');
    expect(formatQuantity('temp', 35, IMPERIAL, 'fr')).toBe('95 °F');
    expect(formatQuantity('temp', -0.04, METRIC, 'en')).toBe('0 °C');
    expect(formatQuantity('weight', 1234.5, METRIC, 'en')).toBe('1,234.5 kg');
    expect(formatQuantity('weight', 1234.56, IMPERIAL, 'de', 0)).toBe('2.722 lb');
  });

  it('names the symbols', () => {
    expect([unitSymbol('weight', IMPERIAL), unitSymbol('temp', IMPERIAL)]).toEqual(['lb', '°F']);
    expect([unitSymbol('weight', METRIC), unitSymbol('temp', METRIC)]).toEqual(['kg', '°C']);
  });
});

describe('parseNumber', () => {
  it.each([
    ['', 'de', null],
    ['  ', 'en', null],
    ['12', 'en', 12],
    ['12.5', 'en', 12.5],
    ['12,5', 'de', 12.5],
    ['12,5', 'en', 12.5],
    ['12.5', 'de', 12.5],
    ['-3,5', 'fr', -3.5],
    ['−4.5', 'en', -4.5],
    ['1.234,5', 'de', 1234.5],
    ['1,234.5', 'en', 1234.5],
    ['1 234,5', 'fr', 1234.5],
    ['1.234.567', 'de', 1234567],
    ['1.234.567', 'en', undefined],
    [',5', 'de', 0.5],
    ['12 kg', 'en', undefined],
    ['abc', 'en', undefined],
    ['1e3', 'en', undefined],
    ['--1', 'en', undefined]
  ])('%s in %s', (text, locale, want) => {
    expect(parseNumber(text, locale)).toBe(want);
  });
});

describe('fields', () => {
  it('stores the identical metric number when the text is not touched', () => {
    for (const u of ALL) {
      for (const locale of LOCALES) {
        for (const q of ['weight', 'temp'] as const) {
          for (const m of [34.567891, 0.1, 12.3456, 2.2, -7.77, 0, 41.5]) {
            if (q === 'weight' && m < 0) continue;
            const f = quantityField(q, m, u, locale);
            expect(readField(q, f, u, locale)).toBe(m);
            f.text = ` ${f.text} `; // stray spaces are not an edit either
            expect(readField(q, f, u, locale)).toBe(m);
          }
        }
      }
    }
  });

  it('reads an entry in pounds and Fahrenheit as metric', () => {
    const w = quantityField('weight', null, IMPERIAL, 'en-US');
    expect(w.text).toBe('');
    w.text = '76.1';
    expect(readField('weight', w, IMPERIAL, 'en-US')).toBe(34.5184);
    const t = quantityField('temp', null, IMPERIAL, 'de');
    t.text = '98,6';
    expect(readField('temp', t, IMPERIAL, 'de')).toBe(37);
  });

  it('shows a stored value in the chosen unit and reads it back after an edit', () => {
    const f = quantityField('weight', 34.5, IMPERIAL, 'de');
    expect(f.text).toBe('76,06');
    f.text = '80';
    expect(readField('weight', f, IMPERIAL, 'de')).toBe(36.2874);
  });

  it('tells empty from unreadable, and refuses a negative weight', () => {
    const f = quantityField('weight', 12, METRIC, 'en');
    f.text = '';
    expect(readField('weight', f, METRIC, 'en')).toBeNull();
    f.text = 'twelve';
    expect(readField('weight', f, METRIC, 'en')).toBeUndefined();
    f.text = '-1';
    expect(readField('weight', f, METRIC, 'en')).toBeUndefined();
    const t = quantityField('temp', null, METRIC, 'en');
    t.text = '-1';
    expect(readField('temp', t, METRIC, 'en')).toBe(-1);
  });
});

describe('localeUnits', () => {
  it.each([
    ['en-US', IMPERIAL],
    ['es-US', IMPERIAL],
    ['en-LR', IMPERIAL],
    ['my-MM', { weight: 'lb', temp: 'c' }],
    ['en', METRIC],
    ['en-GB', METRIC],
    ['de-DE', METRIC],
    ['fr', METRIC],
    ['not a tag', METRIC],
    [undefined, METRIC]
  ])('%s', (tag, want) => {
    expect(localeUnits(tag)).toEqual(want);
  });
});

describe('defaultUnits', () => {
  it.each([
    // App language, browser locale, expected
    ['de', 'en-US', METRIC],
    ['fr', 'en-US', METRIC],
    ['en', 'en-US', IMPERIAL],
    ['en', 'en-GB', METRIC],
    ['en-US', 'de-DE', IMPERIAL],
    ['es', 'es-US', IMPERIAL],
    ['', 'en-US', IMPERIAL],
    [undefined, 'de-DE', METRIC]
  ])('app %s, browser %s', (app, browser, want) => {
    expect(defaultUnits(app, browser)).toEqual(want);
  });
});
