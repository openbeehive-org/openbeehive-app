import { describe, it, expect, beforeEach, vi } from 'vitest';
import { makeLocalDB, setCurrentDB, getCurrentDB } from '../../tests/localdb';

vi.mock('./db', async () => {
  const { getCurrentDB } = await import('../../tests/localdb');
  return { getDB: async () => getCurrentDB() };
});
// repo fires a background sync after every write; irrelevant here.
vi.mock('./sync', () => ({ syncOnce: vi.fn(async () => {}) }));

import { apiaries, hives, tasks, patch, remove } from './repo';

beforeEach(async () => {
  setCurrentDB(await makeLocalDB());
  localStorage.setItem('obh.orgId', 'org1');
  localStorage.setItem('obh.userId', 'user1');
});

const hive = (id: string, apiaryId: string, name: string, extra: Record<string, unknown> = {}) =>
  patch('hive', id, apiaryId, {
    organization_id: 'org1',
    apiary_id: apiaryId,
    name,
    status: 1,
    deleted: 0,
    ...extra
  });

const visit = (id: string, hiveId: string, date: string, extra: Record<string, unknown> = {}) =>
  patch('inspection', id, 'a1', {
    organization_id: 'org1',
    hive_id: hiveId,
    date,
    deleted: 0,
    ...extra
  });

describe('hives.overview', () => {
  it('returns the last visit a person recorded, per hive', async () => {
    const a1 = await apiaries.create({ organization_id: 'org1', name: 'Orchard' });
    await hive('h1', a1, 'Hive 1');
    await hive('h2', a1, 'Hive 2');
    await hive('h3', a1, 'Hive 3');
    await visit('i1', 'h1', '2026-04-01T10:00:00.000Z');
    await visit('i2', 'h1', '2026-05-20T10:00:00.000Z', { source: 1 });
    await visit('i3', 'h1', '2026-03-01T10:00:00.000Z');
    await visit('i4', 'h2', '2026-02-02T10:00:00.000Z');

    const rows = await hives.overview();
    expect(rows.map((r) => [r.id, r.last_inspection])).toEqual([
      ['h1', '2026-05-20T10:00:00.000Z'],
      ['h2', '2026-02-02T10:00:00.000Z'],
      ['h3', null]
    ]);
    expect(rows[0]).toMatchObject({ name: 'Hive 1', apiary_id: a1, apiary_name: 'Orchard' });
  });

  it('ignores sensor readings, however recent', async () => {
    await hive('h1', 'a1', 'Scale hive');
    await hive('h2', 'a1', 'Sensor only');
    await visit('i1', 'h1', '2026-04-01T10:00:00.000Z', { source: 1 });
    await visit('i2', 'h1', '2026-06-30T10:00:00.000Z', { source: 2 });
    await visit('i3', 'h2', '2026-06-30T10:00:00.000Z', { source: 2 });

    const rows = await hives.overview();
    expect(rows.find((r) => r.id === 'h1')!.last_inspection).toBe('2026-04-01T10:00:00.000Z');
    expect(rows.find((r) => r.id === 'h2')!.last_inspection).toBeNull();
  });

  it('counts visits stored before the source column existed as manual', async () => {
    await hive('h1', 'a1', 'Old');
    await getCurrentDB().exec(
      `INSERT INTO inspection (id, hive_id, date, deleted, source) VALUES (?, ?, ?, 0, NULL)`,
      ['i1', 'h1', '2026-01-05T10:00:00.000Z']
    );
    expect((await hives.overview())[0]!.last_inspection).toBe('2026-01-05T10:00:00.000Z');
  });

  it('skips deleted visits and deleted hives', async () => {
    await hive('h1', 'a1', 'Kept');
    await hive('h2', 'a1', 'Removed');
    await visit('i1', 'h1', '2026-04-01T10:00:00.000Z');
    await visit('i2', 'h1', '2026-06-01T10:00:00.000Z');
    await visit('i3', 'h2', '2026-06-01T10:00:00.000Z');
    await remove('inspection', 'i2', 'a1');
    await remove('hive', 'h2', 'a1');

    const rows = await hives.overview();
    expect(rows.map((r) => r.id)).toEqual(['h1']);
    expect(rows[0]!.last_inspection).toBe('2026-04-01T10:00:00.000Z');
  });

  it('keeps hives whose apiary was deleted, with a null apiary name', async () => {
    const gone = await apiaries.create({ organization_id: 'org1', name: 'Gone' });
    await hive('h1', gone, 'Orphan');
    // Another device deleted the apiary while this hive was added to it.
    await remove('apiary', gone, gone);
    await hive('h2', 'never-synced', 'Stray');

    const rows = await hives.overview();
    expect(rows.map((r) => [r.name, r.apiary_name])).toEqual([
      ['Orphan', null],
      ['Stray', null]
    ]);
    expect((await hives.overview(gone)).map((r) => r.id)).toEqual(['h1']);
  });

  it('adds the active queen and filters by apiary', async () => {
    await hive('h1', 'a1', 'A');
    await hive('h2', 'a2', 'B');
    const queen = (id: string, year: number, active: number, introduced: string) =>
      patch('queen', id, 'a1', {
        hive_id: 'h1',
        year,
        marking: 3,
        active,
        introduced_at: introduced,
        deleted: 0
      });
    await queen('q1', 2024, 0, '2024-05-01T00:00:00.000Z');
    await queen('q2', 2026, 1, '2026-05-01T00:00:00.000Z');

    const rows = await hives.overview('a1');
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ id: 'h1', queen_year: 2026, queen_marking: 3 });
    expect((await hives.overview('a2'))[0]).toMatchObject({ queen_year: null });
  });

  it('answers the last-visit lookup from the index, not from the visit rows', async () => {
    const plan = await getCurrentDB().all<{ detail: string }>(
      `EXPLAIN QUERY PLAN
       SELECT hive_id, MAX(date) AS date FROM inspection
       WHERE deleted = 0 AND COALESCE(source, 0) <> 2 GROUP BY hive_id`
    );
    expect(plan.map((p) => p.detail).join(' | ')).toContain('COVERING INDEX idx_inspection_last');
  });
});

describe('natural name order in the repo lists', () => {
  it('sorts hives and apiaries like a person would', async () => {
    for (const [i, name] of ['Hive 10', 'hive 2', 'Hive 1', 'apple', 'Zeta', 'Älm'].entries()) {
      await hive(`h${i}`, 'a1', name);
    }
    const expected = ['Älm', 'apple', 'Hive 1', 'hive 2', 'Hive 10', 'Zeta'];
    expect((await hives.list()).map((h) => h.name)).toEqual(expected);
    expect((await hives.list('a1')).map((h) => h.name)).toEqual(expected);
    expect((await hives.overview()).map((h) => h.name)).toEqual(expected);

    for (const name of ['Site 12', 'site 3', 'Site 1']) {
      await apiaries.create({ organization_id: 'org1', name });
    }
    expect((await apiaries.list()).map((a) => a.name)).toEqual(['Site 1', 'site 3', 'Site 12']);
  });
});

describe('personal tasks and workspaces', () => {
  it('shows a personal task only in the workspace it was created in', async () => {
    await tasks.create({ title: 'buy jars' });
    await tasks.create({ title: 'mow the orchard', apiary_id: 'a1' });
    expect((await tasks.list()).map((t) => t.title).sort()).toEqual([
      'buy jars',
      'mow the orchard'
    ]);
    expect(await tasks.openCount()).toBe(2);

    // The same user's personal task arrives in another workspace's database.
    setCurrentDB(await makeLocalDB());
    localStorage.setItem('obh.orgId', 'org2');
    await getCurrentDB().exec(
      `INSERT INTO task (id, organization_id, title, apiary_id, due_at, done, deleted)
       VALUES ('t1', 'org1', 'buy jars', '', '', 0, 0)`
    );
    await tasks.create({ title: 'club meeting' });
    await tasks.create({ title: 'club apiary fence', apiary_id: 'a7' });

    expect((await tasks.list()).map((t) => t.title).sort()).toEqual([
      'club apiary fence',
      'club meeting'
    ]);
    expect(await tasks.openCount()).toBe(2);
  });
});
