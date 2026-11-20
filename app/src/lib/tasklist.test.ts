import { afterEach, describe, expect, it } from 'vitest';
import { formatDate, fromDateInput } from './dates';
import { dueDay, isOverdue, splitTasks } from './tasklist';

const originalTZ = process.env.TZ;
afterEach(() => {
  process.env.TZ = originalTZ;
});

describe.each(['America/Los_Angeles', 'UTC', 'Europe/Berlin', 'Pacific/Auckland'])(
  'task due dates in %s',
  (tz) => {
    it('keeps the calendar day of every stored form', () => {
      process.env.TZ = tz;
      expect(dueDay('2026-11-20')).toBe('2026-11-20'); // older tasks: bare date
      expect(dueDay(fromDateInput('2026-11-20'))).toBe('2026-11-20'); // local noon
      expect(dueDay('2026-11-20T00:00:00Z')).toBe('2026-11-20'); // created through the API
      expect(dueDay('2026-11-20T00:00:00.000Z')).toBe('2026-11-20');
      expect(dueDay('')).toBe('');
      expect(dueDay(null)).toBe('');
    });

    it('formats the due day in the UI language without shifting it', () => {
      process.env.TZ = tz;
      expect(formatDate(dueDay('2026-11-20T00:00:00Z'), 'en-GB')).toBe('20 Nov 2026');
      expect(formatDate(dueDay('2026-11-20'), 'de')).toBe('20.11.2026');
    });

    it('is overdue only from the day after', () => {
      process.env.TZ = tz;
      expect(isOverdue('2026-11-20', '2026-11-20')).toBe(false);
      expect(isOverdue('2026-11-20T00:00:00Z', '2026-11-21')).toBe(true);
      expect(isOverdue(fromDateInput('2026-11-20'), '2026-11-19')).toBe(false);
      expect(isOverdue('', '2026-11-21')).toBe(false);
    });
  }
);

describe('splitTasks', () => {
  it('separates completed tasks and orders open ones by due day, undated last', () => {
    const rows = [
      { title: 'done', done: 1, due_at: '2026-01-01' },
      { title: 'no date', done: 0, due_at: '' },
      { title: 'later', done: 0, due_at: '2026-12-01' },
      { title: 'soon', done: 0, due_at: '2026-11-21T00:00:00Z' },
      { title: 'also no date', done: 0, due_at: null }
    ];
    const { open, completed } = splitTasks(rows);
    expect(open.map((t) => t.title)).toEqual(['soon', 'later', 'no date', 'also no date']);
    expect(completed.map((t) => t.title)).toEqual(['done']);
  });
});
