import { describe, it, expect } from 'vitest';
import { xPositions } from './chartScale';

describe('xPositions', () => {
  it('centres a single point', () => {
    expect(xPositions(1, 300, 8)).toEqual([150]);
  });

  it('spreads points evenly without timestamps', () => {
    expect(xPositions(3, 300, 10)).toEqual([10, 150, 290]);
  });

  it('spaces points by the time between them', () => {
    expect(xPositions(3, 300, 10, [0, 10, 100])).toEqual([10, 38, 290]);
  });

  it('falls back to even spacing when all readings share one instant', () => {
    expect(xPositions(3, 300, 10, [5, 5, 5])).toEqual([10, 150, 290]);
  });
});
