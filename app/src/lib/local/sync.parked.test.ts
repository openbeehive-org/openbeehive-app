// A change from the server that this device cannot merge is kept in
// inbox_failed and listed with the sync problems, instead of stopping every
// later pull at the same place. The merge is made to fail on purpose here:
// for a note "BOOM".
import { describe, it, expect, beforeEach, vi } from 'vitest';
import { makeLocalDB, setCurrentDB, getCurrentDB } from '../../tests/localdb';

vi.mock('./db', async () => {
  const { getCurrentDB } = await import('../../tests/localdb');
  return { getDB: async () => getCurrentDB() };
});

const pullMock = vi.fn();
const snapshotMock = vi.fn();
vi.mock('#lib/client.js', () => ({
  syncClient: {
    push: vi.fn(),
    pull: (...a: unknown[]) => pullMock(...a),
    snapshot: (...a: unknown[]) => snapshotMock(...a)
  }
}));

vi.mock('./merge', async (importOriginal) => {
  const m = await importOriginal<typeof import('./merge')>();
  const boom = (fields: Record<string, unknown>) => {
    if (fields.note === 'BOOM') throw new TypeError('cannot merge');
  };
  return {
    ...m,
    mergeFields: (...a: Parameters<typeof m.mergeFields>) => (boom(a[1]), m.mergeFields(...a)),
    mergeSnapshot: (...a: Parameters<typeof m.mergeSnapshot>) => (boom(a[1]), m.mergeSnapshot(...a))
  };
});

import { pull, listFailed, discardFailed, unsentCount } from './sync';

const t = (ms: number) => `${String(ms).padStart(15, '0')}:00000:remote`;
const change = (id: string, note: string, ms: number) => ({
  entity: 'apiary',
  entityId: id,
  op: 1,
  hlc: t(ms),
  payloadJson: JSON.stringify({ name: id, note, deleted: 0 })
});
const meta = async (k: string) =>
  (await getCurrentDB().get<{ v: string }>(`SELECT v FROM sync_meta WHERE k = ?`, [k]))?.v;

beforeEach(async () => {
  setCurrentDB(await makeLocalDB());
  localStorage.clear();
  localStorage.setItem('obh.orgId', 'org1');
  localStorage.setItem('obh.userId', 'u1');
  pullMock.mockReset();
  snapshotMock.mockReset();
  snapshotMock.mockResolvedValue({ rows: [], nextPageToken: '', cursor: '' });
  await getCurrentDB().exec(`INSERT INTO sync_meta (k, v) VALUES ('cursor', 'c0')`);
});

describe('received changes that cannot be applied', () => {
  it('are parked, listed and discarded; the pull goes on', async () => {
    const bad = change('a1', 'BOOM', 5);
    pullMock.mockResolvedValueOnce({
      changes: [bad, change('a2', 'fine', 6)],
      nextCursor: 'c1',
      hasMore: false
    });
    await pull();

    expect(await meta('cursor')).toBe('c1');
    const a2 = await getCurrentDB().get<{ note: string }>(
      `SELECT note FROM apiary WHERE id = 'a2'`
    );
    expect(a2?.note).toBe('fine');
    const parked = await getCurrentDB().all<any>(`SELECT * FROM inbox_failed`);
    expect(parked).toHaveLength(1);
    expect(parked[0]).toMatchObject({ entity: 'apiary', entity_id: 'a1', hlc: bad.hlc, op: 1 });
    expect(parked[0].payload).toBe(bad.payloadJson);
    expect(parked[0].error).toContain('cannot merge');

    // The same change received again does not add a second entry.
    pullMock.mockResolvedValueOnce({ changes: [bad], nextCursor: 'c2', hasMore: false });
    await pull();
    const list = await listFailed();
    expect(list).toHaveLength(1);
    expect(list[0]).toMatchObject({
      kind: 'unapplied',
      entity: 'apiary',
      entityId: 'a1',
      code: 'unapplied'
    });
    // It is not a local change waiting to be uploaded.
    expect(await unsentCount()).toBe(0);

    await discardFailed(list[0]!.id);
    expect(await listFailed()).toHaveLength(0);
  });

  it('a snapshot row is parked the same way', async () => {
    await getCurrentDB().exec(`DELETE FROM sync_meta WHERE k = 'cursor'`);
    snapshotMock.mockResolvedValueOnce({
      rows: [
        {
          entity: 'apiary',
          entityId: 'a1',
          payloadJson: JSON.stringify({ name: 'x', note: 'BOOM' }),
          fieldHlcJson: JSON.stringify({ name: t(1), note: t(1) })
        }
      ],
      nextPageToken: '',
      cursor: '9:1:aa'
    });
    pullMock.mockResolvedValueOnce({ changes: [], nextCursor: '9:1:aa', hasMore: false });
    await pull();
    expect(await meta('cursor')).toBe('9:1:aa');
    const list = await listFailed();
    expect(list).toHaveLength(1);
    expect(list[0]).toMatchObject({ kind: 'unapplied', entityId: 'a1' });
  });
});
