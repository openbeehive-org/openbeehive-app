// Mirror of the server merge logic (internal/sync/merge.go) for the client.
import { compare } from './hlc';

// --- Per-field-LWW ---
export type FieldClock = Record<string, string>;

export function parseFieldClock(s: string | null | undefined): FieldClock {
  if (!s) return {};
  try {
    return JSON.parse(s);
  } catch {
    return {};
  }
}

// Applies the field only if the incoming HLC is newer.
export function accept(fc: FieldClock, field: string, hlc: string): boolean {
  if (compare(hlc, fc[field] ?? '') > 0) {
    fc[field] = hlc;
    return true;
  }
  return false;
}

// --- OR-Set (add-wins) ---
type Elem = { a: string[]; r: string[] };
export type ORSet = Record<string, Elem>;

export function parseORSet(s: string | null | undefined): ORSet {
  if (!s || s === 'null') return {};
  try {
    return JSON.parse(s);
  } catch {
    return {};
  }
}

const uniq = (xs: string[], x: string) => (xs.includes(x) ? xs : [...xs, x]);

export function orAdd(set: ORSet, elem: string, tag: string) {
  const e = set[elem] ?? { a: [], r: [] };
  e.a = uniq(e.a, tag);
  set[elem] = e;
}
export function orRemove(set: ORSet, elem: string) {
  const e = set[elem] ?? { a: [], r: [] };
  for (const t of e.a) e.r = uniq(e.r, t); // remove only observed tags
  set[elem] = e;
}
// Remove exactly the tags the ORIGIN device had observed (carried in the sync
// delta). Tags added concurrently elsewhere stay visible: true add-wins.
export function orRemoveTags(set: ORSet, elem: string, tags: string[]) {
  const e = set[elem] ?? { a: [], r: [] };
  for (const t of tags) e.r = uniq(e.r, t);
  set[elem] = e;
}
export function orMerge(into: ORSet, other: ORSet) {
  for (const [elem, oe] of Object.entries(other)) {
    const e = into[elem] ?? { a: [], r: [] };
    for (const t of oe.a) e.a = uniq(e.a, t);
    for (const t of oe.r) e.r = uniq(e.r, t);
    into[elem] = e;
  }
}
export function orValues(set: ORSet): string[] {
  const out: string[] = [];
  for (const [elem, e] of Object.entries(set)) {
    const rm = new Set(e.r);
    if (e.a.some((t) => !rm.has(t))) out.push(elem);
  }
  return out;
}
