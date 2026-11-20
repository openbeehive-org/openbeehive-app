// 0.4.x apps still sync photos inline (data URLs in inspection.photo_keys
// and hive.photo); the server turns them into photo records. What arrives in
// those columns is not stored by this app.
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
    push: vi.fn(async () => ({ rejected: [] })),
    pull: (...a: unknown[]) => pullMock(...a),
    snapshot: (...a: unknown[]) => snapshotMock(...a)
  }
}));

import { pull } from './sync';
import { photoRecords } from './repo';
import { parseORSet, orValues } from './merge';
import { isInlinePhoto, withoutInlinePhotos } from './tables';

const stamp = (n: number) => `${String(Date.now() - 60_000 + n).padStart(15, '0')}:00000:remote`;
const inline = 'data:image/jpeg;base64,' + 'A'.repeat(4000);

function remote(n: number, entity: string, entityId: string, payload: unknown) {
  return {
    entity,
    entityId,
    scopeId: 'a1',
    op: 1,
    payloadJson: JSON.stringify(payload),
    hlc: stamp(n),
    authorId: 'u2'
  };
}

beforeEach(async () => {
  setCurrentDB(await makeLocalDB());
  localStorage.clear();
  localStorage.setItem('obh.orgId', 'org1');
  pullMock.mockReset();
  pullMock.mockResolvedValue({ changes: [], nextCursor: '', hasMore: false });
  snapshotMock.mockReset();
  snapshotMock.mockResolvedValue({ rows: [], cursor: '', nextPageToken: '' });
});

describe('pulled inline photos', () => {
  it('are left out, while the rest of the change and the photo record are stored', async () => {
    pullMock.mockResolvedValueOnce({
      changes: [
        remote(1, 'hive', 'h1', { name: 'Hive 1', apiary_id: 'a1', photo: inline, deleted: 0 }),
        remote(2, 'inspection', 'i1', {
          hive_id: 'h1',
          note: 'strong colony',
          photo_keys: { add: [inline, 'plain-key'] },
          deleted: 0
        }),
        // The record the server made from the inline photo.
        remote(3, 'photo', 'p1', {
          owner_entity: 'inspection',
          owner_id: 'i1',
          thumb: 'data:image/jpeg;base64,AAAA',
          content_type: 'image/jpeg',
          size_bytes: 3000,
          created_at: '2026-07-01T10:00:00.000Z',
          deleted: 0
        }),
        remote(4, 'inspection', 'i1', {
          photo_keys: { remove: [inline], removed_tags: { [inline]: [stamp(2)] } }
        })
      ],
      nextCursor: '4',
      hasMore: false
    });
    await pull();

    const db = getCurrentDB();
    const hive = await db.get<{ name: string; photo: string | null }>(
      `SELECT name, photo FROM hive WHERE id = 'h1'`
    );
    expect(hive).toEqual({ name: 'Hive 1', photo: null });
    const visit = await db.get<{ note: string; photo_keys: string }>(
      `SELECT note, photo_keys FROM inspection WHERE id = 'i1'`
    );
    expect(visit?.note).toBe('strong colony');
    expect(visit?.photo_keys).not.toContain('data:image');
    expect(orValues(parseORSet(visit?.photo_keys))).toEqual(['plain-key']);

    expect(await photoRecords.of('inspection', 'i1')).toMatchObject([
      { id: 'p1', size_bytes: 3000 }
    ]);
  });

  it('a cleared hive picture still clears', async () => {
    await getCurrentDB().exec(
      `INSERT INTO hive (id, name, photo, field_hlc) VALUES ('h1', 'H', 'x', '{}')`
    );
    pullMock.mockResolvedValueOnce({
      changes: [remote(1, 'hive', 'h1', { photo: '' })],
      nextCursor: '1',
      hasMore: false
    });
    await pull();
    const hive = await getCurrentDB().get<{ photo: string | null }>(
      `SELECT photo FROM hive WHERE id = 'h1'`
    );
    expect(hive?.photo).toBe('');
  });
});

describe('inline photos in a snapshot', () => {
  it('are left out as well', async () => {
    snapshotMock.mockResolvedValueOnce({
      rows: [
        {
          entity: 'hive',
          entityId: 'h1',
          payloadJson: JSON.stringify({
            name: 'Hive 1',
            apiary_id: 'a1',
            photo: inline,
            deleted: 0
          }),
          fieldHlcJson: JSON.stringify({ name: stamp(1), photo: stamp(1), deleted: stamp(1) })
        },
        {
          entity: 'inspection',
          entityId: 'i1',
          payloadJson: JSON.stringify({
            hive_id: 'h1',
            note: 'strong colony',
            deleted: 0,
            photo_keys: {
              [inline]: { a: [stamp(2)], r: [] },
              'plain-key': { a: [stamp(2)], r: [] }
            }
          }),
          fieldHlcJson: JSON.stringify({ hive_id: stamp(2), note: stamp(2), deleted: stamp(2) })
        }
      ],
      cursor: 'e:2:t',
      nextPageToken: ''
    });
    await pull();

    const db = getCurrentDB();
    const hive = await db.get<{ name: string; photo: string | null }>(
      `SELECT name, photo FROM hive WHERE id = 'h1'`
    );
    expect(hive).toEqual({ name: 'Hive 1', photo: null });
    const visit = await db.get<{ note: string; photo_keys: string }>(
      `SELECT note, photo_keys FROM inspection WHERE id = 'i1'`
    );
    expect(visit?.note).toBe('strong colony');
    expect(visit?.photo_keys).not.toContain('data:image');
    expect(orValues(parseORSet(visit?.photo_keys))).toEqual(['plain-key']);
  });
});

describe('the inline filters', () => {
  it('only concern the two columns that carried photos', () => {
    expect(isInlinePhoto('hive', 'photo', inline)).toBe(true);
    expect(isInlinePhoto('hive', 'photo', '')).toBe(false);
    expect(isInlinePhoto('hive', 'note', inline)).toBe(false);
    expect(isInlinePhoto('photo', 'thumb', inline)).toBe(false);

    const delta = {
      add: [inline, 'k'],
      remove: [inline],
      removed_tags: { [inline]: ['t'], k: ['t2'] }
    };
    expect(withoutInlinePhotos('inspection', 'photo_keys', delta)).toEqual({
      add: ['k'],
      remove: [],
      removed_tags: { k: ['t2'] }
    });
    expect(withoutInlinePhotos('inspection', 'other', delta)).toBe(delta);
    expect(withoutInlinePhotos('inspection', 'photo_keys', { add: ['k'] })).toEqual({
      add: ['k'],
      remove: undefined
    });
  });
});
