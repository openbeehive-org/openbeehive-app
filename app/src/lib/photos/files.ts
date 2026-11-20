// Where full-size photos live on the device, outside the SQLite database.
//
// Every entry is an image plus a small record about it. The record says
// whether the image still has to reach the server:
//
//   pending  taken on this device and not uploaded yet. It exists nowhere
//            else, so nothing here ever removes it to save space.
//   cached   the server has it too (uploaded from here, or fetched for
//            viewing). A copy for speed and offline use; the oldest ones are
//            dropped when the cache grows past its budget.
//
// The store is IndexedDB: image and record are written in one transaction,
// which the browser applies completely or not at all, also when the tab is
// killed while it runs. One database per workspace, like the SQLite file.

export type FileState = 'pending' | 'cached';

export interface FileMeta {
  /** The photo's id. */
  id: string;
  state: FileState;
  /** Bytes of the image. */
  size: number;
  /** Last time the image was stored or shown (unix ms): the eviction order. */
  usedAt: number;
  /** Uploads tried so far. */
  attempts: number;
  /** Earliest time of the next upload attempt (unix ms). */
  nextTry: number;
  /** Why the last attempt failed, '' if none has. */
  error: string;
}

export interface FileStore {
  /** Stores image and record together: both are there afterwards, or neither. */
  put(meta: FileMeta, data: Blob): Promise<void>;
  get(id: string): Promise<Blob | undefined>;
  list(): Promise<FileMeta[]>;
  /** Changes fields of a record; no-op when the entry is gone. */
  update(id: string, patch: Partial<Omit<FileMeta, 'id'>>): Promise<void>;
  delete(id: string): Promise<void>;
}

/** In memory: for tests, and for browsers that offer no IndexedDB. */
export class MemoryFileStore implements FileStore {
  private metas = new Map<string, FileMeta>();
  private blobs = new Map<string, Blob>();

  async put(meta: FileMeta, data: Blob) {
    this.metas.set(meta.id, { ...meta });
    this.blobs.set(meta.id, data);
  }
  async get(id: string) {
    return this.blobs.get(id);
  }
  async list() {
    return [...this.metas.values()].map((m) => ({ ...m }));
  }
  async update(id: string, patch: Partial<Omit<FileMeta, 'id'>>) {
    const cur = this.metas.get(id);
    if (cur) this.metas.set(id, { ...cur, ...patch });
  }
  async delete(id: string) {
    this.metas.delete(id);
    this.blobs.delete(id);
  }
}

const META = 'meta';
const DATA = 'data';

const done = (tx: IDBTransaction) =>
  new Promise<void>((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error ?? new Error('photo store: transaction failed'));
    tx.onabort = () => reject(tx.error ?? new Error('photo store: transaction aborted'));
  });

const result = <T>(req: IDBRequest<T>) =>
  new Promise<T>((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error ?? new Error('photo store: request failed'));
  });

export class IndexedDBFileStore implements FileStore {
  private db: Promise<IDBDatabase> | null = null;

  constructor(
    private readonly name: string,
    private readonly factory: IDBFactory = indexedDB
  ) {}

  private open(): Promise<IDBDatabase> {
    if (!this.db) {
      this.db = new Promise<IDBDatabase>((resolve, reject) => {
        const req = this.factory.open(this.name, 1);
        req.onupgradeneeded = () => {
          req.result.createObjectStore(META, { keyPath: 'id' });
          req.result.createObjectStore(DATA);
        };
        req.onsuccess = () => resolve(req.result);
        req.onerror = () => reject(req.error ?? new Error('photo store: cannot open'));
        req.onblocked = () => reject(new Error('photo store: blocked by another tab'));
      });
      // A failed open is tried again by the next call, not remembered.
      this.db.catch(() => (this.db = null));
    }
    return this.db;
  }

  // "strict" asks the browser to have the data on disk before the
  // transaction reports success: a photo that was just taken must survive
  // whatever happens to the tab next.
  private async write(stores: string[]) {
    return (await this.open()).transaction(stores, 'readwrite', { durability: 'strict' });
  }

  async put(meta: FileMeta, data: Blob) {
    const tx = await this.write([META, DATA]);
    try {
      tx.objectStore(DATA).put(data, meta.id);
      tx.objectStore(META).put(meta);
    } catch (e) {
      // A request that could not even be made does not stop the transaction
      // by itself; without this the other half would be stored alone.
      tx.abort();
      throw e;
    }
    await done(tx);
  }

  async get(id: string) {
    const tx = (await this.open()).transaction(DATA, 'readonly');
    return (await result(tx.objectStore(DATA).get(id))) as Blob | undefined;
  }

  async list() {
    const tx = (await this.open()).transaction(META, 'readonly');
    return (await result(tx.objectStore(META).getAll())) as FileMeta[];
  }

  async update(id: string, patch: Partial<Omit<FileMeta, 'id'>>) {
    const tx = await this.write([META]);
    const store = tx.objectStore(META);
    const cur = (await result(store.get(id))) as FileMeta | undefined;
    if (cur) store.put({ ...cur, ...patch, id });
    await done(tx);
  }

  async delete(id: string) {
    const tx = await this.write([META, DATA]);
    tx.objectStore(META).delete(id);
    tx.objectStore(DATA).delete(id);
    await done(tx);
  }
}

/** The store of one workspace on this device. */
export function openFileStore(orgId: string): FileStore {
  if (typeof indexedDB === 'undefined') return new MemoryFileStore();
  return new IndexedDBFileStore(`obh-photos-${orgId.replace(/[^a-zA-Z0-9_-]/g, '_')}`);
}
