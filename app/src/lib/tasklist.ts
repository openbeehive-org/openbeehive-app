// Pure helpers for the task lists (tasks page and dashboard).

import { toDateInput } from './dates';

/**
 * The calendar day a task is due, as `YYYY-MM-DD`; '' when it has no date.
 * Tasks created through the API carry a UTC-midnight timestamp that means
 * "that day", so it is read as a date rather than converted to local time
 * (which would show the previous day west of Greenwich).
 */
export function dueDay(dueAt: string | null | undefined): string {
  if (!dueAt) return '';
  const utcMidnight = /^(\d{4}-\d{2}-\d{2})T00:00:00(\.0+)?(Z|\+00:00)$/.exec(dueAt);
  return utcMidnight?.[1] ?? toDateInput(dueAt);
}

/** True when the task's due day lies before `today` (a `YYYY-MM-DD` day). */
export function isOverdue(dueAt: string | null | undefined, today: string): boolean {
  const day = dueDay(dueAt);
  return day !== '' && day < today;
}

type TaskLike = { done: number; due_at: string | null; title: string };

/**
 * Splits tasks into open and completed. Open tasks are ordered by due day,
 * undated ones last, so "what is next" reads top to bottom.
 */
export function splitTasks<T extends TaskLike>(rows: T[]): { open: T[]; completed: T[] } {
  const open = rows
    .filter((t) => !t.done)
    .sort((a, b) => {
      const da = dueDay(a.due_at);
      const db = dueDay(b.due_at);
      if (da === db) return 0;
      if (!da) return 1;
      if (!db) return -1;
      return da < db ? -1 : 1;
    });
  return { open, completed: rows.filter((t) => !!t.done) };
}
