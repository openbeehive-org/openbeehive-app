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
  recordInspection
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
