// Dedicated SQLite-WASM worker. OPFS sync-access handles are only available in
// a Worker context, so the database lives here and the main thread proxies
// queries via postMessage (see db.ts).
//
// The database file is per-tenant: the main thread sends an `open` message with
// the file name (derived from the active tenant) before any query, so each
// tenant's data lives in its own local store and switching tenants never mixes
// them on the same device.
import sqlite3InitModule from '@sqlite.org/sqlite-wasm';

type Req = {
  id: number;
  method: 'open' | 'exec' | 'all';
  sql?: string;
  params?: unknown[];
  name?: string;
};

let dbName = 'openbeehive.sqlite3';
let dbPromise: Promise<any> | null = null;
let pool: any = null;

// The pool's slot count caps how many files SQLite can hold open — per-tenant
// db files PLUS their journals/temp files. Too few slots fail writes with
// SQLITE_CANTOPEN when the journal can't get a slot. Slots are cheap (empty
// OPFS files), so reserve plenty.
const MIN_POOL_CAPACITY = 24;

async function getDb() {
  if (!dbPromise) {
    dbPromise = (async () => {
      const sqlite3 = await sqlite3InitModule();
      // Persistent: OPFS SAHPool VFS (no SharedArrayBuffer / cross-origin
      // isolation required — works behind any reverse proxy). The pool takes
      // EXCLUSIVE access handles, so a page refresh can race the previous
      // worker still holding them — retry briefly before falling back.
      let lastErr: unknown;
      for (let attempt = 0; attempt < 3; attempt++) {
        if (attempt > 0) await new Promise((r) => setTimeout(r, 300));
        try {
          pool = await sqlite3.installOpfsSAHPoolVfs({
            name: 'openbeehive',
            initialCapacity: MIN_POOL_CAPACITY
          });
          // initialCapacity only applies when the pool is first created; a pool
          // that already exists on the device keeps its old (possibly smaller)
          // capacity. Grow it explicitly so upgraded installs get the slots too.
          await pool.reserveMinimumCapacity(MIN_POOL_CAPACITY);
          return new pool.OpfsSAHPoolDb('/' + dbName);
        } catch (err) {
          lastErr = err;
        }
      }
      // OPFS stays unavailable in some environments — notably Firefox Private
      // Browsing and browsers/settings that block storage — where
      // navigator.storage.getDirectory() throws a SecurityError, or another
      // tab keeps holding the pool. Fall back to an in-memory database so the
      // app still works this session; data won't persist across reloads.
      // That's fine for the demo (reseeded hourly and pulled fresh) and a
      // graceful degradation everywhere else. Tell the page so the user learns
      // about it (db.ts shows a toast), not only the console.
      console.warn('OPFS unavailable — using a non-persistent in-memory store:', lastErr);
      (self as unknown as Worker).postMessage({ volatile: true });
      return new sqlite3.oo1.DB(':memory:', 'c');
    })();
  }
  return dbPromise;
}

// Run a query; if the pool has no free slot for the journal/temp file the
// statement fails with SQLITE_CANTOPEN — grow the pool and retry once.
function run(db: any, method: 'exec' | 'all', sql?: string, params?: unknown[]) {
  if (method === 'all') {
    return db.exec({ sql, bind: params, rowMode: 'object', returnValue: 'resultRows' });
  }
  db.exec({ sql, bind: params });
}

self.onmessage = async (e: MessageEvent<Req>) => {
  const { id, method, sql, params, name } = e.data;
  try {
    if (method === 'open') {
      if (name) dbName = name; // must arrive before the first query
      (self as unknown as Worker).postMessage({ id, ok: true });
      return;
    }
    const db = await getDb();
    let rows: unknown;
    try {
      rows = run(db, method, sql, params);
    } catch (err) {
      if (pool && /SQLITE_CANTOPEN/.test(String((err as Error)?.message ?? err))) {
        await pool.addCapacity(8);
        rows = run(db, method, sql, params);
      } else {
        throw err;
      }
    }
    (self as unknown as Worker).postMessage(
      method === 'all' ? { id, ok: true, rows } : { id, ok: true }
    );
  } catch (err) {
    (self as unknown as Worker).postMessage({
      id,
      ok: false,
      error: String((err as Error)?.message ?? err)
    });
  }
};
