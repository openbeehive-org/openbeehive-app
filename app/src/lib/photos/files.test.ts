import { describe, it, expect } from 'vitest';
import { IDBFactory } from 'fake-indexeddb';
import {
  IndexedDBFileStore,
  MemoryFileStore,
  openFileStore,
  removeFileStore,
  type FileMeta,
  type FileStore
} from './files';

const meta = (id: string, extra: Partial<FileMeta> = {}): FileMeta => ({
  id,
  state: 'pending',
  size: 3,
  usedAt: 1,
  attempts: 0,
  nextTry: 0,
  error: '',
  ...extra
});
const blob = (text: string) => new Blob([text], { type: 'image/jpeg' });

const stores: [string, () => FileStore][] = [
  ['memory', () => new MemoryFileStore()],
  ['IndexedDB', () => new IndexedDBFileStore('photos-test', new IDBFactory())]
];

describe.each(stores)('%s file store', (_name, make) => {
  it('stores an image with its record and reads both back', async () => {
    const s = make();
    await s.put(meta('p1'), blob('abc'));
    await s.put(meta('p2', { state: 'cached' }), blob('defgh'));

    expect(await (await s.get('p1'))!.text()).toBe('abc');
    expect((await s.get('p1'))!.type).toBe('image/jpeg');
    expect((await s.list()).sort((a, b) => a.id.localeCompare(b.id))).toEqual([
      meta('p1'),
      meta('p2', { state: 'cached' })
    ]);
    expect(await s.get('nope')).toBeUndefined();
  });

  it('updates fields of a record and leaves the image alone', async () => {
    const s = make();
    await s.put(meta('p1'), blob('abc'));
    await s.update('p1', { state: 'cached', attempts: 2, error: 'retry' });
    expect(await s.list()).toEqual([meta('p1', { state: 'cached', attempts: 2, error: 'retry' })]);
    expect(await (await s.get('p1'))!.text()).toBe('abc');
    // Updating what is not there creates nothing.
    await s.update('ghost', { state: 'cached' });
    expect(await s.list()).toHaveLength(1);
  });

  it('deletes image and record together', async () => {
    const s = make();
    await s.put(meta('p1'), blob('abc'));
    await s.delete('p1');
    await s.delete('p1'); // again: not an error
    expect(await s.get('p1')).toBeUndefined();
    expect(await s.list()).toEqual([]);
  });

  it('replaces an entry stored under the same id', async () => {
    const s = make();
    await s.put(meta('p1'), blob('abc'));
    await s.put(meta('p1', { state: 'cached', size: 5 }), blob('defgh'));
    expect(await (await s.get('p1'))!.text()).toBe('defgh');
    expect(await s.list()).toEqual([meta('p1', { state: 'cached', size: 5 })]);
  });
});

describe('IndexedDB file store', () => {
  it('keeps what was stored when the app is opened again', async () => {
    const browser = new IDBFactory();
    await new IndexedDBFileStore('obh-photos-org1', browser).put(meta('p1'), blob('abc'));

    const reopened = new IndexedDBFileStore('obh-photos-org1', browser);
    expect(await reopened.list()).toEqual([meta('p1')]);
    expect(await (await reopened.get('p1'))!.text()).toBe('abc');
  });

  it('keeps the workspaces of one device apart', async () => {
    const browser = new IDBFactory();
    await new IndexedDBFileStore('obh-photos-org1', browser).put(meta('p1'), blob('abc'));
    const other = new IndexedDBFileStore('obh-photos-org2', browser);
    expect(await other.list()).toEqual([]);
    expect(await other.get('p1')).toBeUndefined();
  });

  // Image and record go in one transaction: when it fails, neither is there.
  it('stores nothing when the write cannot complete', async () => {
    const s = new IndexedDBFileStore('photos-test', new IDBFactory());
    await s.put(meta('p1'), blob('abc'));
    const unstorable = { ...meta('p2'), error: (() => 'no') as unknown as string };
    await expect(s.put(unstorable, blob('zzz'))).rejects.toThrow();
    expect(await s.get('p2')).toBeUndefined();
    expect(await s.list()).toEqual([meta('p1')]);
    // The store keeps working afterwards.
    await s.put(meta('p3'), blob('ok'));
    expect((await s.list()).map((m) => m.id).sort()).toEqual(['p1', 'p3']);
  });

  it('tries to open again after a failure', async () => {
    let fail = true;
    const real = new IDBFactory();
    const flaky = {
      open: (name: string, version?: number) => {
        if (fail) throw new DOMException('denied', 'SecurityError');
        return real.open(name, version);
      }
    } as unknown as IDBFactory;
    const s = new IndexedDBFileStore('photos-test', flaky);
    await expect(s.list()).rejects.toThrow('denied');
    fail = false;
    expect(await s.list()).toEqual([]);
  });
});

describe('removing a workspace from the device', () => {
  it('deletes the open store with every image in it, pending ones too', async () => {
    const browser = new IDBFactory();
    const mine = new IndexedDBFileStore('obh-photos-org1', browser);
    const other = new IndexedDBFileStore('obh-photos-org2', browser);
    await mine.put(meta('p1'), blob('abc'));
    await mine.put(meta('p2', { state: 'cached' }), blob('def'));
    await other.put(meta('p9'), blob('xyz'));

    await removeFileStore('org1', mine);

    expect((await browser.databases()).map((d) => d.name)).toEqual(['obh-photos-org2']);
    expect(await new IndexedDBFileStore('obh-photos-org1', browser).list()).toEqual([]);
    expect(await other.list()).toEqual([meta('p9')]);
    // The store object keeps working: it starts empty.
    await mine.put(meta('p3'), blob('new'));
    expect((await mine.list()).map((m) => m.id)).toEqual(['p3']);
  });

  it('does nothing where there is no IndexedDB, or for a memory store', async () => {
    expect(typeof indexedDB).toBe('undefined'); // plain node
    await expect(removeFileStore('org1')).resolves.toBeUndefined();
    const memory = new MemoryFileStore();
    await memory.put(meta('p1'), blob('abc'));
    await expect(removeFileStore('org1', memory)).resolves.toBeUndefined();
    // A memory store is gone with the page anyway; it is left as it is.
    expect((await memory.list()).map((m) => m.id)).toEqual(['p1']);
  });
});

describe('openFileStore', () => {
  it('falls back to memory where there is no IndexedDB', async () => {
    expect(typeof indexedDB).toBe('undefined'); // plain node
    const s = openFileStore('org/1');
    expect(s).toBeInstanceOf(MemoryFileStore);
    await s.put(meta('p1'), blob('abc'));
    expect(await s.list()).toHaveLength(1);
  });
});
