import { describe, it, expect } from 'vitest';
import { encodeWithin, fitWithin, FULL_EDGE, THUMB_EDGE } from './image';

describe('fitWithin', () => {
  it('scales the longer edge down to the limit and keeps the proportions', () => {
    expect(fitWithin(4000, 3000, FULL_EDGE)).toEqual({ width: 2048, height: 1536 });
    expect(fitWithin(3000, 4000, FULL_EDGE)).toEqual({ width: 1536, height: 2048 });
    expect(fitWithin(4000, 3000, THUMB_EDGE)).toEqual({ width: 200, height: 150 });
  });

  it('never enlarges', () => {
    expect(fitWithin(640, 480, FULL_EDGE)).toEqual({ width: 640, height: 480 });
    expect(fitWithin(2048, 2048, FULL_EDGE)).toEqual({ width: 2048, height: 2048 });
  });

  it('keeps at least one pixel on a very narrow picture', () => {
    expect(fitWithin(10_000, 3, THUMB_EDGE)).toEqual({ width: 200, height: 1 });
    expect(fitWithin(0, 0, THUMB_EDGE)).toEqual({ width: 1, height: 1 });
  });
});

describe('encodeWithin', () => {
  it('takes the first result that fits', async () => {
    const tried: number[] = [];
    const out = await encodeWithin(
      async (q) => {
        tried.push(q);
        return q * 100;
      },
      (size) => size <= 70
    );
    expect(tried).toEqual([0.85, 0.75, 0.6]);
    expect(out).toBe(60);
  });

  it('does not encode again when the first result fits', async () => {
    let calls = 0;
    await encodeWithin(
      async () => ++calls,
      () => true
    );
    expect(calls).toBe(1);
  });

  it('returns the smallest attempt when nothing fits', async () => {
    const out = await encodeWithin(
      async (q) => q,
      () => false
    );
    expect(out).toBe(0.45);
  });
});
