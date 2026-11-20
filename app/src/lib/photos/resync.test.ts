// Photos when a device starts from a snapshot, and when the server turns out
// to be restored from a backup: the real sync code and the real photo module
// on a local database (node SQLite), the IndexedDB store (fake-indexeddb),
// a fake sync service and a fake photo endpoint behind fetch.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';

// Looked up on every call: each test loads its modules afresh.
vi.mock('../local/db', () => ({
  getDB: async () => (await import('../../tests/localdb')).getCurrentDB(),
  localStoreKey: async () => localStorage.getItem('obh.orgId') ?? 'local'
}));
const pullMock = vi.fn();
const snapshotMock = vi.fn();
vi.mock('#lib/client.js', () => ({
  syncClient: {
    push: vi.fn(async () => ({ rejected: [] })),
    pull: (...a: unknown[]) => pullMock(...a),
    snapshot: (...a: unknown[]) => snapshotMock(...a)
  }
}));

import { IndexedDBFileStore } from './files';

// The photo endpoint: which images it has, and what it was asked.
const server = {
  stored: new Map<string, Blob>(),
  requests: [] as { method: string; id: string }[]
};

function fakeFetch(url: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const id = decodeURIComponent(new URL(String(url)).pathname.replace('/photos/', ''));
  const method = init.method ?? 'GET';
  server.requests.push({ method, id });
  if (method === 'POST') {
    server.stored.set(id, init.body as Blob);
    return Promise.resolve(new Response('{}', { status: 201 }));
  }
  const blob = server.stored.get(id);
  return Promise.resolve(
    blob
      ? new Response(blob, { status: 200 })
      : new Response(JSON.stringify({ code: 'not_uploaded' }), { status: 404 })
  );
}

const stamp = (n: number) => `${String(Date.now() - 60_000 + n).padStart(15, '0')}:00000:remote`;
const thumb = 'data:image/jpeg;base64,/9j/AAAA';
const RESET = 2; // PullStatus.RESET_REQUIRED

function snapRow(entity: string, entityId: string, fields: Record<string, unknown>, n: number) {
  const clock = Object.fromEntries(Object.keys(fields).map((k) => [k, stamp(n)]));
  return {
    entity,
    entityId,
    payloadJson: JSON.stringify(fields),
    fieldHlcJson: JSON.stringify(clock)
  };
}

// The workspace as the server's snapshot has it: a visit with one photo.
const workspace = {
  rows: [
    snapRow('apiary', 'a1', { organization_id: 'org1', name: 'Orchard', deleted: 0 }, 1),
    snapRow('hive', 'h1', { organization_id: 'org1', apiary_id: 'a1', name: 'H1', deleted: 0 }, 2),
    snapRow('inspection', 'i1', { organization_id: 'org1', hive_id: 'h1', deleted: 0 }, 3),
    snapRow(
      'photo',
      'p1',
      {
        organization_id: 'org1',
        owner_entity: 'inspection',
        owner_id: 'i1',
        thumb,
        content_type: 'image/jpeg',
        size_bytes: 5000,
        deleted: 0
      },
      4
    )
  ],
  nextPageToken: '',
  cursor: 'e1:4:b1'
};

const deviceFiles = () => new IndexedDBFileStore('obh-photos-org1', indexedDB).list();

let photos: typeof import('./index');
let getCurrentDB: typeof import('../../tests/localdb').getCurrentDB;
let pull: typeof import('../local/sync').pull;
let photoRecords: typeof import('../local/repo').photoRecords;
let storageState: typeof import('../local/live').storageState;

beforeEach(async () => {
  // A fresh app per test: the photo module keeps its store per page load.
  vi.resetModules();
  const localdb = await import('../../tests/localdb');
  localdb.setCurrentDB(await localdb.makeLocalDB());
  getCurrentDB = localdb.getCurrentDB;
  localStorage.clear();
  localStorage.setItem('obh.orgId', 'org1');
  localStorage.setItem('obh.userId', 'user1');
  localStorage.setItem('session', 'session-token');
  vi.stubGlobal('indexedDB', new IDBFactory());
  vi.stubGlobal('fetch', fakeFetch);
  Object.defineProperty(globalThis.navigator, 'onLine', { value: true, configurable: true });
  server.stored.clear();
  server.requests.length = 0;
  pullMock.mockReset();
  snapshotMock.mockReset();
  photos = await import('./index');
  ({ pull } = await import('../local/sync'));
  ({ photoRecords } = await import('../local/repo'));
  ({ storageState } = await import('../local/live'));
  // Writes nudge a background sync; the tests drive pull themselves.
  storageState.set('elsewhere');
});
afterEach(() => {
  vi.unstubAllGlobals();
  storageState.set('ok');
});

describe('a device that starts from a snapshot', () => {
  it('shows the thumbnails at once and fetches a full image when it is opened', async () => {
    server.stored.set('p1', new Blob(['x'.repeat(5000)], { type: 'image/jpeg' }));
    snapshotMock.mockResolvedValueOnce(workspace);
    pullMock.mockResolvedValue({ changes: [], nextCursor: 'e1:4:b1', hasMore: false });

    await pull();

    const shown = await photoRecords.of('inspection', 'i1');
    expect(shown.map((p) => [p.id, p.thumb])).toEqual([['p1', thumb]]);
    expect(server.requests).toEqual([]); // nothing downloaded up front

    const opened = await photos.openPhoto('p1');
    expect('blob' in opened && opened.blob.size).toBe(5000);
    expect(server.requests).toEqual([{ method: 'GET', id: 'p1' }]);
    expect(await deviceFiles()).toMatchObject([{ id: 'p1', state: 'cached' }]);
  });
});

describe('a server restored from a backup', () => {
  it('gets the records and the full images of this device again', async () => {
    // The device knows the photo and keeps its image as an uploaded copy.
    server.stored.set('p1', new Blob(['x'.repeat(5000)], { type: 'image/jpeg' }));
    snapshotMock.mockResolvedValueOnce(workspace);
    pullMock.mockResolvedValueOnce({ changes: [], nextCursor: 'e1:4:b1', hasMore: false });
    await pull();
    await photos.openPhoto('p1');

    // The server now runs on a backup without the photo or its image.
    server.stored.clear();
    server.requests.length = 0;
    pullMock.mockResolvedValueOnce({ status: RESET, changes: [], nextCursor: '', hasMore: false });
    snapshotMock.mockResolvedValueOnce({ rows: [], nextPageToken: '', cursor: 'e2:0:b9' });
    pullMock.mockResolvedValueOnce({ changes: [], nextCursor: 'e2:0:b9', hasMore: false });
    await pull();

    // The record goes up again with the other rows ...
    const queued = await getCurrentDB().all<{ entity: string; entity_id: string }>(
      `SELECT entity, entity_id FROM outbox WHERE entity = 'photo'`
    );
    expect(queued).toEqual([{ entity: 'photo', entity_id: 'p1' }]);
    // ... and the image after it.
    expect(await deviceFiles()).toMatchObject([{ id: 'p1', state: 'pending' }]);
    await photos.uploadPhotosNow();
    expect(server.requests).toEqual([{ method: 'POST', id: 'p1' }]);
    expect(server.stored.get('p1')?.size).toBe(5000);
    expect(await deviceFiles()).toMatchObject([{ id: 'p1', state: 'cached' }]);
  });
});
