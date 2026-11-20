import { describe, expect, it } from 'vitest';
import { reportHtml } from './report';
import { fromDateInput } from '../dates';

const t = (key: string, o?: { values?: Record<string, string | number> }) =>
  o?.values ? `${key}(${Object.values(o.values).join(',')})` : key;

const data = {
  apiary: [
    { id: 'a1', name: 'Orchard <b>', address: 'Lane 1' },
    { id: 'a2', name: 'Empty', address: '' }
  ],
  hive: [{ id: 'h1', apiary_id: 'a1', name: 'Hive 7' }],
  queen: [{ id: 'q1', hive_id: 'h1', year: 2026, marking: 2, active: 1 }],
  inspection: [
    { id: 'v1', hive_id: 'h1', date: fromDateInput('2026-05-01'), weight_kg: 40, note: 'older' },
    {
      id: 'v2',
      hive_id: 'h1',
      date: fromDateInput('2026-05-03'),
      created_at: '2026-05-03T08:00:00.000Z',
      weight_kg: 40,
      temp_hive: 35,
      note: 'newest visit'
    },
    // A sensor reading is not a visit.
    { id: 's1', hive_id: 'h1', date: fromDateInput('2026-05-09'), source: 2, note: 'sensor' }
  ]
};

describe('reportHtml', () => {
  it('is translated, escapes stored values and shows the newest visit of a person', () => {
    const html = reportHtml(data, t, 'en', { weight: 'kg', temp: 'c' });
    expect(html).toContain('<h1>data.report_title</h1>');
    expect(html).toContain('<th scope="col">hives.last_inspection</th>');
    expect(html).toContain('Orchard &lt;b&gt;');
    expect(html).not.toContain('<b>');
    expect(html).toContain('newest visit');
    expect(html).not.toContain('sensor');
    expect(html).toContain('May 3, 2026');
    expect(html).toContain('2026 queens.color.2');
    expect(html).toContain('35 °C');
    expect(html).toContain('<p class="none">apiaries.no_hives</p>');
    expect(html).not.toContain('<script');
  });

  it('shows weights and temperatures in the chosen units and the UI language', () => {
    const html = reportHtml(data, t, 'de', { weight: 'lb', temp: 'f' });
    expect(html).toContain('88,18 lb');
    expect(html).toContain('95 °F');
    expect(html).toContain('03.05.2026');
  });

  it('says so when there is nothing to report', () => {
    expect(reportHtml({}, t, 'en', { weight: 'kg', temp: 'c' })).toContain('data.report_empty');
  });
});
