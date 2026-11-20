import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { HLC, compare, parseHLC, isHLC, formatHLC, MAX_DRIFT_MS } from './hlc';

beforeEach(() => localStorage.clear());
afterEach(() => vi.useRealTimers());

describe('stamp format', () => {
  it('parses only the fixed-width form', () => {
    expect(parseHLC('000001700000000:00042:dev-A.1_x')).toEqual({
      ms: 1700000000,
      counter: 42,
      node: 'dev-A.1_x'
    });
    for (const bad of [
      '',
      'zzzz',
      '1700000000:00042:dev', // ms not padded
      '000001700000000:42:dev', // counter not padded
      '000001700000000:000420:dev', // counter too wide
      '000001700000000:00042:', // no node
      '000001700000000:00042:dev:extra', // ':' is not a node character
      '00000170000000a:00042:dev',
      "000001700000000:00042:dev'; DROP--",
      null,
      undefined,
      42
    ]) {
      expect(parseHLC(bad), String(bad)).toBeNull();
      expect(isHLC(bad)).toBe(false);
    }
  });

  it('a malformed stamp sorts below every well-formed one', () => {
    const good = formatHLC(1000, 0, 'dev');
    expect(compare(good, 'zzzz')).toBe(1);
    expect(compare('zzzz', good)).toBe(-1);
    expect(compare('zzzz', undefined)).toBe(0);
    expect(compare(formatHLC(1000, 0, 'a'), formatHLC(1000, 1, 'a'))).toBe(-1);
    expect(compare(good, good)).toBe(0);
  });
});

describe('HLC', () => {
  it('now() is strictly monotonic even within one millisecond', () => {
    const c = new HLC();
    const stamps = Array.from({ length: 50 }, () => c.now());
    for (let i = 1; i < stamps.length; i++) {
      expect(compare(stamps[i - 1]!, stamps[i]!)).toBeLessThan(0);
    }
  });

  it('string sort equals time sort (fixed-width fields)', () => {
    vi.useFakeTimers();
    vi.setSystemTime(999);
    const c = new HLC();
    const a = c.now();
    vi.setSystemTime(1000); // more digits than 999
    const b = c.now();
    expect(compare(a, b)).toBeLessThan(0);
    expect(parseHLC(b)!.ms).toBe(1000);
  });

  it('recv() advances past a remote stamp from the future', () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000);
    const c = new HLC();
    const local = c.now();
    // Remote clock is ahead by a minute.
    const remote = `${String(61_000).padStart(15, '0')}:00003:other`;
    expect(c.recv(remote)).toBe(true);
    const after = c.now();
    expect(compare(after, remote)).toBeGreaterThan(0);
    expect(compare(after, local)).toBeGreaterThan(0);
  });

  it('recv() keeps ticking when the local wall clock is behind', () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000);
    const c = new HLC();
    const remote = `${String(5_000).padStart(15, '0')}:00000:other`;
    c.recv(remote);
    const s1 = c.now();
    c.recv(remote); // same remote again must not move time backwards
    const s2 = c.now();
    expect(compare(s1, s2)).toBeLessThan(0);
  });

  it('two clocks never produce equal stamps (node id tie-break)', () => {
    vi.useFakeTimers();
    vi.setSystemTime(42);
    localStorage.removeItem('obh.nodeId');
    const a = new HLC();
    localStorage.removeItem('obh.nodeId');
    const b = new HLC();
    expect(a.now()).not.toBe(b.now());
  });

  // The counter has five digits. A sixth one sorted below the stamp before
  // it ("100000" < "99999" as strings).
  it('a full counter rolls into the next millisecond', () => {
    vi.useFakeTimers();
    vi.setSystemTime(5_000);
    const c = new HLC();
    c.recv(formatHLC(5_000, 99_997, 'other'));
    const a = c.now();
    const b = c.now();
    expect(parseHLC(a)).toMatchObject({ ms: 5_000, counter: 99_999 });
    expect(parseHLC(b)).toMatchObject({ ms: 5_001, counter: 0 });
    expect(compare(a, b)).toBeLessThan(0);
    c.recv(formatHLC(5_001, 99_999, 'other'));
    const d = c.now();
    expect(isHLC(d)).toBe(true);
    expect(compare(formatHLC(5_001, 99_999, 'other'), d)).toBeLessThan(0);
  });

  // A stamp that is not a stamp used to be parsed with Number(): "9e99"
  // moved the clock to the end of time, and NaN broke it for good.
  it('a malformed remote stamp never touches the clock', () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000_000);
    const c = new HLC();
    const before = c.now();
    for (const bad of ['9e99:0:x', '999999999999999999:00000:x', 'NaN:NaN:x', '', null, {}]) {
      expect(c.recv(bad)).toBe(false);
    }
    const after = c.now();
    expect(parseHLC(after)!.ms).toBe(1_000_000);
    expect(compare(before, after)).toBeLessThan(0);
  });

  it('does not follow a remote stamp further ahead than the server would accept', () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000_000);
    const c = new HLC();
    expect(c.recv(formatHLC(1_000_000 + MAX_DRIFT_MS + 1, 0, 'other'))).toBe(false);
    expect(parseHLC(c.now())!.ms).toBe(1_000_000);
    expect(c.recv(formatHLC(1_000_000 + MAX_DRIFT_MS, 0, 'other'))).toBe(true);
    expect(parseHLC(c.now())!.ms).toBe(1_000_000 + MAX_DRIFT_MS);
  });

  // Without this a reload in the same millisecond range, or a device clock
  // set back, issued stamps below ones already written: the newer edit lost.
  it('never issues a stamp below one from before a reload', () => {
    vi.useFakeTimers();
    vi.setSystemTime(2_000_000);
    const first = new HLC();
    first.recv(formatHLC(2_300_000, 7, 'other'));
    const last = first.now();

    vi.setSystemTime(1_500_000); // the device clock was set back, the app reloaded
    const reloaded = new HLC();
    const next = reloaded.now();
    expect(compare(last, next)).toBeLessThan(0);
  });

  it('after() issues a stamp above the given ones', () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000_000);
    const c = new HLC();
    const existing = [formatHLC(1_200_000, 4, 'other'), undefined, 'garbage'];
    const s = c.after(existing);
    expect(compare(s, existing[0])).toBeGreaterThan(0);
  });

  it('corrects for a device clock that is off, and keeps the correction across reloads', () => {
    vi.useFakeTimers();
    vi.setSystemTime(10_000_000); // device says 10_000 s
    const c = new HLC();
    c.syncTo(13_600_000, 10_000_000); // server says one hour later
    expect(c.skewMs).toBe(3_600_000);
    expect(parseHLC(c.now())!.ms).toBe(13_600_000);

    expect(new HLC().physical()).toBe(13_600_000);

    // A round trip's worth of difference is not a wrong clock.
    c.syncTo(10_000_900, 10_000_000);
    expect(c.skewMs).toBe(0);
    c.syncTo(0, 10_000_000); // server sent no time: keep what we have
    c.syncTo(Number.NaN, 10_000_000);
    expect(c.skewMs).toBe(0);
  });

  it('rebase() drops back to the corrected wall clock', () => {
    vi.useFakeTimers();
    vi.setSystemTime(50_000_000); // device runs an hour ahead
    const c = new HLC();
    const early = c.now();
    c.syncTo(46_400_000, 50_000_000);
    expect(parseHLC(c.now())!.ms).toBe(50_000_000); // still monotonic
    c.rebase();
    const fixed = c.now();
    expect(parseHLC(fixed)!.ms).toBe(46_400_000);
    expect(compare(fixed, early)).toBeLessThan(0);
  });

  it('replaces a stored node id that could not be part of a stamp', () => {
    localStorage.setItem('obh.nodeId', 'bad:id');
    const c = new HLC();
    expect(isHLC(c.now())).toBe(true);
    expect(localStorage.getItem('obh.nodeId')).toBe(c.node);
  });
});
