// Dedicated SQLite-WASM worker. OPFS sync-access handles are only available in
// a Worker context, so the database lives here and the main thread proxies
// queries via postMessage (see db.ts).
//
// The database file is per-tenant: the main thread sends an `open` message with
// the file name (derived from the active tenant) before any query, so each
// tenant's data lives in its own local store and switching tenants never mixes
// them on the same device.
import sqlite3InitModule, {
  type BindableValue,
  type Database,
  type SAHPoolUtil
} from '@sqlite.org/sqlite-wasm';

type Stmt = { sql: string; params?: BindableValue[] };
type Req = {
  id: number;
  method: 'open' | 'exec' | 'all' | 'batch' | 'wipe';
  sql?: string;
  params?: BindableValue[];
  stmts?: Stmt[];
  name?: string;
  attempts?: number;
};

let dbName = 'openbeehive.sqlite3';
let openAttempts = 3;
let dbPromise: Promise<Database> | null = null;
let pool: SAHPoolUtil | null = null;

// The pool's slot count caps how many files SQLite can hold open: per-tenant
// db files PLUS their journals/temp files. Too few slots fail writes with
// SQLITE_CANTOPEN when the journal can't get a slot. Slots are cheap (empty
// OPFS files), so reserve plenty.
const MIN_POOL_CAPACITY = 24;

async function getDb() {
  if (!dbPromise) {
    dbPromise = (async () => {
      const sqlite3 = await sqlite3InitModule();
      // Persistent: OPFS SAHPool VFS (no SharedArrayBuffer / cross-origin
      // isolation required, works behind any reverse proxy). The pool takes
      // EXCLUSIVE access handles, so a page refresh (or a tab taking the
      // database over) can race the previous worker still holding them:
      // retry briefly before falling back.
      let lastErr: unknown;
      for (let attempt = 0; attempt < openAttempts; attempt++) {
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
      // OPFS stays unavailable in some environments (notably Firefox Private
      // Browsing and browsers/settings that block storage) where
      // navigator.storage.getDirectory() throws a SecurityError. Fall back to
      // an in-memory database so the app still works this session; data that
      // was not synced is gone when the tab closes. Tell the page, which
      // shows a banner for as long as that is the case (see db.ts). A second
      // tab does not end up here: the tab lock keeps it from opening the
      // database at all (tablock.ts).
      console.warn('OPFS unavailable, using a non-persistent in-memory store:', lastErr);
      (self as unknown as Worker).postMessage({ volatile: true });
      return new sqlite3.oo1.DB(':memory:', 'c');
    })();
  }
  return dbPromise;
}

function run(db: Database, req: Req): unknown {
  if (req.method === 'all') {
    return db.exec({
      sql: req.sql ?? '',
      bind: req.params ?? [],
      rowMode: 'object',
      returnValue: 'resultRows'
    });
  }
  if (req.method === 'batch') {
    // One transaction for the whole list: a tab killed halfway leaves either
    // all of it or none of it (a row never exists without its outbox entry).
    db.exec('BEGIN IMMEDIATE');
    try {
      for (const s of req.stmts ?? []) db.exec({ sql: s.sql, bind: s.params ?? [] });
      db.exec('COMMIT');
    } catch (err) {
      try {
        db.exec('ROLLBACK');
      } catch {
        /* the failed statement already ended the transaction */
      }
      throw err;
    }
    return undefined;
  }
  db.exec({ sql: req.sql ?? '', bind: req.params ?? [] });
  return undefined;
}

self.onmessage = async (e: MessageEvent<Req>) => {
  const req = e.data;
  const { id, method } = req;
  try {
    if (method === 'open') {
      // must arrive before the first query
      if (req.name) dbName = req.name;
      if (req.attempts) openAttempts = req.attempts;
      (self as unknown as Worker).postMessage({ id, ok: true });
      return;
    }
    const db = await getDb();
    if (method === 'wipe') {
      // Close the database and delete its file, with the journal beside it,
      // from this device. The connection is gone afterwards.
      db.close();
      dbPromise = null;
      for (const name of pool?.getFileNames() ?? []) {
        if (name.startsWith('/' + dbName)) pool?.unlink(name);
      }
      (self as unknown as Worker).postMessage({ id, ok: true });
      return;
    }
    let rows: unknown;
    try {
      rows = run(db, req);
    } catch (err) {
      // If the pool has no free slot for the journal/temp file the statement
      // fails with SQLITE_CANTOPEN: grow the pool and retry once.
      if (pool && /SQLITE_CANTOPEN/.test(String((err as Error)?.message ?? err))) {
        await pool.addCapacity(8);
        rows = run(db, req);
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
