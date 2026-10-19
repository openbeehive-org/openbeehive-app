import { describe, it, expect } from 'vitest';
import {
  parseFieldClock,
  accept,
  parseORSet,
  orAdd,
  orRemove,
  orMerge,
  orValues,
  type ORSet
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
