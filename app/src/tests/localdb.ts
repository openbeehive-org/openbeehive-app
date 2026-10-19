// A LocalDB implementation backed by node:sqlite (real SQLite, in-memory),
// running the exact schema from lib/local/schema.ts. Tests exercise the same
// SQL the browser store runs, minus the OPFS/worker plumbing.
import { DatabaseSync } from 'node:sqlite';
import type { LocalDB } from '$lib/local/db';
import { migrateLocal } from '$lib/local/schema';

export async function makeLocalDB(): Promise<LocalDB> {
  const db = new DatabaseSync(':memory:');
  const impl: LocalDB = {
    async exec(sql, params = []) {
      if (params.length === 0) {
        db.exec(sql); // multi-statement scripts (schema)
      } else {
        db.prepare(sql).run(...(params as any[]));
      }
    },
    async all<T>(sql: string, params: unknown[] = []) {
      return db.prepare(sql).all(...(params as any[])) as T[];
    },
    async get<T>(sql: string, params: unknown[] = []) {
      return db.prepare(sql).get(...(params as any[])) as T | undefined;
    }
  };
  await migrateLocal(impl);
  return impl;
}

// getDB() in lib/local is a singleton; tests that simulate several devices
// switch the active database here (see the vi.mock in the test files).
let current: LocalDB | null = null;
export function setCurrentDB(db: LocalDB) {
  current = db;
}
export function getCurrentDB(): LocalDB {
  if (!current) throw new Error('setCurrentDB() first');
  return current;
}
