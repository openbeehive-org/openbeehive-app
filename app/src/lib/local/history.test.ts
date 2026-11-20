import { describe, it, expect, beforeEach, vi } from 'vitest';
import { makeLocalDB, setCurrentDB, getCurrentDB } from '../../tests/localdb';

vi.mock('./db', async () => {
  const { getCurrentDB } = await import('../../tests/localdb');
  return { getDB: async () => getCurrentDB() };
});
// repo fires a background sync after every write; irrelevant here.
vi.mock('./sync', () => ({ syncOnce: vi.fn(async () => {}) }));

import { apiaries, hives, inspections, harvests, treatments, queens, patch, remove } from './repo';
import {
  EventType,
  createHive,
  deleteHarvest,
  deleteInspection,
  deleteQueen,
  deleteTreatment,
  editHarvest,
  editInspection,
  editQueen,
  editTreatment,
  honeyByYear,
  moveHive,
  recordHarvest,
  recordInspection,
  recordTreatment,
  resolveContext,
  setQueen,
  updateHive
} from './history';

const events = (where = '1 = 1', params: unknown[] = []) =>
  getCurrentDB().all<any>(
    `SELECT * FROM event WHERE deleted = 0 AND ${where} ORDER BY date`,
    params
  );
const outboxSize = async () =>
  (await getCurrentDB().get<{ n: number }>(`SELECT COUNT(*) AS n FROM outbox`))!.n;
const row = (entity: string, id: string) =>
  getCurrentDB().get<any>(`SELECT * FROM ${entity} WHERE id = ?`, [id]);

let apiary = '';
let hive = '';

beforeEach(async () => {
  setCurrentDB(await makeLocalDB());
  localStorage.setItem('obh.orgId', 'org1');
  localStorage.setItem('obh.userId', 'user1');
  apiary = await apiaries.create({ organization_id: 'org1', name: 'Orchard' });
  hive = await createHive({ name: 'Hive 1', apiary_id: apiary });
});

describe('createHive', () => {
  it('opens a placement and writes a CREATED event', async () => {
    const places = await hives.locationHistory(hive);
    expect(places).toHaveLength(1);
    expect(places[0]).toMatchObject({ apiary_id: apiary, end_at: null, apiary_name: 'Orchard' });
    const ev = await events('hive_id = ?', [hive]);
    expect(ev.map((e) => e.type)).toEqual([EventType.CREATED]);
  });
});

describe('moveHive', () => {
  it('closes the open placement, opens one at the target and records the move', async () => {
    const meadow = await apiaries.create({ organization_id: 'org1', name: 'Meadow' });
    await moveHive(hive, apiary, meadow);

    expect((await hives.get(hive)).apiary_id).toBe(meadow);
    const places = await getCurrentDB().all<any>(
      `SELECT * FROM placement WHERE hive_id = ? ORDER BY start_at, end_at IS NULL`,
      [hive]
    );
    expect(places).toHaveLength(2);
    const closed = places.find((p) => p.apiary_id === apiary)!;
    const open = places.find((p) => p.apiary_id === meadow)!;
    expect(closed.end_at).toBeTruthy();
    expect(open.end_at).toBeNull();
    expect(open.start_at).toBe(closed.end_at);

    const [moved] = await events('type = ?', [EventType.MOVED]);
    expect(moved).toMatchObject({ hive_id: hive, apiary_id: meadow });
    expect(JSON.parse(moved.detail)).toEqual({ from: apiary, to: meadow });
  });
});

describe('resolveContext', () => {
  // A hive that lived in the orchard under queen A until 1 June, then moved
  // to the meadow and got queen B.
  async function twoEras() {
    const db = getCurrentDB();
    const meadow = await apiaries.create({ organization_id: 'org1', name: 'Meadow' });
    await db.exec(`UPDATE placement SET start_at = ?, end_at = ? WHERE hive_id = ?`, [
      '2026-01-01T00:00:00.000Z',
      '2026-06-01T00:00:00.000Z',
      hive
    ]);
    await patch('placement', 'p2', meadow, {
      hive_id: hive,
      apiary_id: meadow,
      start_at: '2026-06-01T00:00:00.000Z',
      end_at: null,
      deleted: 0
    });
    await patch('hive', hive, meadow, { apiary_id: meadow });
    const a = await setQueen(hive, apiary, {
      year: 2025,
      introduced_at: '2026-01-01T00:00:00.000Z'
    });
    const b = await setQueen(hive, meadow, {
      year: 2026,
      introduced_at: '2026-06-01T00:00:00.000Z'
    });
    return { meadow, a, b };
  }

  it('attributes a back-dated entry to the apiary and queen of that day', async () => {
    const { meadow, a, b } = await twoEras();
    expect(await resolveContext(hive, '2026-03-15T11:00:00.000Z')).toEqual({
      apiaryId: apiary,
      queenId: a
    });
    expect(await resolveContext(hive, '2026-07-15T10:00:00.000Z')).toEqual({
      apiaryId: meadow,
      queenId: b
    });
  });

  it('treats the day of a change as belonging to the new era', async () => {
    const { meadow, b } = await twoEras();
    expect(await resolveContext(hive, '2026-06-01T00:00:00.000Z')).toEqual({
      apiaryId: meadow,
      queenId: b
    });
  });

  it('falls back to the current apiary and queen before any recorded interval', async () => {
    const { meadow, b } = await twoEras();
    expect(await resolveContext(hive, '2020-05-01T10:00:00.000Z')).toEqual({
      apiaryId: meadow,
      queenId: b
    });
  });

  it('ignores deleted queens and placements', async () => {
    const { meadow, a, b } = await twoEras();
    await remove('queen', a, apiary);
    expect((await resolveContext(hive, '2026-03-15T11:00:00.000Z')).queenId).toBe(b);
    await remove('placement', 'p2', meadow);
    expect((await resolveContext(hive, '2026-07-15T10:00:00.000Z')).apiaryId).toBe(meadow);
  });

  it('writes back-dated records with the frozen context', async () => {
    const { a } = await twoEras();
    const id = await recordHarvest({
      hiveId: hive,
      date: '2026-03-15T11:00:00.000Z',
      amount_kg: 4
    });
    expect(await row('harvest', id)).toMatchObject({ apiary_id: apiary, queen_id: a });
    const [ev] = await events('ref_id = ?', [id]);
    expect(ev).toMatchObject({ apiary_id: apiary, queen_id: a, scope_id: apiary, amount_kg: 4 });
  });
});

describe('queens', () => {
  it('setQueen introduces the first queen with the marking colour of her year', async () => {
    const q = await setQueen(hive, apiary, { year: 2026, origin: 'own' });
    expect(await queens.current(hive)).toMatchObject({ id: q, year: 2026, marking: 1, active: 1 });
    const ev = await events('queen_id = ?', [q]);
    expect(ev.map((e) => e.type)).toEqual([EventType.QUEEN_INTRODUCED]);
    expect(ev[0].title).toBe('Queen 2026');
  });

  it('setQueen on a hive with a queen closes her reign and records the replacement', async () => {
    const a = await setQueen(hive, apiary, { year: 2024 });
    const b = await setQueen(hive, apiary, { year: 2026 });
    const old = await row('queen', a);
    const cur = await row('queen', b);
    expect(old.active).toBe(0);
    expect(old.replaced_at).toBe(cur.introduced_at);
    expect((await queens.current(hive)).id).toBe(b);
    expect(await events('type = ?', [EventType.QUEEN_REPLACED])).toHaveLength(1);
  });

  it('editQueen corrects details without inventing a re-queening', async () => {
    const q = await setQueen(hive, apiary, { year: 2025, marking: 5 });
    const before = await outboxSize();

    expect(await editQueen(q, { year: 2026, marking: 1, breeder_number: '12' })).toBe(true);

    expect(await queens.current(hive)).toMatchObject({
      id: q,
      year: 2026,
      marking: 1,
      breeder_number: '12',
      active: 1
    });
    expect(await queens.listByHive(hive)).toHaveLength(1);
    expect(await events('type = ?', [EventType.QUEEN_REPLACED])).toHaveLength(0);
    const introduced = await events('type = ?', [EventType.QUEEN_INTRODUCED]);
    expect(introduced).toHaveLength(1);
    expect(introduced[0].title).toBe('Queen 2026');
    // one change for the queen, one for the event title
    expect(await outboxSize()).toBe(before + 2);
  });

  it('editQueen writes nothing when nothing changed', async () => {
    const q = await setQueen(hive, apiary, { year: 2025, origin: 'own' });
    const before = await outboxSize();
    expect(await editQueen(q, { year: 2025, origin: 'own', note: '' })).toBe(false);
    expect(await outboxSize()).toBe(before);
  });

  it('editQueen moves the introduction date together with the previous reign', async () => {
    const a = await setQueen(hive, apiary, {
      year: 2024,
      introduced_at: '2024-05-01T10:00:00.000Z'
    });
    const b = await setQueen(hive, apiary, {
      year: 2026,
      introduced_at: '2026-05-10T10:00:00.000Z'
    });

    await editQueen(b, { introduced_at: '2026-05-03T10:00:00.000Z' });

    expect((await row('queen', b)).introduced_at).toBe('2026-05-03T10:00:00.000Z');
    expect((await row('queen', a)).replaced_at).toBe('2026-05-03T10:00:00.000Z');
    const replaced = await events('type = ?', [EventType.QUEEN_REPLACED]);
    expect(replaced.map((e) => e.date)).toEqual(['2026-05-03T10:00:00.000Z']);
    const intro = await events('type = ? AND queen_id = ?', [EventType.QUEEN_INTRODUCED, b]);
    expect(intro[0].date).toBe('2026-05-03T10:00:00.000Z');
    // A visit between the old and the new date now belongs to the new queen.
    expect((await resolveContext(hive, '2026-05-05T10:00:00.000Z')).queenId).toBe(b);
  });

  it('deleteQueen removes her events and reinstates the predecessor; undo restores all', async () => {
    const a = await setQueen(hive, apiary, { year: 2024 });
    const b = await setQueen(hive, apiary, { year: 2026 });
    const replacedAt = (await row('queen', a)).replaced_at;

    const undo = await deleteQueen(b);

    expect(await queens.current(hive)).toMatchObject({ id: a, active: 1, replaced_at: null });
    expect((await queens.listByHive(hive)).map((q) => q.id)).toEqual([a]);
    expect(await events('type = ?', [EventType.QUEEN_REPLACED])).toHaveLength(0);
    expect(await events('queen_id = ?', [b])).toHaveLength(0);

    await undo();

    expect((await queens.current(hive)).id).toBe(b);
    expect(await row('queen', a)).toMatchObject({ active: 0, replaced_at: replacedAt });
    expect(await events('type = ?', [EventType.QUEEN_REPLACED])).toHaveLength(1);
    expect(await events('queen_id = ?', [b])).toHaveLength(1);
  });

  it('deleteQueen of the only queen leaves the hive without one', async () => {
    const q = await setQueen(hive, apiary, { year: 2026 });
    await deleteQueen(q);
    expect(await queens.current(hive)).toBeUndefined();
    expect(await events('type = ?', [EventType.QUEEN_INTRODUCED])).toHaveLength(0);
  });

  it('deleteQueen of a former queen keeps the current one', async () => {
    const a = await setQueen(hive, apiary, { year: 2024 });
    const b = await setQueen(hive, apiary, { year: 2026 });
    await deleteQueen(a);
    expect((await queens.current(hive)).id).toBe(b);
    expect(await events('queen_id = ?', [a])).toHaveLength(0);
    expect(await events('queen_id = ?', [b])).toHaveLength(1);
  });
});

describe('inspections', () => {
  const day = '2026-05-14T10:00:00.000Z';

  it('recordInspection stores NULL for counts that were not recorded and 0 for observed zero', async () => {
    const id = await recordInspection(hive, {
      date: day,
      frames: null,
      queen_cells: 0,
      brood_frames: 6
    });
    const r = await row('inspection', id);
    expect(r.frames).toBeNull();
    expect(r.queen_cells).toBe(0);
    expect(r.brood_frames).toBe(6);
    expect(r.source).toBe(1);
    const payload = JSON.parse(
      (await getCurrentDB().get<any>(`SELECT payload FROM outbox WHERE entity_id = ?`, [id]))
        .payload
    );
    expect(payload.frames).toBeNull();
    expect(payload.queen_cells).toBe(0);
  });

  it('recordInspection writes one INSPECTION event referencing the visit', async () => {
    const id = await recordInspection(hive, { date: day });
    const ev = await events('ref_id = ?', [id]);
    expect(ev).toHaveLength(1);
    expect(ev[0]).toMatchObject({
      type: EventType.INSPECTION,
      ref_entity: 'inspection',
      hive_id: hive,
      date: day
    });
  });

  it('editInspection changes only the edited fields and moves the event with the date', async () => {
    const id = await recordInspection(hive, { date: day, frames: 8, note: 'calm' });
    const before = await outboxSize();
    const later = '2026-05-16T10:00:00.000Z';

    expect(await editInspection(id, { date: later, frames: 9, note: 'calm' })).toBe(true);

    expect(await row('inspection', id)).toMatchObject({ date: later, frames: 9, note: 'calm' });
    const ev = await events('ref_id = ?', [id]);
    expect(ev).toHaveLength(1);
    expect(ev[0].date).toBe(later);
    const sent = await getCurrentDB().all<any>(
      `SELECT entity, payload FROM outbox ORDER BY rowid DESC LIMIT ?`,
      [(await outboxSize()) - before]
    );
    const visit = sent.find((s) => s.entity === 'inspection')!;
    expect(Object.keys(JSON.parse(visit.payload)).sort()).toEqual(['date', 'frames']);
  });

  it('editInspection can clear a count back to not recorded', async () => {
    const id = await recordInspection(hive, { date: day, frames: 8 });
    await editInspection(id, { frames: null });
    expect((await row('inspection', id)).frames).toBeNull();
  });

  it('editInspection without a change writes nothing', async () => {
    const id = await recordInspection(hive, { date: day, frames: 8 });
    const before = await outboxSize();
    expect(await editInspection(id, { date: day, frames: 8 })).toBe(false);
    expect(await outboxSize()).toBe(before);
  });

  it('deleteInspection tombstones the visit and its event; undo brings both back', async () => {
    const id = await recordInspection(hive, { date: day });
    const undo = await deleteInspection(id);
    expect(await inspections.listByHive(hive)).toHaveLength(0);
    expect(await events('ref_id = ?', [id])).toHaveLength(0);
    await undo();
    expect(await inspections.listByHive(hive)).toHaveLength(1);
    expect(await events('ref_id = ?', [id])).toHaveLength(1);
  });

  it('listByHive leaves sensor readings out unless asked for them', async () => {
    const manual = await recordInspection(hive, { date: '2026-05-10T10:00:00.000Z' });
    const legacy = await inspections.add(
      { hive_id: hive, date: '2026-05-01T10:00:00.000Z' },
      apiary
    );
    await getCurrentDB().exec(`UPDATE inspection SET source = NULL WHERE id = ?`, [legacy]);
    const sensor = await recordInspection(hive, {
      date: '2026-05-20T10:00:00.000Z',
      source: 2,
      weight_kg: 41.5
    });

    // The newest row is a sensor reading, yet the last inspection is the manual one.
    const visits = await inspections.listByHive(hive);
    expect(visits.map((v) => v.id)).toEqual([manual, legacy]);
    expect(visits[0].date).toBe('2026-05-10T10:00:00.000Z');

    const all = await inspections.listByHive(hive, { sensors: true });
    expect(all.map((v) => v.id)).toEqual([sensor, manual, legacy]);
  });
});

describe('harvests', () => {
  const day = '2026-07-01T10:00:00.000Z';

  it('recordHarvest writes the record and a HARVEST fact row', async () => {
    const id = await recordHarvest({ hiveId: hive, date: day, amount_kg: 12.5, variety: 'Lime' });
    expect(await row('harvest', id)).toMatchObject({ amount_kg: 12.5, amended_at: null });
    const [ev] = await events('ref_id = ?', [id]);
    expect(ev).toMatchObject({ type: EventType.HARVEST, amount_kg: 12.5, title: '12.5 kg Lime' });
  });

  it('editHarvest marks the record as amended and keeps the fact row in step', async () => {
    const id = await recordHarvest({ hiveId: hive, date: day, amount_kg: 12.5, variety: 'Lime' });

    expect(await editHarvest(id, { amount_kg: 10, date: '2025-07-02T10:00:00.000Z' })).toBe(true);

    const r = await row('harvest', id);
    expect(r).toMatchObject({ amount_kg: 10, date: '2025-07-02T10:00:00.000Z' });
    expect(r.amended_at).toBeTruthy();
    const ev = await events('ref_id = ?', [id]);
    expect(ev).toHaveLength(1);
    expect(ev[0]).toMatchObject({
      amount_kg: 10,
      date: '2025-07-02T10:00:00.000Z',
      title: '10 kg Lime'
    });
    expect(await harvests.totalByHive(hive)).toBe(10);
    expect(await honeyByYear()).toEqual([{ year: '2025', kg: 10 }]);
  });

  it('editHarvest without a change neither writes nor marks the record as amended', async () => {
    const id = await recordHarvest({ hiveId: hive, date: day, amount_kg: 5, variety: '' });
    const before = await outboxSize();
    expect(await editHarvest(id, { amount_kg: 5, variety: '', date: day })).toBe(false);
    expect(await outboxSize()).toBe(before);
    expect((await row('harvest', id)).amended_at).toBeNull();
  });

  it('deleteHarvest takes the amount out of the totals; undo puts it back', async () => {
    await recordHarvest({ hiveId: hive, date: day, amount_kg: 8 });
    const id = await recordHarvest({ hiveId: hive, date: day, amount_kg: 5 });

    const undo = await deleteHarvest(id);
    expect(await harvests.totalByHive(hive)).toBe(8);
    expect(await honeyByYear()).toEqual([{ year: '2026', kg: 8 }]);
    expect(await events('ref_id = ?', [id])).toHaveLength(0);

    await undo();
    expect(await harvests.totalByHive(hive)).toBe(13);
    expect(await honeyByYear()).toEqual([{ year: '2026', kg: 13 }]);
  });
});

describe('treatments', () => {
  const day = '2026-08-01T10:00:00.000Z';

  it('editTreatment marks the record as amended and renames its timeline entry', async () => {
    const id = await recordTreatment({ hiveId: hive, date: day, product: 'Oxuvr', dose: '50 ml' });
    expect((await row('treatment', id)).amended_at).toBeNull();

    await editTreatment(id, { product: 'Oxuvar', dose: '50 ml' });

    const r = await row('treatment', id);
    expect(r.product).toBe('Oxuvar');
    expect(r.amended_at).toBeTruthy();
    const [ev] = await events('ref_id = ?', [id]);
    expect(ev).toMatchObject({ type: EventType.TREATMENT, title: 'Oxuvar', date: day });
  });

  it('deleteTreatment tombstones the record and its event; undo restores them', async () => {
    const id = await recordTreatment({ hiveId: hive, date: day, product: 'Oxuvar' });
    const undo = await deleteTreatment(id);
    expect(await treatments.listByHive(hive)).toHaveLength(0);
    expect(await events('type = ?', [EventType.TREATMENT])).toHaveLength(0);
    await undo();
    expect(await treatments.listByHive(hive)).toHaveLength(1);
    expect(await events('type = ?', [EventType.TREATMENT])).toHaveLength(1);
  });

  it('deleting a record that is already gone is a no-op', async () => {
    const before = await outboxSize();
    await (
      await deleteTreatment('missing')
    )();
    expect(await outboxSize()).toBe(before);
  });
});

describe('updateHive', () => {
  it('records a STATUS event when the status changes', async () => {
    await updateHive(hive, apiary, { name: 'Hive 1', type: 0, status: 3 });
    expect((await hives.get(hive)).status).toBe(3);
    const ev = await events('type = ?', [EventType.STATUS]);
    expect(ev).toHaveLength(1);
    expect(ev[0]).toMatchObject({ hive_id: hive, apiary_id: apiary });
    expect(JSON.parse(ev[0].detail)).toEqual({ from: 1, to: 3 });
  });

  it('records DISSOLVED when the colony is given up', async () => {
    await updateHive(hive, apiary, { status: 5 });
    expect(await events('type = ?', [EventType.DISSOLVED])).toHaveLength(1);
    expect(await events('type = ?', [EventType.STATUS])).toHaveLength(0);
  });

  it('writes no status event for a rename', async () => {
    await updateHive(hive, apiary, { name: 'Renamed', status: 1 });
    expect((await hives.get(hive)).name).toBe('Renamed');
    expect(await events('type IN (?, ?)', [EventType.STATUS, EventType.DISSOLVED])).toHaveLength(0);
  });
});

describe('honeyByYear', () => {
  it('sums harvest fact rows per year', async () => {
    await recordHarvest({ hiveId: hive, date: '2025-07-01T10:00:00.000Z', amount_kg: 7 });
    await recordHarvest({ hiveId: hive, date: '2026-06-01T10:00:00.000Z', amount_kg: 3 });
    await recordHarvest({ hiveId: hive, date: '2026-07-01T10:00:00.000Z', amount_kg: 4 });
    expect(await honeyByYear()).toEqual([
      { year: '2025', kg: 7 },
      { year: '2026', kg: 7 }
    ]);
  });

  it('leaves out the harvests of deleted hives', async () => {
    const other = await createHive({ name: 'Hive 2', apiary_id: apiary });
    await recordHarvest({ hiveId: hive, date: '2026-07-01T10:00:00.000Z', amount_kg: 4 });
    await recordHarvest({ hiveId: other, date: '2026-07-02T10:00:00.000Z', amount_kg: 9 });
    expect(await honeyByYear()).toEqual([{ year: '2026', kg: 13 }]);

    await hives.remove(other, apiary);
    expect(await honeyByYear()).toEqual([{ year: '2026', kg: 4 }]);
  });
});
