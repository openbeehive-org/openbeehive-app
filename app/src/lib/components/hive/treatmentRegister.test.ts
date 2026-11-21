import { describe, it, expect } from 'vitest';
import type { TreatmentRow } from '../../../routes/(app)/rows.js';
import {
  treatmentRegisterHtml,
  treatmentYears,
  withdrawalActive,
  workspaceRegister
} from './treatmentRegister';

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
      'treat.reason',
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
      'varroa',
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

describe('workspaceRegister', () => {
  const apiaries = [
    { id: 'a2', name: 'Wood' },
    { id: 'a1', name: 'Orchard' }
  ];
  const hives = [
    { id: 'h1', name: 'Hive 10', apiary_id: 'a1' },
    { id: 'h2', name: 'Hive 2', apiary_id: 'a1' },
    { id: 'h3', name: 'Hive 1', apiary_id: 'a2' },
    { id: 'h4', name: 'Untreated', apiary_id: 'a1' }
  ];
  const rows = [
    treatment({ id: 't1', hive_id: 'h1', date: noon(2026, 8, 1) }),
    treatment({ id: 't2', hive_id: 'h2', date: noon(2025, 8, 1) }),
    treatment({ id: 't3', hive_id: 'h3', date: noon(2026, 1, 1) })
  ];
  const names = (r: ReturnType<typeof workspaceRegister>) => r.map((h) => `${h.apiary}/${h.name}`);

  it('covers every treated hive, ordered by apiary and hive', () => {
    expect(names(workspaceRegister(apiaries, hives, rows, { apiary: '', year: '' }))).toEqual([
      'Orchard/Hive 2',
      'Orchard/Hive 10',
      'Wood/Hive 1'
    ]);
  });

  it('narrows to one apiary and one local calendar year', () => {
    const one = workspaceRegister(apiaries, hives, rows, { apiary: 'a1', year: '2026' });
    expect(names(one)).toEqual(['Orchard/Hive 10']);
    expect(one[0]?.treatments.map((r) => r.id)).toEqual(['t1']);
    expect(treatmentYears(rows)).toEqual(['2026', '2025']);
  });

  it('says so when the selection has no treatments', () => {
    expect(treatmentRegisterHtml([], t, 'en', 'Wood · 2024')).toContain(
      '<p class="meta">Wood · 2024</p><p class="meta">treat.register_printed('
    );
    expect(treatmentRegisterHtml([], t, 'en')).toContain('treat.register_empty');
  });
});
