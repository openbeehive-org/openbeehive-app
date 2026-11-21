// The elements of an OR-Set that are visible: added under a tag that was
// not removed. The app itself never needs this list (it shows photo
// records, not the legacy set); tests use it to state what a set holds.
import type { ORSet } from '#lib/local/merge.js';

export function orValues(set: ORSet): string[] {
  const out: string[] = [];
  for (const [elem, e] of Object.entries(set)) {
    const rm = new Set(e.r);
    if (e.a.some((t) => !rm.has(t))) out.push(elem);
  }
  return out;
}
