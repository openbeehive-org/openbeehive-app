// Mirror of the server merge logic (server/internal/sync/merge.go) for the
// client. Both sides run the cases in
// server/internal/sync/testdata/merge_vectors.json; a rule changed here must
// change there, with a vector that shows it.
import { compare } from './hlc';

// --- Per-field-LWW ---
export type FieldClock = Record<string, string>;

export function parseFieldClock(s: string | null | undefined): FieldClock {
  if (!s) return {};
  try {
    const fc = JSON.parse(s);
    return fc && typeof fc === 'object' && !Array.isArray(fc) ? fc : {};
  } catch {
    return {};
  }
}

// Applies the field only if the incoming HLC is strictly newer than the
// field's stamp. An equal stamp does not overwrite.
export function accept(fc: FieldClock, field: string, hlc: string): boolean {
  if (compare(hlc, fc[field]) > 0) {
    fc[field] = hlc;
    return true;
  }
  return false;
}

// --- OR-Set (add-wins) ---
type Elem = { a: string[]; r: string[] };
export type ORSet = Record<string, Elem>;

// Element names come from other devices, so a set must not inherit anything:
// in a plain object, set["constructor"] or set["__proto__"] finds (or
// replaces) a built-in property instead of an element, and the merge throws.
// Sets are therefore objects without a prototype, and every lookup goes
// through own().
export const emptySet = (): ORSet => Object.create(null) as ORSet;

const own = <T>(o: Record<string, T>, key: string): T | undefined =>
  Object.hasOwn(o, key) ? o[key] : undefined;

// The entry of elem, created when missing. Entries are written with
// defineProperty, which makes even "__proto__" an ordinary key.
function entry(set: ORSet, elem: string): Elem {
  let e = own(set, elem);
  if (!e) {
    e = { a: [], r: [] };
    Object.defineProperty(set, elem, {
      value: e,
      enumerable: true,
      writable: true,
      configurable: true
    });
  }
  return e;
}

const strings = (v: unknown): string[] =>
  Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];

// Reads a decoded JSON value as an OR-Set; anything else is the empty set.
export function toORSet(raw: unknown): ORSet {
  const set = emptySet();
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return set;
  for (const [elem, e] of Object.entries(raw as Record<string, Partial<Elem> | null>)) {
    const out = entry(set, elem);
    out.a = strings(e?.a);
    out.r = strings(e?.r);
  }
  return set;
}

export function parseORSet(s: string | null | undefined): ORSet {
  if (!s || s === 'null') return emptySet();
  try {
    return toORSet(JSON.parse(s));
  } catch {
    return emptySet();
  }
}

const uniq = (xs: string[], x: string) => (xs.includes(x) ? xs : [...xs, x]);

export function orAdd(set: ORSet, elem: string, tag: string) {
  const e = entry(set, elem);
  e.a = uniq(e.a, tag);
}
export function orRemove(set: ORSet, elem: string) {
  const e = entry(set, elem);
  for (const t of e.a) e.r = uniq(e.r, t); // remove only observed tags
}
// Remove exactly the tags the ORIGIN device had observed (carried in the sync
// delta). Tags added concurrently elsewhere stay visible: true add-wins.
export function orRemoveTags(set: ORSet, elem: string, tags: string[]) {
  const e = entry(set, elem);
  for (const t of tags) e.r = uniq(e.r, t);
}
export function orMerge(into: ORSet, other: ORSet) {
  for (const [elem, oe] of Object.entries(other)) {
    const e = entry(into, elem);
    for (const t of strings(oe?.a)) e.a = uniq(e.a, t);
    for (const t of strings(oe?.r)) e.r = uniq(e.r, t);
  }
}

// The change to one set column carried in a sync payload. removed_tags
// lists, per removed element, the add tags the origin device had observed;
// it is absent in deltas from older apps.
export type SetDelta = {
  add?: string[];
  remove?: string[];
  removed_tags?: Record<string, string[]>;
};

const stringList = (v: unknown): string[] | null =>
  v == null ? [] : Array.isArray(v) && v.every((x) => typeof x === 'string') ? v : null;

// Reads a decoded JSON value as a set delta; null for anything but string
// lists in the three known keys (other keys are ignored).
export function parseSetDelta(v: unknown): SetDelta | null {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return null;
  const o = v as Record<string, unknown>;
  const add = stringList(o.add);
  const remove = stringList(o.remove);
  if (!add || !remove) return null;
  const d: SetDelta = { add, remove };
  if (o.removed_tags != null) {
    const raw = o.removed_tags;
    if (typeof raw !== 'object' || Array.isArray(raw)) return null;
    const removed: Record<string, string[]> = Object.create(null);
    for (const [elem, list] of Object.entries(raw as Record<string, unknown>)) {
      const tags = stringList(list);
      if (!tags) return null;
      Object.defineProperty(removed, elem, {
        value: tags,
        enumerable: true,
        writable: true,
        configurable: true
      });
    }
    d.removed_tags = removed;
  }
  return d;
}

// Merges a delta stamped hlc into the set. Adds are tagged with the stamp. A
// remove tombstones the tags listed for its element in removed_tags: an
// element that is missing there, or has an empty list, loses nothing. Only a
// delta without any removed_tags falls back to tombstoning every tag this
// replica has observed.
export function orApplyDelta(set: ORSet, d: SetDelta, hlc: string) {
  for (const e of d.add ?? []) orAdd(set, e, hlc);
  for (const e of d.remove ?? []) {
    if (!d.removed_tags) orRemove(set, e);
    else orRemoveTags(set, e, own(d.removed_tags, e) ?? []);
  }
}

// The fields a delete change carries: deleting a row is a write to its
// "deleted" column, merged like any other scalar.
export const tombstone = (): Record<string, unknown> => ({ deleted: 1 });

// The merge state of one row: the clock of its scalar columns and the
// content of its set columns.
export type RowState = { clock: FieldClock; sets: Record<string, ORSet> };

// Merges the fields of one change stamped hlc into the row and returns the
// columns that have to be written back, sorted: scalars whose stamp won
// (their new value is fields[column]) and every set column the change
// carries (its new value is row.sets[column]). Values of setCols must be
// SetDelta objects. "id" and "field_hlc" are never merged.
export function mergeFields(
  row: RowState,
  fields: Record<string, unknown>,
  setCols: readonly string[],
  hlc: string
): string[] {
  const changed: string[] = [];
  for (const [col, v] of Object.entries(fields)) {
    if (col === 'id' || col === 'field_hlc') continue;
    if (setCols.includes(col)) {
      const set = (row.sets[col] ??= emptySet());
      orApplyDelta(set, v as SetDelta, hlc);
      changed.push(col);
    } else if (accept(row.clock, col, hlc)) {
      changed.push(col);
    }
  }
  return changed.sort();
}

// Merges one row of a snapshot into the local row and returns the columns
// that have to be written back, sorted. Unlike a change, a snapshot row
// carries a stamp per column (stamps) and whole OR-Sets (values of setCols
// must be ORSet objects):
//  - a column is taken when its stamp is newer than the local one, so a
//    newer local edit is never overwritten;
//  - a column without a stamp was never written through sync. It is taken
//    only where this device has no stamp for it either;
//  - set columns are merged by union of their tags.
// The server has no counterpart: it never receives a snapshot.
export function mergeSnapshot(
  row: RowState,
  fields: Record<string, unknown>,
  stamps: FieldClock,
  setCols: readonly string[]
): string[] {
  const changed: string[] = [];
  for (const [col, v] of Object.entries(fields)) {
    if (col === 'id' || col === 'field_hlc') continue;
    if (setCols.includes(col)) {
      orMerge((row.sets[col] ??= emptySet()), v as ORSet);
      changed.push(col);
    } else if (compare(stamps[col], undefined) > 0) {
      if (accept(row.clock, col, stamps[col]!)) changed.push(col);
    } else if (compare(row.clock[col], undefined) <= 0) {
      changed.push(col);
    }
  }
  return changed.sort();
}
