// What the sync engine and the repository may write: the synced tables and,
// per table, the columns the local schema really has. Entity and column
// names from a payload or a caller are only ever used in SQL after they
// were found here.
import type { LocalDB } from './db';

export const SYNCED_TABLES = [
  'apiary',
  'hive',
  'queen',
  'inspection',
  'task',
  'placement',
  'harvest',
  'treatment',
  'event',
  'photo'
] as const;

// Columns merged as OR-Sets (add-wins) instead of per-field last-writer-wins.
const SET_COLS: Record<string, readonly string[]> = { inspection: ['photo_keys'] };

// Up to 0.4.x a photo was stored inline, as a data URL: an element of
// inspection.photo_keys, or the value of hive.photo. Apps of that version
// still sync, and the server turns what they write into photo records. This
// app shows the records, so the inline copies that keep arriving for those
// columns are not stored (they are megabytes nobody reads).
const isInlineImage = (v: unknown): boolean => typeof v === 'string' && v.startsWith('data:');

/** A pulled scalar value that is an inline photo. */
export function isInlinePhoto(entity: string, column: string, value: unknown): boolean {
  return entity === 'hive' && column === 'photo' && isInlineImage(value);
}

/** A pulled set delta without the inline photos it adds or removes. */
export function withoutInlinePhotos<
  D extends { add?: string[]; remove?: string[]; removed_tags?: Record<string, string[]> }
>(entity: string, column: string, delta: D): D {
  if (entity !== 'inspection' || column !== 'photo_keys') return delta;
  const keep = (list?: string[]) => list?.filter((e) => !isInlineImage(e));
  const out: D = { ...delta, add: keep(delta.add), remove: keep(delta.remove) };
  if (delta.removed_tags) {
    out.removed_tags = Object.fromEntries(
      Object.entries(delta.removed_tags).filter(([e]) => !isInlineImage(e))
    );
  }
  return out;
}

export const isSynced = (entity: string): boolean =>
  (SYNCED_TABLES as readonly string[]).includes(entity);

export const setColsOf = (entity: string): readonly string[] => SET_COLS[entity] ?? [];

// One schema lookup per table and database handle; the schema only changes
// in migrateLocal, before any of this runs.
const cache = new WeakMap<LocalDB, Map<string, Promise<Set<string>>>>();

// The table's column names, or null when entity is not a synced table.
export async function columnsOf(db: LocalDB, entity: string): Promise<Set<string> | null> {
  if (!isSynced(entity)) return null;
  let perDb = cache.get(db);
  if (!perDb) cache.set(db, (perDb = new Map()));
  let cols = perDb.get(entity);
  if (!cols) {
    cols = db.all<{ name: string }>(`PRAGMA table_info(${entity})`).then((rows) => {
      // No columns means no table: say so, rather than treating every
      // field as unknown and dropping the whole write without a word.
      if (rows.length === 0) throw new Error(`local table "${entity}" is missing`);
      return new Set(rows.map((r) => r.name));
    });
    perDb.set(entity, cols);
    // A failed lookup is retried by the next caller, not remembered.
    cols.catch(() => perDb.delete(entity));
  }
  return cols;
}

// A column value as SQLite stores it: string, finite number or null, with
// booleans as 0/1. Returns undefined for anything else (objects, arrays,
// NaN), which is never a column value.
export function toScalar(v: unknown): string | number | null | undefined {
  if (v === null || typeof v === 'string') return v;
  if (typeof v === 'number') return Number.isFinite(v) ? v : undefined;
  if (typeof v === 'boolean') return v ? 1 : 0;
  return undefined;
}
