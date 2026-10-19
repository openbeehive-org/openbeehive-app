import { describe, it, expect, vi, afterEach } from 'vitest';
import { HLC, compare } from './hlc';

afterEach(() => vi.useRealTimers());

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
  });

  it('recv() advances past a remote stamp from the future', () => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000);
    const c = new HLC();
    const local = c.now();
    // Remote clock is ahead by a minute.
    const remote = `${String(61_000).padStart(15, '0')}:00003:other`;
    c.recv(remote);
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
});
