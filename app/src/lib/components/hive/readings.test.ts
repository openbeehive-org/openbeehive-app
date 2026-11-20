import { describe, it, expect } from 'vitest';
import type { InspectionRow } from '../../../routes/(app)/rows.js';
import {
  carryOver,
  isSensor,
  level,
  levelSeries,
  numericSeries,
  parseVarroa,
  reading,
  varroaSeries,
  visitChips,
  ORDINALS
} from './readings';

const row = (over: Partial<InspectionRow> = {}): InspectionRow => ({
  id: 'i1',
  hive_id: 'h1',
  date: '2026-05-14T10:00:00.000Z',
  weather: null,
  queen_seen: 0,
  eggs_seen: 0,
  temperament: 0,
  frames: null,
  stores: 0,
  queen_cells: null,
  varroa: null,
  honey_kg: null,
  note: null,
  brood_frames: null,
  calmness: 0,
  fed_kg: null,
  frames_added: null,
  frames_removed: null,
  drone_frame_cut: 0,
  super_added: 0,
  weight_kg: null,
  youngest_larva: null,
  covered_larva: 0,
  temp_hive: null,
  temp_outside: null,
  humidity_hive: null,
  humidity_outside: null,
  source: 1,
  created_at: '2026-05-14T10:00:00.000Z',
  photos: [],
  ...over
});

// Translations are replaced by their keys, so the tests read the structure.
const t = (key: string) => key;

describe('reading: NULL is "not recorded", 0 is "observed none"', () => {
  it('returns null for an empty count', () => {
    expect(reading(row({ queen_cells: null }), 'queen_cells')).toBeNull();
  });

  it('keeps an observed zero on a visit recorded with the current form', () => {
    expect(reading(row({ queen_cells: 0, source: 1 }), 'queen_cells')).toBe(0);
  });

  it('reads the 0 that older app versions stored for empty fields as not recorded', () => {
    expect(reading(row({ frames: 0, source: 0 }), 'frames')).toBeNull();
    expect(reading(row({ frames: 0, source: null }), 'frames')).toBeNull();
    expect(reading(row({ frames: 7, source: 0 }), 'frames')).toBe(7);
  });

  it('does not take a sensor row without a count as a count of zero', () => {
    expect(reading(row({ frames: 0, source: 2 }), 'frames')).toBeNull();
    expect(reading(row({ weight_kg: 41.5, source: 2 }), 'weight_kg')).toBe(41.5);
  });

  it('keeps zero and sub-zero climate values whatever the source', () => {
    expect(reading(row({ temp_outside: 0, source: 0 }), 'temp_outside')).toBe(0);
    expect(reading(row({ temp_outside: -4.5, source: 2 }), 'temp_outside')).toBe(-4.5);
    expect(reading(row({ temp_outside: null }), 'temp_outside')).toBeNull();
  });
});

describe('visitChips', () => {
  it('shows an observed zero and leaves out what was not recorded', () => {
    const chips = visitChips(row({ queen_cells: 0, frames: null, brood_frames: 5 }), t);
    expect(chips).toContain('0 insp.queen_cells');
    expect(chips).toContain('5 insp.brood_frames');
    expect(chips.some((c) => c.includes('insp.frames'))).toBe(false);
  });

  it('shows no zero chips for a visit saved by an older app version', () => {
    const legacy = row({
      source: 0,
      frames: 0,
      brood_frames: 0,
      queen_cells: 0,
      youngest_larva: 0,
      fed_kg: 0,
      frames_added: 0,
      frames_removed: 0,
      weight_kg: 0,
      honey_kg: 0
    });
    expect(visitChips(legacy, t)).toEqual([]);
  });

  it('summarises everything recorded on a visit, in a fixed order', () => {
    const full = row({
      queen_seen: 1,
      eggs_seen: 1,
      youngest_larva: 3,
      covered_larva: 1,
      frames: 9,
      brood_frames: 6,
      queen_cells: 2,
      fed_kg: 1.5,
      frames_added: 2,
      frames_removed: 1,
      drone_frame_cut: 1,
      super_added: 1,
      weight_kg: 38,
      temp_hive: 34,
      humidity_hive: 62,
      varroa: '3 mites/day'
    });
    expect(visitChips(full, t)).toEqual([
      'insp.queen_seen',
      'insp.eggs_seen',
      'insp.youngest_larva: 3 insp.unit_d',
      'insp.covered_larva',
      '9 insp.frames',
      '6 insp.brood_frames',
      '2 insp.queen_cells',
      'insp.fed_kg 1.5 kg',
      '+2 insp.frames',
      '−1 insp.frames',
      'insp.drone_frame_cut',
      'insp.super_added',
      '38 kg',
      'insp.temp_hive 34 °C',
      'insp.humidity_hive 62 %',
      'insp.varroa: 3 mites/day'
    ]);
  });

  it('names rating levels instead of printing their codes', () => {
    const chips = visitChips(row({ stores: 3, temperament: 1, calmness: 4 }), t);
    expect(chips).toEqual([
      'insp.stores: storeslvl.3',
      'insp.temperament: temperament.1',
      'insp.calmness: calmlvl.4'
    ]);
  });

  it('still shows honey recorded on an old visit', () => {
    expect(visitChips(row({ source: 0, honey_kg: 12 }), t)).toEqual(['insp.honey_kg 12 kg']);
  });

  it('shows weights and temperatures in the chosen units and language', () => {
    const imperial = { weight: 'lb', temp: 'f' } as const;
    const visit = row({ source: 1, weight_kg: 38, fed_kg: 0.5, temp_hive: 34.5 });
    expect(visitChips(visit, t, imperial, 'de')).toEqual([
      'insp.fed_kg 1,1 lb',
      '83,78 lb',
      'insp.temp_hive 94,1 °F'
    ]);
    expect(visitChips(visit, t, { weight: 'kg', temp: 'c' }, 'de')).toContain(
      'insp.temp_hive 34,5 °C'
    );
  });

  it('shows frost readings', () => {
    expect(visitChips(row({ temp_hive: -2 }), t)).toEqual(['insp.temp_hive -2 °C']);
  });
});

describe('ratings', () => {
  it('treats 0 and unknown codes as not rated', () => {
    expect(level(row({ stores: 0 }), 'stores')).toBeNull();
    expect(level(row({ stores: 9 }), 'stores')).toBeNull();
    expect(level(row({ stores: 2 }), 'stores')).toBe(2);
  });

  it('orders every scale from best to worst', () => {
    expect(ORDINALS.stores.bestFirst[0]).toBe(1); // good
    expect(ORDINALS.temperament.bestFirst[0]).toBe(1); // very gentle
    expect(ORDINALS.calmness.bestFirst[0]).toBe(4); // very calm
    expect(ORDINALS.calmness.bestFirst.at(-1)).toBe(1); // runs off comb
  });

  it('levelSeries skips visits without a rating', () => {
    const rows = [row({ stores: 1 }), row({ stores: 0 }), row({ stores: 3 })];
    expect(levelSeries(rows, 'stores').map((p) => p.value)).toEqual([1, 3]);
  });
});

describe('chart series', () => {
  it('plots negative and zero temperatures and skips missing ones', () => {
    const rows = [
      row({ temp_outside: -6 }),
      row({ temp_outside: null }),
      row({ temp_outside: 0 }),
      row({ temp_outside: 3.5 })
    ];
    expect(numericSeries(rows, 'temp_outside').map((p) => p.value)).toEqual([-6, 0, 3.5]);
  });

  it('includes sensor readings', () => {
    const rows = [row({ weight_kg: 30 }), row({ source: 2, weight_kg: 31.2 })];
    expect(numericSeries(rows, 'weight_kg').map((p) => p.value)).toEqual([30, 31.2]);
  });

  it('keeps an observed zero as a point but not an older empty field', () => {
    const rows = [
      row({ queen_cells: 0 }),
      row({ source: 0, queen_cells: 0 }),
      row({ queen_cells: 4 })
    ];
    expect(numericSeries(rows, 'queen_cells').map((p) => p.value)).toEqual([0, 4]);
  });
});

describe('varroa', () => {
  it('parses only plain numbers', () => {
    expect(parseVarroa('3')).toBe(3);
    expect(parseVarroa(' 2,5 ')).toBe(2.5);
    expect(parseVarroa('0')).toBe(0);
    expect(parseVarroa('3 mites/day')).toBeNull();
    expect(parseVarroa('12e3')).toBeNull();
    expect(parseVarroa('ca. 5')).toBeNull();
    expect(parseVarroa('')).toBeNull();
    expect(parseVarroa(null)).toBeNull();
  });

  it('charts plain numbers with what was entered and counts the rest', () => {
    const rows = [
      row({ varroa: '3' }),
      row({ varroa: '3 mites/day' }),
      row({ varroa: '' }),
      row({ varroa: '1,5' })
    ];
    const { points, skipped } = varroaSeries(rows);
    expect(points.map((p) => [p.value, p.entered])).toEqual([
      [3, '3'],
      [1.5, '1,5']
    ]);
    expect(skipped).toBe(1);
  });
});

describe('sensor readings', () => {
  it('are recognised by source = 2 only', () => {
    expect(isSensor(row({ source: 2 }))).toBe(true);
    expect(isSensor(row({ source: 1 }))).toBe(false);
    expect(isSensor(row({ source: 0 }))).toBe(false);
    expect(isSensor(row({ source: null }))).toBe(false);
  });
});

describe('carryOver ("Start from last visit")', () => {
  it('carries colony size and ratings, nothing else', () => {
    const last = row({
      frames: 9,
      brood_frames: 6,
      stores: 2,
      temperament: 1,
      calmness: 3,
      queen_cells: 4,
      queen_seen: 1,
      weight_kg: 40,
      note: 'x'
    });
    expect(carryOver(last)).toEqual({
      frames: 9,
      brood_frames: 6,
      stores: 2,
      temperament: 1,
      calmness: 3
    });
  });

  it('leaves counts empty when the last visit did not record them', () => {
    expect(carryOver(row({ source: 0, frames: 0, brood_frames: 0 }))).toMatchObject({
      frames: null,
      brood_frames: null,
      stores: 0
    });
  });
});
