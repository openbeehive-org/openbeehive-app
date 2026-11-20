import { describe, it, expect, vi } from 'vitest';
import { fromDateInput, toDateInput } from '#lib/dates.js';
import { changedFrom, dotted, storedDate } from './forms';
import { createSaver } from './saver';
import { deleteWithUndo } from './deleting';
import { get } from 'svelte/store';

describe('storedDate', () => {
  it('keeps the stored timestamp when the day was not changed', () => {
    const before = new Date(2026, 4, 14, 17, 45).toISOString();
    expect(storedDate(toDateInput(before), before)).toBe(before);
  });

  it('stores a changed or new day as local noon', () => {
    const before = new Date(2026, 4, 14, 17, 45).toISOString();
    expect(storedDate('2026-05-12', before)).toBe(fromDateInput('2026-05-12'));
    expect(storedDate('2026-05-12')).toBe(fromDateInput('2026-05-12'));
    expect(new Date(storedDate('2026-05-12')).getHours()).toBe(12);
  });

  it('keeps an empty optional date empty', () => {
    expect(storedDate('', null)).toBe('');
    expect(storedDate('', '2026-05-14T10:00:00.000Z')).toBe('');
  });
});

describe('createSaver', () => {
  it('runs a save once when the button is tapped twice', async () => {
    const saver = createSaver();
    let release!: () => void;
    const fn = vi.fn(() => new Promise<void>((r) => (release = r)));

    const first = saver.run(fn);
    const second = saver.run(fn);
    expect(get(saver.busy)).toBe(true);
    expect(await second).toBe(false);
    release();
    expect(await first).toBe(true);

    expect(fn).toHaveBeenCalledTimes(1);
    expect(get(saver.busy)).toBe(false);
  });

  it('reports a failure and allows another attempt', async () => {
    const saver = createSaver();
    expect(
      await saver.run(async () => {
        throw new Error('disk full');
      })
    ).toBe(false);
    expect(get(saver.error)).toBe('disk full');
    expect(get(saver.busy)).toBe(false);

    expect(await saver.run(async () => {})).toBe(true);
    expect(get(saver.error)).toBe('');
  });
});

describe('deleteWithUndo', () => {
  const setup = (confirmed: boolean) => {
    const undo = vi.fn(async () => {});
    const run = vi.fn(async () => undo);
    const reload = vi.fn();
    const offer = vi.fn();
    const confirm = vi.fn(() => confirmed);
    const start = () =>
      deleteWithUndo({
        ask: 'Delete the visit of 14 May?',
        done: 'Deleted',
        run,
        reload,
        offer,
        confirm
      });
    return { undo, run, reload, offer, confirm, start };
  };

  it('does nothing when the question is declined', async () => {
    const s = setup(false);
    expect(await s.start()).toBe(false);
    expect(s.confirm).toHaveBeenCalledWith('Delete the visit of 14 May?');
    expect(s.run).not.toHaveBeenCalled();
    expect(s.offer).not.toHaveBeenCalled();
  });

  it('deletes, reloads and offers an undo that restores and reloads again', async () => {
    const s = setup(true);
    expect(await s.start()).toBe(true);
    expect(s.run).toHaveBeenCalledTimes(1);
    expect(s.reload).toHaveBeenCalledTimes(1);
    expect(s.offer).toHaveBeenCalledWith('Deleted', expect.any(Function));

    await s.offer.mock.calls[0]![1]();
    expect(s.undo).toHaveBeenCalledTimes(1);
    expect(s.reload).toHaveBeenCalledTimes(2);
  });
});

describe('dotted', () => {
  it('joins the parts that have a value', () => {
    expect(dotted('Blue', 'own breeding')).toBe('Blue · own breeding');
    expect(dotted('Blue', '', null, undefined, false)).toBe('Blue');
    expect(dotted('Honey', 0)).toBe('Honey · 0');
  });
});

describe('changedFrom', () => {
  it('keeps only the fields that differ from the values the form was opened with', () => {
    const opened = { name: 'Orchard', note: 'barn', lat: 0, lng: 0 };
    expect(changedFrom(opened, { ...opened, name: 'Meadow' })).toEqual({ name: 'Meadow' });
    expect(changedFrom(opened, { ...opened })).toEqual({});
  });

  it('treats an empty string and an unset value as equal', () => {
    expect(changedFrom({ note: null as string | null }, { note: '' })).toEqual({});
    expect(changedFrom({ note: '' as string | null }, { note: null })).toEqual({});
    expect(changedFrom({ n: null as number | null }, { n: 0 })).toEqual({ n: 0 });
  });
});
