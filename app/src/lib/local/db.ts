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
import { get } from 'svelte/store';
import { initialSyncPending, storageState } from './live';
import { claimTab, takeoverRequested } from './tablock';
import { discardUnclaimed, openOwnStore } from './owner';
import { openFileStore, removeFileStore } from '../photos/files';
import { CodedError } from '../errors';

const elsewhere = () => new CodedError('db_elsewhere', 'local database is open in another tab');

const API = (import.meta.env?.BEEHIVE_API_URL ?? '').replace(/\/$/, '');

// Worker reply: a query result, or an unsolicited volatile-store notice.
type Reply = { id?: number; ok?: boolean; rows?: unknown; error?: string; volatile?: boolean };
type Method = 'open' | 'exec' | 'all' | 'batch' | 'wipe' | 'drop';

let dbPromise: Promise<LocalDB> | null = null;
// Set once the database is open; see wipeLocalDB.
let wipe: (() => Promise<unknown>) | null = null;
// The workspace and account the open store belongs to, and its key.
let opened: { org: string; user: string } | null = null;
let storeKey = '';

/**
 * The workspace and account the local store was opened for, or null while
 * none is open. The layout reloads when the server reports another one.
 */
export const openedFor = (): { org: string; user: string } | null => opened;

/** Key of the open local store; other stores on the device (photo images) use the same. */
export async function localStoreKey(): Promise<string> {
  await getDB();
  return storeKey;
}

/**
 * Deletes the open workspace's database from this device, including changes
 * that were never uploaded. The connection is closed by it: leave or reload
 * the page afterwards. Fails while another tab owns the database: nothing is
 * removed then, and the user has to be told.
 */
export async function wipeLocalDB(): Promise<void> {
  if (get(storageState) === 'elsewhere') throw elsewhere();
  await getDB();
  await wipe?.();
}

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
    for (const p of pending.values()) p.reject(elsewhere());
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
        reject(elsewhere());
        return;
      }
      const id = ++seq;
      pending.set(id, { resolve, reject });
      worker.postMessage({ id, method, ...msg });
    });
  }

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

  // One local store per workspace and account (see owner.ts), so people who
  // share a device never see or upload each other's unsent changes.
  const org = localStorage.getItem('obh.orgId') || 'local';
  const { user, confirmed } = await whoIsSignedIn();
  opened = { org, user };
  let unclaimed = '';
  storeKey = await openOwnStore(
    async (key) => {
      // After a takeover the previous tab's worker may still be letting go
      // of its file handles; give the open a little longer before giving up.
      await call('open', { name: storeFile(key), attempts: stolen ? 12 : 3 });
      await migrateLocal(impl);
      return impl;
    },
    org,
    user,
    confirmed,
    (key) => (unclaimed = key)
  );
  wipe = () => call('wipe');
  if (unclaimed)
    discardAfterFirstSync(impl, unclaimed, (key) => call('drop', { name: storeFile(key) }));
  return impl;
}

const storeFile = (key: string) => `openbeehive-${key}.sqlite3`;

// Removes an unclaimed store (owner.ts) once the open store has completed a
// sync with the server, checking again every minute until it has.
function discardAfterFirstSync(db: LocalDB, key: string, drop: (key: string) => Promise<unknown>) {
  const synced = async () =>
    !!(await db.get(
      `SELECT 1 AS x FROM sync_meta WHERE (k = 'cursor' AND v <> '') OR (k = 'last_sync' AND v <> '')`
    ));
  // One connection to the old photo store: deleting it waits for open ones.
  const files = openFileStore(key);
  const photosPending = async () => (await files.list()).some((m) => m.state === 'pending');
  const remove = async (k: string) => {
    await drop(k);
    await removeFileStore(k, files);
  };
  let started = false;
  const attempt = async () => {
    try {
      if (!(await synced())) {
        setTimeout(() => void attempt(), 60_000);
        return;
      }
      await discardUnclaimed(key, { photosPending, remove });
    } catch (err) {
      console.warn('old local store kept:', err);
    }
  };
  const stop = initialSyncPending.subscribe((pending) => {
    if (pending || started) return;
    started = true;
    queueMicrotask(() => stop());
    void attempt();
  });
}

// The account the store is opened for. Normally a sign-in left it on the
// device; after a sign-in that only set a cookie (single sign-on) it is asked
// from the server. Without an answer the device is treated as single-user,
// which never claims a store (owner.ts).
async function whoIsSignedIn(): Promise<{ user: string; confirmed: boolean }> {
  const stored = localStorage.getItem('obh.userId');
  if (stored) return { user: stored, confirmed: true };
  try {
    const token = localStorage.getItem('session');
    const r = await fetch(`${API}/auth/me`, {
      credentials: 'include',
      headers: token ? { Authorization: `Bearer ${token}` } : {}
    });
    const me = r.ok ? ((await r.json()) as { user_id?: string; active_org?: string }) : {};
    if (me.user_id) {
      localStorage.setItem('obh.userId', me.user_id);
      return { user: me.user_id, confirmed: true };
    }
  } catch {
    /* offline or no server: fall through */
  }
  return { user: 'local', confirmed: false };
}
