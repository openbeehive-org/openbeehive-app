// Hybrid Logical Clock, same format and rules as the Go server
// (server/internal/sync/hlc.go):
//   "<ms:15>:<counter:5>:<nodeId>"  -> string order == time order.
// nodeId is a stable, per-device randomly generated ID.

const KEY_NODE = 'obh.nodeId';
// Last stamp this device issued; restored on load so a reload (or a device
// clock that was set back) never produces a stamp below an earlier one.
const KEY_LAST = 'obh.hlc';
// What the server's clock was ahead of this device's at the last sync (ms).
const KEY_OFFSET = 'obh.clockOffset';

// How far ahead of the receiver's wall clock a stamp may be; the server
// refuses changes beyond it (code "clock_ahead").
export const MAX_DRIFT_MS = 10 * 60_000;
// Device clocks within this distance of the server's are left alone: a
// network round trip is not a clock error.
const MIN_SKEW_MS = 2_000;
const MAX_COUNTER = 99_999;
const STAMP = /^(\d{15}):(\d{5}):([A-Za-z0-9._-]{1,64})$/;

export type Stamp = { ms: number; counter: number; node: string };

// Strict parser: anything that is not exactly the fixed-width form is not a
// stamp (null), whatever it happens to contain.
export function parseHLC(s: unknown): Stamp | null {
  if (typeof s !== 'string') return null;
  const m = STAMP.exec(s);
  return m ? { ms: Number(m[1]), counter: Number(m[2]), node: m[3]! } : null;
}

export const isHLC = (s: unknown): s is string => typeof s === 'string' && STAMP.test(s);

const pad = (n: number, w: number) => String(n).padStart(w, '0');

export const formatHLC = (ms: number, counter: number, node: string) =>
  `${pad(ms, 15)}:${pad(counter, 5)}:${node}`;

// -1 a<b, 0 equal, 1 a>b. Well-formed stamps compare as strings (fixed
// width). A malformed stamp counts as "no stamp" and sorts below every
// well-formed one, so garbage in a field clock can never win a merge.
export function compare(a: unknown, b: unknown): number {
  const va = isHLC(a);
  const vb = isHLC(b);
  if (va && vb) return a < b ? -1 : a > b ? 1 : 0;
  return va ? 1 : vb ? -1 : 0;
}

function nodeId(): string {
  let id = localStorage.getItem(KEY_NODE);
  if (!id || !/^[A-Za-z0-9._-]{1,64}$/.test(id)) {
    id = crypto.randomUUID().slice(0, 8);
    localStorage.setItem(KEY_NODE, id);
  }
  return id;
}

export class HLC {
  private lastMs = 0;
  private counter = 0;
  private offsetMs = 0;
  readonly node = nodeId();

  constructor() {
    const last = parseHLC(localStorage.getItem(KEY_LAST));
    if (last) {
      this.lastMs = last.ms;
      this.counter = last.counter;
    }
    const off = Number(localStorage.getItem(KEY_OFFSET));
    if (Number.isFinite(off)) this.offsetMs = off;
  }

  // The device's wall clock, corrected by what the server last told us.
  physical(): number {
    return Date.now() + this.offsetMs;
  }

  // Correction currently applied to the device clock (ms; 0 = trusted).
  get skewMs(): number {
    return this.offsetMs;
  }

  now(): string {
    const pt = this.physical();
    if (pt > this.lastMs) {
      this.lastMs = pt;
      this.counter = 0;
    } else {
      this.tick();
    }
    const stamp = formatHLC(this.lastMs, this.counter, this.node);
    localStorage.setItem(KEY_LAST, stamp);
    return stamp;
  }

  // Fold a foreign stamp into the clock so every later now() is greater.
  // Returns false, leaving the clock untouched, for a malformed stamp and for
  // one further ahead than the server itself would accept: adopting it would
  // make every stamp from this device unacceptable too.
  recv(remote: unknown): boolean {
    const r = parseHLC(remote);
    const pt = this.physical();
    if (!r || r.ms > pt + MAX_DRIFT_MS) return false;
    if (pt > this.lastMs && pt > r.ms) {
      this.lastMs = pt;
      this.counter = 0;
    } else if (r.ms > this.lastMs) {
      this.lastMs = r.ms;
      this.counter = r.counter;
      this.tick();
    } else if (this.lastMs > r.ms) {
      this.tick();
    } else {
      this.counter = Math.max(this.counter, r.counter);
      this.tick();
    }
    return true;
  }

  // A fresh stamp that also sorts after the given ones (the stamps already
  // on the fields about to be written), so a local edit is never born stale.
  after(stamps: Iterable<unknown>): string {
    for (const s of stamps) this.recv(s);
    return this.now();
  }

  // Learn the server's wall clock. serverMs is what it reported, localMs the
  // device time the answer belongs to (the middle of the round trip).
  syncTo(serverMs: number, localMs: number) {
    if (!Number.isFinite(serverMs) || serverMs <= 0) return;
    const off = Math.round(serverMs - localMs);
    this.offsetMs = Math.abs(off) < MIN_SKEW_MS ? 0 : off;
    localStorage.setItem(KEY_OFFSET, String(this.offsetMs));
  }

  // Drop back to the corrected wall clock. This deliberately moves the clock
  // backwards and is only for re-stamping changes the server refused as
  // stamped ahead of time; the caller re-stamps everything issued after them.
  rebase() {
    this.lastMs = this.physical();
    this.counter = 0;
    localStorage.setItem(KEY_LAST, formatHLC(this.lastMs, this.counter, this.node));
  }

  // The counter has a fixed width: when it is exhausted the millisecond
  // moves on, a sixth digit would break the string ordering.
  private tick() {
    if (this.counter >= MAX_COUNTER) {
      this.lastMs += 1;
      this.counter = 0;
    } else {
      this.counter++;
    }
  }
}

export const hlc = new HLC();
