// The local mirror schema and its evolution, shared by the real
// SQLite-WASM store (db.ts) and the node-side test database.
import type { LocalDB } from './db';

// Local mirror schema. field_hlc = field clock (per-field LWW),
// photo_keys holds OR-Set JSON. outbox = changes not yet pushed.
export async function migrateLocal(db: LocalDB) {
  await db.exec(`
    CREATE TABLE IF NOT EXISTS apiary (
      id TEXT PRIMARY KEY, organization_id TEXT, name TEXT, address TEXT,
      lat REAL, lng REAL, note TEXT, created_at TEXT, updated_at TEXT,
      field_hlc TEXT DEFAULT '{}', deleted INTEGER DEFAULT 0);
    CREATE TABLE IF NOT EXISTS hive (
      id TEXT PRIMARY KEY, organization_id TEXT, apiary_id TEXT, name TEXT,
      type INTEGER, status INTEGER, boxes INTEGER, colony_origin TEXT, note TEXT,
      qr_code TEXT, photo TEXT, created_at TEXT, updated_at TEXT, field_hlc TEXT DEFAULT '{}', deleted INTEGER DEFAULT 0);
    CREATE TABLE IF NOT EXISTS queen (
      id TEXT PRIMARY KEY, organization_id TEXT, hive_id TEXT, year INTEGER,
      marking INTEGER, origin TEXT, breeder_number TEXT, introduced_at TEXT,
      replaced_at TEXT, active INTEGER, note TEXT, created_at TEXT, updated_at TEXT,
      field_hlc TEXT DEFAULT '{}', deleted INTEGER DEFAULT 0);
    CREATE TABLE IF NOT EXISTS inspection (
      id TEXT PRIMARY KEY, organization_id TEXT, hive_id TEXT, date TEXT,
      weather TEXT, queen_seen INTEGER, eggs_seen INTEGER, temperament INTEGER,
      frames INTEGER, stores INTEGER, queen_cells INTEGER, varroa TEXT,
      honey_kg REAL, note TEXT,
      -- Stockkarte fields: colony, behaviour and the activities done on the visit.
      brood_frames INTEGER, calmness INTEGER, fed_kg REAL,
      frames_added INTEGER, frames_removed INTEGER, drone_frame_cut INTEGER,
      super_added INTEGER, weight_kg REAL,
      youngest_larva INTEGER, covered_larva INTEGER,
      -- Climate readings: temperature (°C) and humidity (%), inside the hive and outside.
      temp_hive REAL, temp_outside REAL, humidity_hive REAL, humidity_outside REAL,
      photo_keys TEXT DEFAULT '{}', created_at TEXT, field_hlc TEXT DEFAULT '{}', deleted INTEGER DEFAULT 0);
    CREATE TABLE IF NOT EXISTS task (
      id TEXT PRIMARY KEY, organization_id TEXT, title TEXT, hive_id TEXT,
      apiary_id TEXT, due_at TEXT, done INTEGER, priority INTEGER,
      note TEXT, recurrence TEXT, assigned_to TEXT, created_at TEXT,
      field_hlc TEXT DEFAULT '{}', deleted INTEGER DEFAULT 0);

    -- history / fact table
    CREATE TABLE IF NOT EXISTS placement (
      id TEXT PRIMARY KEY, organization_id TEXT, hive_id TEXT, apiary_id TEXT,
      start_at TEXT, end_at TEXT, field_hlc TEXT DEFAULT '{}', deleted INTEGER DEFAULT 0);
    CREATE TABLE IF NOT EXISTS harvest (
      id TEXT PRIMARY KEY, organization_id TEXT, apiary_id TEXT, hive_id TEXT,
      queen_id TEXT, date TEXT, variety TEXT, amount_kg REAL, water_content REAL,
      batch_number TEXT, best_before TEXT, note TEXT, field_hlc TEXT DEFAULT '{}', deleted INTEGER DEFAULT 0);
    -- Treatments (Bestandsbuch): varroa / disease treatments, audit-ready.
    CREATE TABLE IF NOT EXISTS treatment (
      id TEXT PRIMARY KEY, organization_id TEXT, apiary_id TEXT, hive_id TEXT, queen_id TEXT,
      date TEXT, product TEXT, active_ingredient TEXT, dose TEXT, method TEXT,
      batch_number TEXT, withdrawal_until TEXT, reason TEXT, note TEXT,
      field_hlc TEXT DEFAULT '{}', deleted INTEGER DEFAULT 0);
    CREATE TABLE IF NOT EXISTS event (
      id TEXT PRIMARY KEY, organization_id TEXT, scope_id TEXT, type INTEGER, date TEXT,
      apiary_id TEXT, hive_id TEXT, queen_id TEXT, ref_entity TEXT, ref_id TEXT,
      title TEXT, amount_kg REAL DEFAULT 0, detail TEXT, author_id TEXT,
      field_hlc TEXT DEFAULT '{}', deleted INTEGER DEFAULT 0);
    CREATE INDEX IF NOT EXISTS idx_event_hive ON event (hive_id, date);
    CREATE INDEX IF NOT EXISTS idx_event_apiary ON event (apiary_id, date);

    -- Outbox: local changes not yet pushed (partial field deltas).
    CREATE TABLE IF NOT EXISTS outbox (
      id TEXT PRIMARY KEY, entity TEXT, entity_id TEXT, scope_id TEXT,
      op INTEGER, payload TEXT, hlc TEXT, author_id TEXT);

    CREATE TABLE IF NOT EXISTS sync_meta (k TEXT PRIMARY KEY, v TEXT);
  `);

  // Schema evolution for databases created by an earlier app version:
  // CREATE TABLE IF NOT EXISTS never adds columns to an existing table, so
  // reconcile any columns added after the initial schema. Without this, writes
  // that reference a new column fail on an old local DB (e.g. saving a visit).
  await ensureColumns(db, 'hive', [['photo', 'TEXT']]);
  await ensureColumns(db, 'inspection', [
    ['brood_frames', 'INTEGER'],
    ['calmness', 'INTEGER'],
    ['fed_kg', 'REAL'],
    ['frames_added', 'INTEGER'],
    ['frames_removed', 'INTEGER'],
    ['drone_frame_cut', 'INTEGER'],
    ['super_added', 'INTEGER'],
    ['weight_kg', 'REAL'],
    ['youngest_larva', 'INTEGER'],
    ['covered_larva', 'INTEGER'],
    ['temp_hive', 'REAL'],
    ['temp_outside', 'REAL'],
    ['humidity_hive', 'REAL'],
    ['humidity_outside', 'REAL']
  ]);
}

// Add any missing columns to an existing table (idempotent; SQLite ALTER TABLE
// ADD COLUMN errors if the column already exists, so we check first).
async function ensureColumns(db: LocalDB, table: string, cols: [string, string][]) {
  const info = await db.all<{ name: string }>(`PRAGMA table_info(${table})`);
  const existing = new Set(info.map((r) => r.name));
  for (const [name, type] of cols) {
    if (!existing.has(name)) await db.exec(`ALTER TABLE ${table} ADD COLUMN ${name} ${type}`);
  }
}
