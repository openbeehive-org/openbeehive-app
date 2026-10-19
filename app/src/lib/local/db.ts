// Local database on the device. Web: SQLite-WASM on OPFS (persistent).
// Native (Capacitor): same interface, different implementation.
//
// The UI never talks to this class directly, only via lib/local/repo.ts.

export interface LocalDB {
  exec(sql: string, params?: unknown[]): Promise<void>;
  all<T = Record<string, unknown>>(sql: string, params?: unknown[]): Promise<T[]>;
  get<T = Record<string, unknown>>(sql: string, params?: unknown[]): Promise<T | undefined>;
}

// --- Web implementation: SQLite-WASM runs in a dedicated worker (db.worker.ts)
// because OPFS sync-access handles are only available off the main thread.
// This module proxies exec/all/get to that worker over postMessage. ---
import DbWorker from './db.worker?worker';
import { migrateLocal } from './schema';
import { toast } from '#lib/toast.js';
import { get } from 'svelte/store';
import { _ } from 'svelte-i18n';

// Worker reply: a query result, or an unsolicited volatile-store notice.
type Reply = { id?: number; ok?: boolean; rows?: unknown; error?: string; volatile?: boolean };

let dbPromise: Promise<LocalDB> | null = null;

export function getDB(): Promise<LocalDB> {
  if (!dbPromise) dbPromise = init();
  return dbPromise;
}

async function init(): Promise<LocalDB> {
  const worker = new DbWorker();
  let seq = 0;
  const pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>();

  worker.onmessage = (e: MessageEvent<Reply>) => {
    const { id, ok, rows, error, volatile } = e.data;
    if (volatile) {
      // The worker fell back to an in-memory database (OPFS unavailable:
      // private browsing, another tab holding the pool). The app keeps
      // working, but nothing persists; the user must hear that, not just
      // the console.
      toast(get(_)('common.volatile_store'), 15_000);
      return;
    }
    const p = id === undefined ? undefined : pending.get(id);
    if (!p || id === undefined) return;
    pending.delete(id);
    if (ok) p.resolve(rows);
    else p.reject(new Error(error));
  };

  function call(
    method: 'open' | 'exec' | 'all',
    sql: string,
    params: unknown[],
    name?: string
  ): Promise<unknown> {
    return new Promise((resolve, reject) => {
      const id = ++seq;
      pending.set(id, { resolve, reject });
      worker.postMessage({ id, method, sql, params, name });
    });
  }

  // Per-tenant local store: open a database file keyed to the active tenant so
  // switching tenants never mixes data on the same device. Falls back to the
  // single-user "local" store when no tenant is set.
  const orgId =
    (typeof localStorage !== 'undefined' && localStorage.getItem('obh.orgId')) || 'local';
  const dbFile = `openbeehive-${orgId.replace(/[^a-zA-Z0-9_-]/g, '_')}.sqlite3`;
  await call('open', '', [], dbFile);

  const impl: LocalDB = {
    async exec(sql, params = []) {
      await call('exec', sql, params);
    },
    async all<T>(sql: string, params: unknown[] = []) {
      return ((await call('all', sql, params)) ?? []) as T[];
    },
    async get<T>(sql: string, params: unknown[] = []) {
      const rows = await impl.all<T>(sql, params);
      return rows[0];
    }
  };

  await migrateLocal(impl);
  return impl;
}
