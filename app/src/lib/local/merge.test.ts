import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  parseFieldClock,
  accept,
  parseORSet,
  parseSetDelta,
  mergeFields,
  tombstone,
  orAdd,
  orRemove,
  orMerge,
  orValues,
  type ORSet,
  type RowState
} from './merge';

const t1 = '000000000000001:00000:devA';
const t2 = '000000000000002:00000:devB';
const t3 = '000000000000003:00000:devA';

describe('per-field LWW (FieldClock)', () => {
  it('accepts a newer stamp and records it', () => {
    const fc = parseFieldClock('{}');
    expect(accept(fc, 'name', t2)).toBe(true);
    expect(fc.name).toBe(t2);
  });

  it('rejects an older or equal stamp', () => {
    const fc = { name: t2 };
    expect(accept(fc, 'name', t1)).toBe(false);
    expect(accept(fc, 'name', t2)).toBe(false);
    expect(fc.name).toBe(t2); // unchanged
  });

  it('fields are independent', () => {
    const fc = { name: t3 };
    expect(accept(fc, 'note', t1)).toBe(true); // other field, older stamp: fine
  });

  it('tolerates malformed clock JSON', () => {
    expect(parseFieldClock('not json')).toEqual({});
    expect(parseFieldClock(null)).toEqual({});
    expect(parseFieldClock(undefined)).toEqual({});
  });

  it('same millisecond: counter then node id break the tie deterministically', () => {
    const a = '000000000000005:00001:devA';
    const b = '000000000000005:00002:devA';
    const fc = { f: a };
    expect(accept(fc, 'f', b)).toBe(true);
  });
});

describe('OR-Set (add-wins)', () => {
  it('add makes the element visible', () => {
    const s: ORSet = {};
    orAdd(s, 'p1', t1);
    expect(orValues(s)).toEqual(['p1']);
  });

  it('remove hides only observed adds', () => {
    const s: ORSet = {};
    orAdd(s, 'p1', t1);
    orRemove(s, 'p1');
    expect(orValues(s)).toEqual([]);
  });

  it('concurrent add wins over remove (add-wins)', () => {
    // Device A removes based on what it saw; device B adds concurrently with
    // a tag A has not observed. After merge, the element stays.
    const a: ORSet = {};
    orAdd(a, 'p1', t1);
    orRemove(a, 'p1');

    const b: ORSet = {};
    orAdd(b, 'p1', t2); // unobserved by A's remove

    orMerge(a, b);
    expect(orValues(a)).toEqual(['p1']);
  });

  it('merge is commutative: both orders converge', () => {
    const mk = () => {
      const s: ORSet = {};
      orAdd(s, 'x', t1);
      orRemove(s, 'x');
      return s;
    };
    const other: ORSet = {};
    orAdd(other, 'x', t2);
    orAdd(other, 'y', t3);

    const ab = mk();
    orMerge(ab, other);
    const ba: ORSet = JSON.parse(JSON.stringify(other));
    orMerge(ba, mk());
    expect(orValues(ab).sort()).toEqual(orValues(ba).sort());
  });

  it('re-add after remove is visible again', () => {
    const s: ORSet = {};
    orAdd(s, 'p1', t1);
    orRemove(s, 'p1');
    orAdd(s, 'p1', t3);
    expect(orValues(s)).toEqual(['p1']);
  });

  it('duplicate tags are not accumulated', () => {
    const s: ORSet = {};
    orAdd(s, 'p1', t1);
    orAdd(s, 'p1', t1);
    expect(s['p1']?.a).toEqual([t1]);
  });

  it('tolerates malformed set JSON', () => {
    expect(parseORSet('nope')).toEqual({});
    expect(parseORSet(null)).toEqual({});
    expect(parseORSet('null')).toEqual({});
  });
});

// The shared merge cases, also executed by the server
// (server/internal/sync/vectors_test.go). A rule that differs between the
// two sides makes devices and server disagree for good.
type Vector = {
  name: string;
  set_cols?: string[];
  row: {
    fields: Record<string, unknown>;
    field_hlc: Record<string, string>;
    sets: Record<string, ORSet>;
  } | null;
  changes: { op: 'upsert' | 'delete'; hlc: string; payload: Record<string, unknown> }[];
  want: {
    fields: Record<string, unknown>;
    field_hlc: Record<string, string>;
    sets: Record<string, string[]>;
    tags?: Record<string, ORSet>;
  };
};

const vectors: { cases: Vector[] } = JSON.parse(
  readFileSync(
    new URL('../../../../server/internal/sync/testdata/merge_vectors.json', import.meta.url),
    'utf8'
  )
);

describe('shared merge vectors', () => {
  it('has cases', () => expect(vectors.cases.length).toBeGreaterThan(10));

  for (const c of vectors.cases) {
    it(c.name, () => {
      const setCols = c.set_cols ?? [];
      const values: Record<string, unknown> = { ...(c.row?.fields ?? {}) };
      const row: RowState = {
        clock: { ...(c.row?.field_hlc ?? {}) },
        // Through the same parser the stored column goes through.
        sets: Object.fromEntries(
          Object.entries(c.row?.sets ?? {}).map(([k, v]) => [k, parseORSet(JSON.stringify(v))])
        )
      };

      for (const ch of c.changes) {
        const fields: Record<string, unknown> =
          ch.op === 'delete' ? tombstone() : { ...ch.payload };
        for (const sc of setCols) {
          if (!(sc in fields)) continue;
          const d = parseSetDelta(fields[sc]);
          expect(d, `set delta ${sc}`).not.toBeNull();
          fields[sc] = d;
        }
        for (const col of mergeFields(row, fields, setCols, ch.hlc)) {
          if (!setCols.includes(col)) values[col] = fields[col];
        }
      }

      expect(values).toEqual(c.want.fields);
      expect(row.clock).toEqual(c.want.field_hlc);
      for (const [col, want] of Object.entries(c.want.sets)) {
        expect(orValues(row.sets[col] ?? {}).sort(), `set ${col}`).toEqual(want);
      }
      for (const [col, want] of Object.entries(c.want.tags ?? {})) {
        expect(row.sets[col], `tags of ${col}`).toEqual(want);
      }
    });
  }
});

describe('set deltas', () => {
  it('reads add, remove and removed_tags; ignores other keys', () => {
    expect(
      parseSetDelta({ add: ['a'], remove: ['b'], removed_tags: { b: ['t'] }, junk: 1 })
    ).toEqual({ add: ['a'], remove: ['b'], removed_tags: { b: ['t'] } });
    expect(parseSetDelta({})).toEqual({ add: [], remove: [] });
    expect(parseSetDelta({ remove: ['b'], removed_tags: null })).toEqual({
      add: [],
      remove: ['b']
    });
  });

  it('refuses anything that is not a delta', () => {
    for (const bad of [
      null,
      '{}',
      ['a'],
      { add: 'a' },
      { add: [1] },
      { remove: {} },
      { removed_tags: [] },
      { removed_tags: { a: 't' } },
      { removed_tags: { a: [1] } }
    ]) {
      expect(parseSetDelta(bad), JSON.stringify(bad)).toBeNull();
    }
  });

  it('a stored set with missing or broken tag lists still parses', () => {
    expect(parseORSet('{"p1":{"a":["t"],"r":null},"p2":null,"p3":{"a":"x"}}')).toEqual({
      p1: { a: ['t'], r: [] },
      p2: { a: [], r: [] },
      p3: { a: [], r: [] }
    });
    expect(parseORSet('[1,2]')).toEqual({});
    expect(parseFieldClock('[1]')).toEqual({});
  });
});
