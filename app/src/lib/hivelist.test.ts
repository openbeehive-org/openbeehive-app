import { describe, expect, it } from 'vitest';
import {
  byName,
  daysSince,
  dueHives,
  filterHives,
  firstRunSteps,
  naturalCompare,
  NO_APIARY,
  sortHives,
  type HiveOverview
} from './hivelist';

const hive = (over: Partial<HiveOverview> & { id: string; name: string }): HiveOverview => ({
  apiary_id: 'a1',
  status: 1,
  photo: null,
  apiary_name: 'Orchard',
  last_inspection: null,
  queen_year: null,
  queen_marking: null,
  ...over
});

describe('natural name order', () => {
  it('orders numbers by value and ignores case', () => {
    const names = ['Hive 10', 'hive 2', 'Hive 1', 'HIVE 3', 'Hive 02b', 'hive 2a'];
    expect([...names].sort(naturalCompare)).toEqual([
      'Hive 1',
      'hive 2',
      'hive 2a',
      'Hive 02b',
      'HIVE 3',
      'Hive 10'
    ]);
  });

  it('keeps accented letters next to their base letter and tolerates missing names', () => {
    expect(['Zeta', 'Äpfel 2', 'apfel 1', 'Birke'].sort(naturalCompare)).toEqual([
      'apfel 1',
      'Äpfel 2',
      'Birke',
      'Zeta'
    ]);
    expect(byName([{ name: 'b' }, { name: null }, { name: 'A' }]).map((r) => r.name)).toEqual([
      null,
      'A',
      'b'
    ]);
  });
});

describe('daysSince', () => {
  const now = new Date(2026, 5, 15, 9, 0); // 15 June, 09:00 local

  it('counts calendar days, not 24-hour blocks', () => {
    expect(daysSince(new Date(2026, 5, 14, 23, 50).toISOString(), now)).toBe(1);
    expect(daysSince(new Date(2026, 5, 1, 12, 0).toISOString(), now)).toBe(14);
    expect(daysSince('2026-06-12', now)).toBe(3);
  });

  it('is 0 for a visit dated today, even when stamped later than now', () => {
    // Date-only entries are stored as local noon; at 09:00 that lies ahead.
    expect(daysSince(new Date(2026, 5, 15, 12, 0).toISOString(), now)).toBe(0);
    expect(daysSince(new Date(2026, 5, 16, 12, 0).toISOString(), now)).toBe(0);
  });

  it('is null without a usable date', () => {
    expect(daysSince(null, now)).toBeNull();
    expect(daysSince('', now)).toBeNull();
    expect(daysSince('not a date', now)).toBeNull();
  });
});

describe('filterHives', () => {
  const rows = [
    hive({ id: '3f9a1c22-0000-4000-8000-000000000001', name: 'Völker Nord', status: 1 }),
    hive({ id: 'bbbbbbbb-0000-4000-8000-000000000002', name: 'Hive 2', status: 3 }),
    hive({
      id: 'cccccccc-0000-4000-8000-000000000003',
      name: 'Hive 3',
      apiary_id: 'a2',
      apiary_name: 'Meadow'
    }),
    hive({
      id: 'dddddddd-0000-4000-8000-000000000004',
      name: 'Orphan',
      apiary_id: 'gone',
      apiary_name: null
    })
  ];
  const names = (f: Parameters<typeof filterHives>[1]) => filterHives(rows, f).map((h) => h.name);

  it('returns everything without filters', () => {
    expect(names({})).toHaveLength(4);
    expect(names({ query: '   ', status: '', apiary: '' })).toHaveLength(4);
  });

  it('searches name, apiary and label code, ignoring case and accents', () => {
    expect(names({ query: 'volker' })).toEqual(['Völker Nord']);
    expect(names({ query: 'MEADOW' })).toEqual(['Hive 3']);
    expect(names({ query: '3f9a1c' })).toEqual(['Völker Nord']);
    expect(names({ query: '3F9' })).toEqual(['Völker Nord']);
    expect(names({ query: 'hive orchard' })).toEqual(['Hive 2']);
    expect(names({ query: 'nothing like this' })).toEqual([]);
  });

  it('does not match digits or letters that merely occur in a label code', () => {
    // "2" is in no name but in several ids; "9a1" is inside a code, not at its start.
    expect(names({ query: '2' })).toEqual(['Hive 2']);
    expect(names({ query: 'bb' })).toEqual([]);
    expect(names({ query: '9a1' })).toEqual([]);
    expect(names({ query: 'hive 3f9' })).toEqual([]);
  });

  it('filters by status and apiary, including hives without an apiary', () => {
    expect(names({ status: '3' })).toEqual(['Hive 2']);
    expect(names({ apiary: 'a2' })).toEqual(['Hive 3']);
    expect(names({ apiary: NO_APIARY })).toEqual(['Orphan']);
    expect(names({ apiary: 'a1', status: '1', query: 'nord' })).toEqual(['Völker Nord']);
  });
});

describe('sortHives', () => {
  const now = new Date(2026, 5, 15, 9, 0);
  const rows = [
    hive({ id: '1', name: 'Hive 10', apiary_name: 'Meadow', last_inspection: '2026-06-10' }),
    hive({ id: '2', name: 'hive 2', apiary_name: 'meadow', last_inspection: null }),
    hive({ id: '3', name: 'Hive 1', apiary_name: null, last_inspection: '2026-05-01' }),
    hive({ id: '4', name: 'Hive 3', apiary_name: 'Apple yard', last_inspection: '2026-06-10' })
  ];
  const order = (s: Parameters<typeof sortHives>[1]) => sortHives(rows, s, now).map((h) => h.name);

  it('sorts by name naturally and leaves the input untouched', () => {
    expect(order('name')).toEqual(['Hive 1', 'hive 2', 'Hive 3', 'Hive 10']);
    expect(rows[0]!.name).toBe('Hive 10');
  });

  it('sorts by apiary, then name, with hives without an apiary last', () => {
    expect(order('apiary')).toEqual(['Hive 3', 'hive 2', 'Hive 10', 'Hive 1']);
  });

  it('puts the longest-unvisited hives first, never visited before all', () => {
    expect(order('inspected')).toEqual(['hive 2', 'Hive 1', 'Hive 3', 'Hive 10']);
  });
});

describe('dueHives', () => {
  const now = new Date(2026, 5, 15, 9, 0);

  it('lists the most neglected hives and skips lost and dissolved colonies', () => {
    const rows = [
      hive({ id: '1', name: 'Recent', last_inspection: '2026-06-14' }),
      hive({ id: '2', name: 'Never' }),
      hive({ id: '3', name: 'Old', last_inspection: '2026-04-01', apiary_name: null }),
      hive({ id: '4', name: 'Lost', status: 4 }),
      hive({ id: '5', name: 'Dissolved', status: 5 })
    ];
    expect(dueHives(rows, now)).toEqual([
      { id: '2', name: 'Never', apiary: 'Orchard', days: null },
      { id: '3', name: 'Old', apiary: null, days: 75 },
      { id: '1', name: 'Recent', apiary: 'Orchard', days: 1 }
    ]);
    expect(dueHives(rows, now, 1).map((d) => d.name)).toEqual(['Never']);
  });

  it('is empty without hives', () => {
    expect(dueHives([], now)).toEqual([]);
  });
});

describe('firstRunSteps', () => {
  const state = (s: Parameters<typeof firstRunSteps>[0]) => {
    const r = firstRunSteps(s);
    return {
      complete: r.complete,
      done: r.steps.filter((x) => x.done).map((x) => x.key),
      current: r.steps.filter((x) => x.current).map((x) => x.key)
    };
  };

  it('starts a brand-new user at "create an apiary"', () => {
    const r = firstRunSteps({ apiaries: 0, hives: 0, visits: 0 });
    expect(r.complete).toBe(false);
    expect(r.steps.map((s) => [s.key, s.done, s.current, s.href])).toEqual([
      ['apiary', false, true, '/apiaries?new=1'],
      ['hive', false, false, '/hives?new=1'],
      ['visit', false, false, '/hives']
    ]);
  });

  it('ticks steps off one by one', () => {
    expect(state({ apiaries: 1, hives: 0, visits: 0 })).toEqual({
      complete: false,
      done: ['apiary'],
      current: ['hive']
    });
    expect(state({ apiaries: 1, hives: 2, visits: 0 })).toEqual({
      complete: false,
      done: ['apiary', 'hive'],
      current: ['visit']
    });
    expect(state({ apiaries: 2, hives: 5, visits: 9 })).toEqual({
      complete: true,
      done: ['apiary', 'hive', 'visit'],
      current: []
    });
  });

  it('links the visit step to the first hive when there is one', () => {
    const r = firstRunSteps({ apiaries: 1, hives: 1, visits: 0, firstHiveId: 'h1' });
    expect(r.steps[2]).toMatchObject({ key: 'visit', current: true, href: '/hives/h1' });
  });

  it('offers only the first open step when later ones are already done', () => {
    // Hives synced from another device whose apiary was deleted there.
    expect(state({ apiaries: 0, hives: 3, visits: 4 })).toEqual({
      complete: false,
      done: ['hive', 'visit'],
      current: ['apiary']
    });
  });
});
