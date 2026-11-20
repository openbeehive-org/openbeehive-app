// Local database on the device. Web: SQLite-WASM on OPFS (persistent).
// Native (Capacitor): same interface, different implementation.
//
// The UI never talks to this class directly, only via lib/local/repo.ts.

export interface Statement {
  sql: string;
  params?: unknown[];
}

export interface LocalDB {
  exec(sql: string, params?: unknown[]): Promise<void>;
  all<T = Record<string, unknown>>(sql: string, params?: unknown[]): Promise<T[]>;
  get<T = Record<string, unknown>>(sql: string, params?: unknown[]): Promise<T | undefined>;
  // Runs the statements in one transaction: all of them are stored, or none.
  batch(stmts: Statement[]): Promise<void>;
}

// --- Web implementation: SQLite-WASM runs in a dedicated worker (db.worker.ts)
// because OPFS sync-access handles are only available off the main thread.
// This module proxies exec/all/get/batch to that worker over postMessage. ---
import DbWorker from './db.worker?worker';
import { migrateLocal } from './schema';
import { storageState } from './live';
import { claimTab, takeoverRequested } from './tablock';

// Worker reply: a query result, or an unsolicited volatile-store notice.
type Reply = { id?: number; ok?: boolean; rows?: unknown; error?: string; volatile?: boolean };
type Method = 'open' | 'exec' | 'all' | 'batch';

let dbPromise: Promise<LocalDB> | null = null;

export function getDB(): Promise<LocalDB> {
  if (!dbPromise) dbPromise = init();
  return dbPromise;
}

// Ask the browser not to evict this origin's storage under pressure: the
// local database holds records that may not have reached the server yet.
function requestPersistence() {
  try {
    void navigator.storage?.persist?.().catch(() => undefined);
  } catch {
    /* not available: storage stays best-effort */
  }
}

async function init(): Promise<LocalDB> {
  let worker: Worker | null = null;
  const pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>();

  // Another tab took the database over: drop the connection (which frees the
  // OPFS handles for that tab) and fail whatever was still in flight.
  const release = () => {
    worker?.terminate();
    worker = null;
    for (const p of pending.values()) p.reject(new Error('local database is open in another tab'));
    pending.clear();
    storageState.set('elsewhere');
  };

  const stolen = takeoverRequested();
  const claim = await claimTab(navigator.locks, stolen, release);
  if (claim === 'blocked') {
    // Never resolves: the notice covers the app until the user takes over
    // (which reloads this tab) or closes it.
    storageState.set('elsewhere');
    return new Promise<LocalDB>(() => {});
  }
  requestPersistence();

  const w = (worker = new DbWorker());
  let seq = 0;
  w.onmessage = (e: MessageEvent<Reply>) => {
    const { id, ok, rows, error, volatile } = e.data;
    if (volatile) {
      // The worker fell back to an in-memory database (persistent storage
      // unavailable). The app keeps working, but nothing survives the tab;
      // the layout shows that as a banner for as long as it is true.
      storageState.set('volatile');
      return;
    }
    const p = id === undefined ? undefined : pending.get(id);
    if (!p || id === undefined) return;
    pending.delete(id);
    if (ok) p.resolve(rows);
    else p.reject(new Error(error));
  };

  function call(method: Method, msg: Record<string, unknown> = {}): Promise<unknown> {
    return new Promise((resolve, reject) => {
      if (!worker) {
        reject(new Error('local database is open in another tab'));
        return;
      }
      const id = ++seq;
      pending.set(id, { resolve, reject });
      worker.postMessage({ id, method, ...msg });
    });
  }

  // Per-tenant local store: open a database file keyed to the active tenant so
  // switching tenants never mixes data on the same device. Falls back to the
  // single-user "local" store when no tenant is set.
  const orgId =
    (typeof localStorage !== 'undefined' && localStorage.getItem('obh.orgId')) || 'local';
  const dbFile = `openbeehive-${orgId.replace(/[^a-zA-Z0-9_-]/g, '_')}.sqlite3`;
  // After a takeover the previous tab's worker may still be letting go of
  // its file handles; give the open a little longer before giving up.
  await call('open', { name: dbFile, attempts: stolen ? 12 : 3 });

  const impl: LocalDB = {
    async exec(sql, params = []) {
      await call('exec', { sql, params });
    },
    async all<T>(sql: string, params: unknown[] = []) {
      return ((await call('all', { sql, params })) ?? []) as T[];
    },
    async get<T>(sql: string, params: unknown[] = []) {
      const rows = await impl.all<T>(sql, params);
      return rows[0];
    },
    async batch(stmts) {
      if (stmts.length > 0) await call('batch', { stmts });
    }
  };

  await migrateLocal(impl);
  return impl;
}
