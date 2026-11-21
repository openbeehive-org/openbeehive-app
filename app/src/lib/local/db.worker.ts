// Dedicated SQLite-WASM worker. OPFS sync-access handles are only available in
// a Worker context, so the database lives here and the main thread proxies
// queries via postMessage (see db.ts).
//
// The database file is per workspace and account: the main thread sends an
// `open` message with the file name before any query, so switching
// workspaces or accounts never mixes their data on the same device.
import sqlite3InitModule, {
  type BindableValue,
  type Database,
  type SAHPoolUtil
} from '@sqlite.org/sqlite-wasm';

type Stmt = { sql: string; params?: BindableValue[] };
type Req = {
  id: number;
  method: 'open' | 'exec' | 'all' | 'batch' | 'wipe' | 'drop';
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

// The SQLite module and the OPFS pool are set up once per worker. A second
// module instance would install a second pool, which cannot take the access
// handles the first one holds (NoModificationAllowedError); that happens
// whenever the main thread opens a second file, as when another account of
// the same workspace signs in (lib/local/owner.ts).
let sqlite3Promise: ReturnType<typeof sqlite3InitModule> | null = null;

async function installPool(sqlite3: Awaited<ReturnType<typeof sqlite3InitModule>>) {
  // The pool takes EXCLUSIVE access handles, so a page refresh (or a tab
  // taking the database over) can race the previous worker still holding
  // them: retry briefly before giving up.
  let lastErr: unknown;
  for (let attempt = 0; attempt < openAttempts; attempt++) {
    if (attempt > 0) await new Promise((r) => setTimeout(r, 300));
    try {
      // Without forceReinitIfPreviouslyFailed (missing from the typings) a
      // retry would only get the cached failure of the first attempt.
      const options = {
        name: 'openbeehive',
        initialCapacity: MIN_POOL_CAPACITY,
        forceReinitIfPreviouslyFailed: true
      };
      const p = await sqlite3.installOpfsSAHPoolVfs(options);
      // initialCapacity only applies when the pool is first created; a pool
      // that already exists on the device keeps its old (possibly smaller)
      // capacity. Grow it explicitly so upgraded installs get the slots too.
      await p.reserveMinimumCapacity(MIN_POOL_CAPACITY);
      return p;
    } catch (err) {
      lastErr = err;
    }
  }
  console.warn('OPFS unavailable, using a non-persistent in-memory store:', lastErr);
  return null;
}

async function getDb() {
  if (!dbPromise) {
    dbPromise = (async () => {
      sqlite3Promise ??= sqlite3InitModule();
      const sqlite3 = await sqlite3Promise;
      // Persistent: OPFS SAHPool VFS (no SharedArrayBuffer / cross-origin
      // isolation required, works behind any reverse proxy).
      pool ??= await installPool(sqlite3);
      if (pool) return new pool.OpfsSAHPoolDb('/' + dbName);
      // OPFS stays unavailable in some environments (notably Firefox Private
      // Browsing and browsers/settings that block storage) where
      // navigator.storage.getDirectory() throws a SecurityError. Fall back to
      // an in-memory database so the app still works this session; data that
      // was not synced is gone when the tab closes. Tell the page, which
      // shows a banner for as long as that is the case (see db.ts). A second
      // tab does not end up here: the tab lock keeps it from opening the
      // database at all (tablock.ts).
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
      // Must arrive before the first query. Opening another file closes the
      // one in use: the main thread may look at one store and then pick
      // another (lib/local/owner.ts).
      if (dbPromise && req.name && req.name !== dbName) {
        (await dbPromise).close();
        dbPromise = null;
      }
      if (req.name) dbName = req.name;
      if (req.attempts) openAttempts = req.attempts;
      (self as unknown as Worker).postMessage({ id, ok: true });
      return;
    }
    const db = await getDb();
    if (method === 'drop') {
      // Delete another store's file, with its journal, from this device.
      // The open one is never touched.
      if (req.name && req.name !== dbName) {
        for (const name of pool?.getFileNames() ?? []) {
          if (name === '/' + req.name || name.startsWith('/' + req.name + '-')) pool?.unlink(name);
        }
      }
      (self as unknown as Worker).postMessage({ id, ok: true });
      return;
    }
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
