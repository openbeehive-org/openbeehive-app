// The photo flows as the UI drives them, on a real local database (node
// SQLite), the IndexedDB store (fake-indexeddb) and a fake server behind
// fetch.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { get, writable } from 'svelte/store';
import { IDBFactory } from 'fake-indexeddb';
import { makeLocalDB, setCurrentDB, getCurrentDB } from '../../tests/localdb';
import type { PreparedPhoto } from '../image';

vi.mock('../local/db', async () => {
  const { getCurrentDB } = await import('../../tests/localdb');
  return { getDB: async () => getCurrentDB() };
});
// Local writes nudge a sync; none runs here.
vi.mock('../local/sync', () => ({
  syncOnce: vi.fn(async () => {}),
  syncStatus: writable({ state: 'idle', pending: 0, failed: 0, lastSyncedAt: null })
}));

import { apiaries, hives, inspections, patch, photoRecords } from '../local/repo';
import { migrateLocal } from '../local/schema';
import { IndexedDBFileStore } from './files';

type Photos = typeof import('./index');

// The server: knows which photo records have been synced to it.
const server = {
  known: new Set<string>(),
  stored: new Map<string, Blob>(),
  requests: [] as { method: string; path: string; auth: string | null }[]
};

function fakeFetch(url: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  if (!navigator.onLine) return Promise.reject(new TypeError('Failed to fetch'));
  const path = new URL(String(url)).pathname;
  const id = decodeURIComponent(path.replace('/photos/', ''));
  const method = init.method ?? 'GET';
  const headers = new Headers(init.headers);
  server.requests.push({ method, path, auth: headers.get('Authorization') });
  const answer = (status: number, code = '') =>
    Promise.resolve(new Response(JSON.stringify({ code }), { status }));
  if (method === 'POST') {
    if (!server.known.has(id)) return answer(404, 'unknown_photo');
    server.stored.set(id, init.body as Blob);
    return answer(201);
  }
  const blob = server.stored.get(id);
  return blob ? Promise.resolve(new Response(blob, { status: 200 })) : answer(404, 'not_uploaded');
}

function setOnline(online: boolean) {
  Object.defineProperty(globalThis.navigator, 'onLine', { value: online, configurable: true });
}

const picture = (name: string, bytes = 2000): PreparedPhoto => ({
  full: new Blob([name.padEnd(bytes, '.')], { type: 'image/jpeg' }),
  thumb: `data:image/jpeg;base64,${btoa(name)}`,
  contentType: 'image/jpeg'
});

const outbox = () =>
  getCurrentDB().all<{
    entity: string;
    entity_id: string;
    scope_id: string;
    op: number;
    payload: string;
  }>(`SELECT entity, entity_id, scope_id, op, payload FROM outbox ORDER BY hlc, id`);

// What a finished sync means for the server: it knows every record pushed.
async function syncRecords() {
  for (const o of await outbox()) if (o.entity === 'photo') server.known.add(o.entity_id);
}

let photos: Photos;
let apiary: string;

// "Opening the app": a fresh module instance over the device's stores.
async function openApp() {
  vi.resetModules();
  photos = await import('./index');
}

beforeEach(async () => {
  setCurrentDB(await makeLocalDB());
  localStorage.clear();
  localStorage.setItem('obh.orgId', 'org1');
  localStorage.setItem('obh.userId', 'user1');
  localStorage.setItem('session', 'session-token');
  vi.stubGlobal('indexedDB', new IDBFactory());
  vi.stubGlobal('fetch', fakeFetch);
  setOnline(true);
  server.known.clear();
  server.stored.clear();
  server.requests.length = 0;
  await openApp();

  apiary = await apiaries.create({ organization_id: 'org1', name: 'Orchard' });
  await patch('hive', 'h1', apiary, {
    organization_id: 'org1',
    apiary_id: apiary,
    name: 'Hive 1',
    deleted: 0
  });
  await patch('inspection', 'i1', apiary, {
    organization_id: 'org1',
    hive_id: 'h1',
    date: '2026-07-01T10:00:00.000Z',
    deleted: 0
  });
  await getCurrentDB().exec(`DELETE FROM outbox`);
});
afterEach(() => {
  vi.unstubAllGlobals();
  setOnline(true);
});

const deviceFiles = () => new IndexedDBFileStore('obh-photos-org1', indexedDB).list();

describe('adding a photo to a visit', () => {
  it('writes the record with its thumbnail and queues it for sync in the hive apiary scope', async () => {
    const id = await photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('one'));

    const rows = await photoRecords.of('inspection', 'i1');
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      id,
      owner_entity: 'inspection',
      owner_id: 'i1',
      thumb: picture('one').thumb,
      content_type: 'image/jpeg',
      size_bytes: 2000
    });
    const out = await outbox();
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ entity: 'photo', entity_id: id, scope_id: apiary, op: 1 });
    const payload = JSON.parse(out[0]!.payload);
    expect(payload).toMatchObject({ organization_id: 'org1', owner_id: 'i1', deleted: 0 });
    // The full image is not part of the record.
    expect(out[0]!.payload.length).toBeLessThan(500);

    // The visit list shows it.
    const visits = await inspections.listByHive('h1');
    expect(visits[0].photos.map((p: { id: string }) => p.id)).toEqual([id]);
  });

  it('works offline: the photo is there at once and marked as waiting', async () => {
    setOnline(false);
    const id = await photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('one'));

    expect(await photoRecords.of('inspection', 'i1')).toHaveLength(1);
    expect(get(photos.uploadStates).get(id)).toBe('waiting');
    expect(get(photos.waitingUploads)).toBe(1);
    expect(server.requests).toEqual([]);
    // The full image opens from the device.
    const opened = await photos.openPhoto(id);
    expect('blob' in opened && opened.blob.size).toBe(2000);
  });

  it('uploads when the device is back online and its record has synced', async () => {
    setOnline(false);
    const id = await photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('one'));

    setOnline(true);
    await photos.uploadPhotosNow(); // the record has not reached the server yet
    expect(server.stored.has(id)).toBe(false);
    expect(get(photos.uploadStates).get(id)).toBe('waiting');

    await syncRecords();
    await photos.uploadPhotosNow(); // still inside the pause after the first attempt
    expect(server.stored.has(id)).toBe(false);
    vi.useFakeTimers({ now: Date.now() + 6_000, toFake: ['Date'] });
    await photos.uploadPhotosNow();
    vi.useRealTimers();
    expect(server.stored.get(id)?.size).toBe(2000);
    expect(get(photos.uploadStates).size).toBe(0);
    expect(server.requests.at(-1)).toEqual({
      method: 'POST',
      path: `/photos/${id}`,
      auth: 'Bearer session-token'
    });
  });

  it('survives closing the app before the upload', async () => {
    setOnline(false);
    const id = await photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('one'));
    await syncRecords();

    await openApp(); // later, online
    setOnline(true);
    expect((await deviceFiles()).map((m) => [m.id, m.state])).toEqual([[id, 'pending']]);
    await photos.uploadPhotosNow();
    expect(server.stored.get(id)?.size).toBe(2000);
    expect((await deviceFiles()).map((m) => m.state)).toEqual(['cached']);
  });

  it('leaves no image behind when the record cannot be written', async () => {
    const db = getCurrentDB();
    const batch = db.batch.bind(db);
    db.batch = async () => {
      throw new Error('disk full');
    };
    await expect(
      photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('one'))
    ).rejects.toThrow('disk full');
    db.batch = batch;
    expect(await deviceFiles()).toEqual([]);
    expect(await photoRecords.of('inspection', 'i1')).toEqual([]);
  });

  it('lists the photos of a visit oldest first, each visit its own', async () => {
    await patch('inspection', 'i2', apiary, {
      organization_id: 'org1',
      hive_id: 'h1',
      date: '2026-07-02T10:00:00.000Z',
      deleted: 0
    });
    vi.useFakeTimers({ now: new Date('2026-07-01T10:00:00Z'), toFake: ['Date'] });
    const first = await photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('one'));
    vi.setSystemTime(new Date('2026-07-01T10:00:05Z'));
    const second = await photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('two'));
    const other = await photos.addPhoto({ entity: 'inspection', id: 'i2' }, picture('three'));
    vi.useRealTimers();

    const byVisit = Object.fromEntries(
      (await inspections.listByHive('h1')).map((v) => [
        v.id,
        v.photos.map((p: { id: string }) => p.id)
      ])
    );
    expect(byVisit).toEqual({ i1: [first, second], i2: [other] });
  });
});

describe('deleting a photo', () => {
  it('removes record, thumbnail and image from the device and queues the delete', async () => {
    const id = await photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('one'));
    const kept = await photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('two'));
    await getCurrentDB().exec(`DELETE FROM outbox`);

    await photos.deletePhoto(id);

    expect((await photoRecords.of('inspection', 'i1')).map((p) => p.id)).toEqual([kept]);
    const row = await getCurrentDB().get<{ deleted: number; thumb: string | null }>(
      `SELECT deleted, thumb FROM photo WHERE id = ?`,
      [id]
    );
    expect(row).toEqual({ deleted: 1, thumb: null });
    expect((await deviceFiles()).map((m) => m.id)).toEqual([kept]);
    expect(await outbox()).toMatchObject([
      { entity: 'photo', entity_id: id, op: 2, scope_id: apiary }
    ]);
    expect(get(photos.uploadStates).has(id)).toBe(false);
    // Deleting again, or something unknown, does nothing.
    await photos.deletePhoto(id);
    await photos.deletePhoto('nope');
    expect(await outbox()).toHaveLength(1);
  });

  it('forgets the picture of a photo that was deleted on another device', async () => {
    const id = await photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('one'));
    // What a pulled delete leaves: the row marked deleted, picture still in it.
    await getCurrentDB().exec(`UPDATE photo SET deleted = 1 WHERE id = ?`, [id]);
    await photoRecords.eraseDeleted();
    const row = await getCurrentDB().get<{ thumb: string | null }>(
      `SELECT thumb FROM photo WHERE id = ?`,
      [id]
    );
    expect(row?.thumb).toBeNull();
    expect(await photoRecords.isLive(id)).toBe(false);
  });
});

describe('the picture of a hive', () => {
  it('is the newest photo of the hive; setting one deletes the one before', async () => {
    vi.useFakeTimers({ now: new Date('2026-07-01T10:00:00Z'), toFake: ['Date'] });
    const first = await photos.setHivePicture('h1', picture('first'));
    vi.setSystemTime(new Date('2026-07-01T10:01:00Z'));
    await getCurrentDB().exec(`DELETE FROM outbox`);
    const second = await photos.setHivePicture('h1', picture('second'));
    vi.useRealTimers();

    expect((await photoRecords.hivePicture('h1'))?.id).toBe(second);
    expect(await photoRecords.isLive(first)).toBe(false);
    // One local transaction: the new record and the delete of the old one.
    const out = await outbox();
    expect(out.map((o) => [o.entity_id, o.op]).sort()).toEqual(
      [
        [first, 2],
        [second, 1]
      ].sort()
    );
    expect((await deviceFiles()).map((m) => m.id)).toEqual([second]);

    // Lists show the thumbnail of the current picture.
    const overview = await hives.overview();
    expect(overview[0]!.photo).toBe(picture('second').thumb);

    await photos.deletePhoto(second);
    expect(await photoRecords.hivePicture('h1')).toBeUndefined();
    expect((await hives.overview())[0]!.photo).toBeNull();
  });

  it('is not one of the visit photos, and visit photos are not the picture', async () => {
    await photos.addPhoto({ entity: 'inspection', id: 'i1' }, picture('visit'));
    expect(await photoRecords.hivePicture('h1')).toBeUndefined();
    await photos.setHivePicture('h1', picture('hive'));
    expect(await photoRecords.of('inspection', 'i1')).toHaveLength(1);
  });
});

describe('viewing', () => {
  it('fetches a photo taken elsewhere once, then shows it from the device', async () => {
    // A record that arrived by sync; its image is on the server.
    await getCurrentDB().exec(
      `INSERT INTO photo (id, owner_entity, owner_id, thumb, deleted) VALUES ('remote', 'inspection', 'i1', 'data:image/jpeg;base64,AA', 0)`
    );
    server.stored.set('remote', new Blob(['x'.repeat(5000)], { type: 'image/jpeg' }));

    const first = await photos.openPhoto('remote');
    expect('blob' in first && first.blob.size).toBe(5000);
    setOnline(false);
    const again = await photos.openPhoto('remote');
    expect('blob' in again && again.blob.size).toBe(5000);
    expect(server.requests.filter((r) => r.method === 'GET')).toHaveLength(1);
    expect(get(photos.uploadStates).size).toBe(0);
  });

  it('explains a full image that is not there yet', async () => {
    await getCurrentDB().exec(
      `INSERT INTO photo (id, owner_entity, owner_id, deleted) VALUES ('remote', 'inspection', 'i1', 0)`
    );
    expect(await photos.openPhoto('remote')).toEqual({ unavailable: 'not_uploaded' });
    setOnline(false);
    expect(await photos.openPhoto('remote')).toEqual({ unavailable: 'offline' });
  });
});

describe('inline photos of the old format', () => {
  it('are cleared from the local database when the app starts', async () => {
    const db = getCurrentDB();
    const inline = 'data:image/jpeg;base64,' + 'A'.repeat(5000);
    await db.exec(`UPDATE inspection SET photo_keys = ? WHERE id = 'i1'`, [
      JSON.stringify({ [inline]: { a: ['t1'], r: [] } })
    ]);
    await db.exec(`UPDATE hive SET photo = ? WHERE id = 'h1'`, [inline]);

    await migrateLocal(db);

    const visit = await db.get<{ photo_keys: string }>(
      `SELECT photo_keys FROM inspection WHERE id = 'i1'`
    );
    const hive = await db.get<{ photo: string | null; name: string }>(
      `SELECT photo, name FROM hive WHERE id = 'h1'`
    );
    expect(visit?.photo_keys).toBe('{}');
    expect(hive).toEqual({ photo: null, name: 'Hive 1' });
  });
});
