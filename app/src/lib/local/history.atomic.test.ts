import { describe, it, expect, beforeEach, vi } from 'vitest';
import { makeLocalDB, setCurrentDB, getCurrentDB } from '../../tests/localdb';

vi.mock('./db', async () => {
  const { getCurrentDB } = await import('../../tests/localdb');
  return { getDB: async () => getCurrentDB() };
});
// Flows fire a background sync after they commit; irrelevant here.
vi.mock('./sync', () => ({ syncOnce: vi.fn(async () => {}) }));

import {
  createHive,
  moveHive,
  setQueen,
  recordHarvest,
  recordTreatment,
  recordInspection,
  editHarvest,
  editInspection,
  editQueen,
  deleteInspection,
  deleteQueen,
  updateHive
} from './history';
import { syncOnce } from './sync';

const count = async (table: string) =>
  (await getCurrentDB().get<{ n: number }>(`SELECT COUNT(*) AS n FROM ${table}`))!.n;

beforeEach(async () => {
  setCurrentDB(await makeLocalDB());
  localStorage.setItem('obh.orgId', 'org1');
  localStorage.setItem('obh.userId', 'user1');
  vi.mocked(syncOnce).mockClear();
});

describe('history flows are atomic', () => {
  it('createHive stores hive, placement, event and their outbox entries in one transaction', async () => {
    const db = getCurrentDB();
    const batch = vi.spyOn(db, 'batch');

    const id = await createHive({ name: 'H1', apiary_id: 'a1', type: 2 });

    expect(batch).toHaveBeenCalledOnce();
    expect(batch.mock.calls[0]![0]).toHaveLength(6);
    expect(
      await db.get<any>(`SELECT name, apiary_id, status FROM hive WHERE id = ?`, [id])
    ).toEqual({
      name: 'H1',
      apiary_id: 'a1',
      status: 1
    });
    expect(await count('placement')).toBe(1);
    expect(await count('event')).toBe(1);
    const out = await db.all<any>(`SELECT entity, hlc FROM outbox ORDER BY hlc`);
    expect(out.map((o) => o.entity)).toEqual(['hive', 'placement', 'event']);
    expect(syncOnce).toHaveBeenCalledOnce();
  });

  // A hive without its placement and event breaks the location history and
  // the timeline for good; the three writes used to be independent.
  it('a flow that fails halfway leaves nothing behind', async () => {
    const db = getCurrentDB();
    await db.exec(`ALTER TABLE event RENAME TO event_gone`);
    await expect(createHive({ name: 'H1', apiary_id: 'a1' })).rejects.toThrow();
    await db.exec(`ALTER TABLE event_gone RENAME TO event`);

    expect(await count('hive')).toBe(0);
    expect(await count('placement')).toBe(0);
    expect(await count('outbox')).toBe(0);
    expect(syncOnce).not.toHaveBeenCalled();
  });

  it('the other flows commit once as well', async () => {
    const db = getCurrentDB();
    const hive = await createHive({ name: 'H1', apiary_id: 'a1' });
    const batch = vi.spyOn(db, 'batch');

    await setQueen(hive, 'a1', { year: 2026 });
    await setQueen(hive, 'a1', { year: 2027 }); // ends the first queen's reign
    await moveHive(hive, 'a1', 'a2');
    await recordHarvest({ hiveId: hive, amount_kg: 12.5, variety: 'Lime' });
    await recordTreatment({ hiveId: hive, product: 'Oxalic acid' });
    await recordInspection(hive, { frames: 9 });

    expect(batch).toHaveBeenCalledTimes(6);
    expect(await db.all<any>(`SELECT year, active FROM queen ORDER BY year`)).toEqual([
      { year: 2026, active: 0 },
      { year: 2027, active: 1 }
    ]);
    expect((await db.get<any>(`SELECT apiary_id FROM hive WHERE id = ?`, [hive]))!.apiary_id).toBe(
      'a2'
    );
    expect(await count('placement')).toBe(2);
    expect(await count('harvest')).toBe(1);
    expect(await count('treatment')).toBe(1);
    expect(await count('inspection')).toBe(1);
    // One event per step: created, 2 introduced + 1 replaced, moved, harvest, treatment, inspection.
    expect(await count('event')).toBe(8);
    // hive+placement+event, 2 + 4 for the queens, 4 for the move, 2 each for the records.
    expect(await count('outbox')).toBe(19);
  });
});

// The corrections touch a record and its events. Each used to be a series
// of independent writes: a tab closed (or a statement failing) in between
// left a changed record with a stale timeline entry, or a deleted record
// that the honey totals still counted.
describe('corrections are atomic', () => {
  const outbox = () => count('outbox');
  const one = (sql: string, params: unknown[] = []) => getCurrentDB().get<any>(sql, params);

  // Lets the record's own statement through and fails the one for its event,
  // which is the second row a correction writes.
  async function refuseEventWrites(kind: 'UPDATE' | 'INSERT' = 'UPDATE') {
    await getCurrentDB().exec(
      `CREATE TRIGGER refuse_event BEFORE ${kind} ON event
       BEGIN SELECT RAISE(ABORT, 'event write refused'); END`
    );
  }
  const allowEventWrites = () => getCurrentDB().exec(`DROP TRIGGER refuse_event`);

  it('an edit that fails on the event leaves the record untouched and queues nothing', async () => {
    const hive = await createHive({ name: 'H1', apiary_id: 'a1' });
    const id = await recordHarvest({ hiveId: hive, amount_kg: 10, variety: 'Lime' });
    const before = await one(`SELECT * FROM harvest WHERE id = ?`, [id]);
    const queued = await outbox();

    await refuseEventWrites();
    await expect(editHarvest(id, { amount_kg: 12.5 })).rejects.toThrow(/event write refused/);
    await expect(editInspection('missing', { note: 'x' })).rejects.toThrow(/not found/);

    expect(await one(`SELECT * FROM harvest WHERE id = ?`, [id])).toEqual(before);
    expect(await outbox()).toBe(queued);

    // Once the write goes through, record and fact row change together.
    await allowEventWrites();
    const batch = vi.spyOn(getCurrentDB(), 'batch');
    expect(await editHarvest(id, { amount_kg: 12.5 })).toBe(true);
    expect(batch).toHaveBeenCalledOnce();
    expect(
      (await one(`SELECT amount_kg, amended_at FROM harvest WHERE id = ?`, [id])).amount_kg
    ).toBe(12.5);
    expect(await one(`SELECT amount_kg, title FROM event WHERE ref_id = ?`, [id])).toEqual({
      amount_kg: 12.5,
      title: '12.5 kg Lime'
    });
    expect(await outbox()).toBe(queued + 2);
  });

  it('a delete that fails on the event deletes nothing; its undo is all or nothing too', async () => {
    const hive = await createHive({ name: 'H1', apiary_id: 'a1' });
    const id = await recordInspection(hive, { frames: 9 });
    const queued = await outbox();
    const live = async () => ({
      record: (await one(`SELECT deleted FROM inspection WHERE id = ?`, [id])).deleted,
      event: (await one(`SELECT deleted FROM event WHERE ref_id = ?`, [id])).deleted
    });

    await refuseEventWrites();
    await expect(deleteInspection(id)).rejects.toThrow(/event write refused/);
    expect(await live()).toEqual({ record: 0, event: 0 });
    expect(await outbox()).toBe(queued);

    await allowEventWrites();
    const batch = vi.spyOn(getCurrentDB(), 'batch');
    const undo = await deleteInspection(id);
    expect(batch).toHaveBeenCalledOnce();
    expect(await live()).toEqual({ record: 1, event: 1 });
    expect(await outbox()).toBe(queued + 2);

    // An undo that cannot restore the event does not restore the record either.
    await refuseEventWrites();
    await expect(undo()).rejects.toThrow(/event write refused/);
    expect(await live()).toEqual({ record: 1, event: 1 });
    expect(await outbox()).toBe(queued + 2);
    await allowEventWrites();
    await undo();
    expect(await live()).toEqual({ record: 0, event: 0 });
    expect(await outbox()).toBe(queued + 4);
  });

  it('deleting the current queen and reinstating her predecessor is one write', async () => {
    const hive = await createHive({ name: 'H1', apiary_id: 'a1' });
    const first = await setQueen(hive, 'a1', {
      year: 2025,
      introduced_at: '2025-05-01T10:00:00.000Z'
    });
    const second = await setQueen(hive, 'a1', {
      year: 2026,
      introduced_at: '2026-05-01T10:00:00.000Z'
    });
    const queens = () =>
      getCurrentDB().all<any>(`SELECT year, active, deleted FROM queen ORDER BY year`);
    const queued = await outbox();

    await refuseEventWrites();
    await expect(deleteQueen(second)).rejects.toThrow(/event write refused/);
    expect(await queens()).toEqual([
      { year: 2025, active: 0, deleted: 0 },
      { year: 2026, active: 1, deleted: 0 }
    ]);
    expect(await outbox()).toBe(queued);

    await allowEventWrites();
    const batch = vi.spyOn(getCurrentDB(), 'batch');
    const undo = await deleteQueen(second);
    expect(batch).toHaveBeenCalledOnce();
    expect(await queens()).toEqual([
      { year: 2025, active: 1, deleted: 0 },
      { year: 2026, active: 1, deleted: 1 }
    ]);
    expect(
      (await one(`SELECT replaced_at FROM queen WHERE id = ?`, [first])).replaced_at
    ).toBeNull();

    await undo();
    expect(batch).toHaveBeenCalledTimes(2);
    expect(await queens()).toEqual([
      { year: 2025, active: 0, deleted: 0 },
      { year: 2026, active: 1, deleted: 0 }
    ]);
    expect(await count('event WHERE deleted = 1')).toBe(0);
  });

  it('a corrected introduction date moves both queens and their events together', async () => {
    const hive = await createHive({ name: 'H1', apiary_id: 'a1' });
    const first = await setQueen(hive, 'a1', {
      year: 2025,
      introduced_at: '2025-05-01T10:00:00.000Z'
    });
    const second = await setQueen(hive, 'a1', {
      year: 2026,
      introduced_at: '2026-05-01T10:00:00.000Z'
    });
    const dates = async () => ({
      replaced: (await one(`SELECT replaced_at FROM queen WHERE id = ?`, [first])).replaced_at,
      introduced: (await one(`SELECT introduced_at FROM queen WHERE id = ?`, [second]))
        .introduced_at
    });
    const before = await dates();
    const queued = await outbox();

    await refuseEventWrites();
    await expect(
      editQueen(second, { introduced_at: '2026-06-01T10:00:00.000Z' })
    ).rejects.toThrow();
    expect(await dates()).toEqual(before);
    expect(await outbox()).toBe(queued);

    await allowEventWrites();
    const batch = vi.spyOn(getCurrentDB(), 'batch');
    await editQueen(second, { introduced_at: '2026-06-01T10:00:00.000Z' });
    expect(batch).toHaveBeenCalledOnce();
    expect(await dates()).toEqual({
      replaced: '2026-06-01T10:00:00.000Z',
      introduced: '2026-06-01T10:00:00.000Z'
    });
    expect(await outbox()).toBe(queued + 4); // two queens, two events
  });

  it('a status change is stored with its event or not at all', async () => {
    const hive = await createHive({ name: 'H1', apiary_id: 'a1' });
    const queued = await outbox();

    await refuseEventWrites('INSERT');
    await expect(updateHive(hive, 'a1', { status: 5 })).rejects.toThrow(/event write refused/);
    expect((await one(`SELECT status FROM hive WHERE id = ?`, [hive])).status).toBe(1);
    expect(await outbox()).toBe(queued);

    await allowEventWrites();
    await updateHive(hive, 'a1', { status: 5 });
    expect((await one(`SELECT status FROM hive WHERE id = ?`, [hive])).status).toBe(5);
    expect(await count('event')).toBe(2);
    expect(await outbox()).toBe(queued + 2);
  });
});
