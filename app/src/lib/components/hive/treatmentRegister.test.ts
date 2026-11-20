import { describe, it, expect } from 'vitest';
import type { TreatmentRow } from '../../../routes/(app)/rows.js';
import { treatmentRegisterHtml, withdrawalActive } from './treatmentRegister';

// Local noon, so the calendar day is the same in every time zone.
const noon = (y: number, m: number, d: number) => new Date(y, m - 1, d, 12).toISOString();

const treatment = (over: Partial<TreatmentRow> = {}): TreatmentRow => ({
  id: 't1',
  hive_id: 'h1',
  date: noon(2026, 8, 1),
  product: 'Oxuvar',
  active_ingredient: 'oxalic acid',
  dose: '50 ml',
  method: 'trickling',
  batch_number: 'B-17',
  withdrawal_until: noon(2026, 8, 20),
  reason: 'varroa',
  note: null,
  amended_at: null,
  ...over
});

const t = (key: string, o?: { values?: Record<string, string | number> }) =>
  o?.values ? `${key}(${Object.values(o.values).join(',')})` : key;

describe('withdrawalActive', () => {
  it('warns up to and including the last day, not after it', () => {
    const until = new Date(2026, 7, 20, 12).toISOString();
    expect(withdrawalActive(until, '2026-08-19')).toBe(true);
    expect(withdrawalActive(until, '2026-08-20')).toBe(true);
    expect(withdrawalActive(until, '2026-08-21')).toBe(false);
  });

  it('is false without a withdrawal date', () => {
    expect(withdrawalActive('', '2026-08-01')).toBe(false);
    expect(withdrawalActive(null, '2026-08-01')).toBe(false);
  });
});

describe('treatmentRegisterHtml', () => {
  it('lists every register column for each treatment, oldest first', () => {
    const html = treatmentRegisterHtml(
      [
        {
          name: 'Hive 1',
          apiary: 'Orchard',
          treatments: [
            treatment({ id: 'b', date: noon(2026, 8, 1), product: 'Second' }),
            treatment({ id: 'a', date: noon(2026, 7, 1), product: 'First' })
          ]
        }
      ],
      t,
      'en'
    );
    for (const col of [
      'insp.date',
      'treat.product',
      'treat.active',
      'treat.dose',
      'treat.method',
      'treat.batch',
      'treat.withdrawal',
      'treat.amended_col'
    ]) {
      expect(html).toContain(`<th scope="col">${col}</th>`);
    }
    expect(html).toContain('<h2>Hive 1 · Orchard</h2>');
    for (const cell of [
      'oxalic acid',
      '50 ml',
      'trickling',
      'B-17',
      'Aug 1, 2026',
      'Aug 20, 2026'
    ]) {
      expect(html).toContain(`<td>${cell}</td>`);
    }
    expect(html.indexOf('First')).toBeLessThan(html.indexOf('Second'));
  });

  it('marks amended treatments with the date of the amendment', () => {
    const html = treatmentRegisterHtml(
      [{ name: 'Hive 1', treatments: [treatment({ amended_at: noon(2026, 8, 3) })] }],
      t,
      'en'
    );
    expect(html).toContain('<td>treat.amended(Aug 3, 2026)</td>');
  });

  it('escapes everything a user typed', () => {
    const html = treatmentRegisterHtml(
      [
        {
          name: '<img src=x onerror=alert(1)>',
          apiary: '"Orchard" & sons',
          treatments: [treatment({ product: '<script>alert(1)</script>', dose: "5'ml" })]
        }
      ],
      t,
      'en'
    );
    expect(html).not.toContain('<script>');
    expect(html).not.toContain('<img');
    expect(html).toContain('&lt;script&gt;alert(1)&lt;/script&gt;');
    expect(html).toContain('&lt;img src=x onerror=alert(1)&gt; · &quot;Orchard&quot; &amp; sons');
    expect(html).toContain('5&#39;ml');
  });

  it('prints several hives, saying so when one has no treatments', () => {
    const html = treatmentRegisterHtml(
      [
        { name: 'Hive 1', treatments: [treatment()] },
        { name: 'Hive 2', treatments: [] }
      ],
      t,
      'en'
    );
    expect(html.match(/<section>/g)).toHaveLength(2);
    expect(html).toContain('<h2>Hive 2</h2><p class="none">treat.none</p>');
  });
});
