import { afterEach, describe, expect, it } from 'vitest';
import { formatDate, fromDateInput, parseStored, toDateInput, todayLocal } from './dates';

const originalTZ = process.env.TZ;
afterEach(() => {
  process.env.TZ = originalTZ;
});

describe.each(['America/Los_Angeles', 'UTC', 'Europe/Berlin', 'Pacific/Auckland'])(
  'calendar dates in %s',
  (tz) => {
    it('round-trips a date input through storage on the same day', () => {
      process.env.TZ = tz;
      const stored = fromDateInput('2026-03-14');
      expect(toDateInput(stored)).toBe('2026-03-14');
      expect(formatDate(stored, 'en-GB', { dateStyle: 'short' })).toBe('14/03/2026');
    });

    it('uses the local day for today, late in the evening too', () => {
      process.env.TZ = tz;
      const lateEvening = new Date(2026, 5, 30, 23, 30);
      expect(todayLocal(lateEvening)).toBe('2026-06-30');
    });

    it('reads a bare stored date as that calendar day', () => {
      process.env.TZ = tz;
      expect(toDateInput('2026-11-01')).toBe('2026-11-01');
    });

    it('reads a date stored as UTC midnight as that calendar day', () => {
      process.env.TZ = tz;
      expect(toDateInput('2026-11-01T00:00:00.000Z')).toBe('2026-11-01');
      expect(toDateInput('2026-11-01T00:00:00Z')).toBe('2026-11-01');
    });
  }
);

describe('edge cases', () => {
  it('keeps empty and invalid values empty', () => {
    expect(fromDateInput('')).toBe('');
    expect(fromDateInput('14.03.2026')).toBe('');
    expect(toDateInput(null)).toBe('');
    expect(toDateInput('not a date')).toBe('');
    expect(parseStored('nope')).toBeNull();
    expect(formatDate(undefined, 'en')).toBe('');
    expect(formatDate('nope', 'en')).toBe('');
  });

  it('formats in the requested UI language', () => {
    const stored = fromDateInput('2026-03-14');
    expect(formatDate(stored, 'de', { dateStyle: 'long' })).toBe('14. März 2026');
  });
});
