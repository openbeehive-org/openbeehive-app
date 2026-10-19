import { describe, it, expect, beforeEach, vi } from 'vitest';
import { makeLocalDB, setCurrentDB, getCurrentDB } from '../../tests/localdb';

vi.mock('./db', async () => {
  const { getCurrentDB } = await import('../../tests/localdb');
  return { getDB: async () => getCurrentDB() };
});
// repo fires a background sync after every write; irrelevant here.
vi.mock('./sync', () => ({ syncOnce: vi.fn(async () => {}) }));

import { apiaries, hives, tasks, setAdd, setRemove, patch, remove } from './repo';
import { parseFieldClock, parseORSet, orValues } from './merge';

beforeEach(async () => {
  setCurrentDB(await makeLocalDB());
  localStorage.setItem('obh.orgId', 'org1');
  localStorage.setItem('obh.userId', 'user1');
});

describe('apiaries repo', () => {
  it('create writes the row, stamps every field clock and queues the outbox', async () => {
    const id = await apiaries.create({
      organization_id: 'org1',
      name: 'Orchard',
      address: 'A st',
      lat: 1.5,
      lng: 2.5,
      note: 'n'
    });

    const row = await apiaries.get(id);
    expect(row).toMatchObject({
      id,
      name: 'Orchard',
      address: 'A st',
      lat: 1.5,
      lng: 2.5,
      deleted: 0
    });

    const fc = parseFieldClock(row!.field_hlc);
    for (const f of [
      'organization_id',
      'name',
      'address',
      'lat',
      'lng',
      'note',
      'created_at',
      'updated_at',
      'deleted'
    ]) {
      expect(fc[f], `field clock for ${f}`).toBeTruthy();
    }

    const db = getCurrentDB();
    const out = await db.all<any>(`SELECT * FROM outbox`);
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({
      entity: 'apiary',
      entity_id: id,
      scope_id: id,
      op: 1,
      author_id: 'user1'
    });
    expect(JSON.parse(out[0].payload).name).toBe('Orchard');
  });

  it('update patches only the given fields and advances only their clocks', async () => {
    const id = await apiaries.create({ organization_id: 'org1', name: 'Old' });
    const before = parseFieldClock((await apiaries.get(id))!.field_hlc);

    await apiaries.rename(id, 'New');
    const row = await apiaries.get(id);
    const after = parseFieldClock(row!.field_hlc);

    expect(row!.name).toBe('New');
    expect(after.name! > before.name!).toBe(true);
    expect(after.note).toBe(before.note); // untouched field keeps its stamp
  });

  it('remove tombstones the row and hides it from list/get', async () => {
    const id = await apiaries.create({ organization_id: 'org1', name: 'Gone' });
    await apiaries.remove(id);

    expect(await apiaries.get(id)).toBeUndefined();
    expect(await apiaries.list()).toHaveLength(0);

    const raw = await getCurrentDB().get<any>(
      `SELECT deleted, field_hlc FROM apiary WHERE id = ?`,
      [id]
    );
    expect(raw!.deleted).toBe(1);
    expect(parseFieldClock(raw!.field_hlc).deleted).toBeTruthy();
  });

  it('empty-name guard lives in the UI; repo trusts its callers with scope = own id', async () => {
    const id = await apiaries.create({ organization_id: 'org1', name: 'X' });
    const out = await getCurrentDB().all<any>(`SELECT scope_id FROM outbox`);
    expect(out[0].scope_id).toBe(id);
  });
});

describe('hives repo', () => {
  it('count only sees live hives of the apiary', async () => {
    const db = getCurrentDB();
    await patch('hive', 'h1', 'a1', {
      organization_id: 'org1',
      apiary_id: 'a1',
      name: 'H1',
      deleted: 0
    });
    await patch('hive', 'h2', 'a1', {
      organization_id: 'org1',
      apiary_id: 'a1',
      name: 'H2',
      deleted: 0
    });
    await patch('hive', 'h3', 'a2', {
      organization_id: 'org1',
      apiary_id: 'a2',
      name: 'H3',
      deleted: 0
    });
    await remove('hive', 'h2', 'a1');

    expect(await hives.count('a1')).toBe(1);
    expect(await hives.count('a2')).toBe(1);
    expect((await hives.list()).map((h) => h.id).sort()).toEqual(['h1', 'h3']);
    void db;
  });
});

describe('tasks repo', () => {
  it('create + toggle + remove round-trip', async () => {
    const id = await tasks.create({ title: 'feed' });
    let all = await tasks.list();
    expect(all).toHaveLength(1);
    expect(all[0]).toMatchObject({ id, title: 'feed', done: 0 });
    expect(await tasks.openCount()).toBe(1);

    await tasks.toggle(all[0]);
    all = await tasks.list();
    expect(all[0].done).toBe(1);
    expect(await tasks.openCount()).toBe(0);

    await tasks.remove(all[0]);
    expect(await tasks.list()).toHaveLength(0);
  });

  it('a personal task syncs under the user scope, an apiary task under the apiary', async () => {
    await tasks.create({ title: 'personal' });
    await tasks.create({ title: 'shared', apiary_id: 'a9' });
    const out = await getCurrentDB().all<any>(
      `SELECT scope_id, payload FROM outbox ORDER BY rowid`
    );
    expect(out[0].scope_id).toBe('user:user1');
    expect(out[1].scope_id).toBe('a9');
  });
});

describe('OR-Set columns through the repo', () => {
  it('setAdd/setRemove maintain photo_keys and queue delta ops', async () => {
    await patch('inspection', 'i1', 'a1', { organization_id: 'org1', hive_id: 'h1', deleted: 0 });

    await setAdd('inspection', 'i1', 'a1', 'photo_keys', 'p1');
    await setAdd('inspection', 'i1', 'a1', 'photo_keys', 'p2');
    await setRemove('inspection', 'i1', 'a1', 'photo_keys', 'p1');

    const row = await getCurrentDB().get<any>(`SELECT photo_keys FROM inspection WHERE id = 'i1'`);
    expect(orValues(parseORSet(row!.photo_keys)).sort()).toEqual(['p2']);

    const ops = await getCurrentDB().all<any>(`SELECT payload FROM outbox ORDER BY rowid`);
    const last = JSON.parse(ops[ops.length - 1].payload);
    expect(last.photo_keys.remove).toEqual(['p1']);
  });
});
