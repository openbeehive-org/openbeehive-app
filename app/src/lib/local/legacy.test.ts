// Installed 0.4.x apps keep syncing against a newer server.
//
// Such an app stores every key of a pulled payload in a column of that name
// and retries a page that fails for ever, so one key it has no column for
// ends its sync for good. The server therefore sends clients that announce
// no protocol version only what release 0.4.1 could store.
//
// This test runs the pull code of release 0.4.1 itself, on the schema of
// release 0.4.1, over changes the current server produced for such a client:
// server/internal/service/testdata/legacy_pull_v0.4.1.json, written by the Go
// test TestLegacyPullGolden from what a current app pushes (every column of
// every table, the newer ones included).
import { describe, it, expect, beforeEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { DatabaseSync } from 'node:sqlite';
import type { LocalDB } from './db';
import { ChangeOp, type Change } from '../proto/openbeehive/v1/sync_pb';

// ---------------------------------------------------------------------------
// Release 0.4.1, verbatim. Do not edit: this is the code that runs on
// installed devices. Taken from the tag with
//   git show v0.4.1:app/src/lib/local/schema.ts   (migrateLocal, ensureColumns)
//   git show v0.4.1:app/src/lib/local/merge.ts    (the helpers applyRemote uses)
//   git show v0.4.1:app/src/lib/local/hlc.ts      (compare)
//   git show v0.4.1:app/src/lib/local/sync.ts     (SYNCED_TABLES, SET_COLS, applyRemote)
// Only "export" keywords and the import lines are removed; getDB() returns
// the test database.
// ---------------------------------------------------------------------------

let db041: LocalDB;
const getDB = async () => db041;

// Local mirror schema. field_hlc = field clock (per-field LWW),
// photo_keys holds OR-Set JSON. outbox = changes not yet pushed.
async function migrateLocal(db: LocalDB) {
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

const compare = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

type FieldClock = Record<string, string>;

function parseFieldClock(s: string | null | undefined): FieldClock {
  if (!s) return {};
  try {
    return JSON.parse(s);
  } catch {
    return {};
  }
}

function accept(fc: FieldClock, field: string, hlc: string): boolean {
  if (compare(hlc, fc[field] ?? '') > 0) {
    fc[field] = hlc;
    return true;
  }
  return false;
}

type Elem = { a: string[]; r: string[] };
type ORSet = Record<string, Elem>;

function parseORSet(s: string | null | undefined): ORSet {
  if (!s || s === 'null') return {};
  try {
    return JSON.parse(s);
  } catch {
    return {};
  }
}

const uniq = (xs: string[], x: string) => (xs.includes(x) ? xs : [...xs, x]);

function orAdd(set: ORSet, elem: string, tag: string) {
  const e = set[elem] ?? { a: [], r: [] };
  e.a = uniq(e.a, tag);
  set[elem] = e;
}

function orRemove(set: ORSet, elem: string) {
  const e = set[elem] ?? { a: [], r: [] };
  for (const t of e.a) e.r = uniq(e.r, t); // remove only observed tags
  set[elem] = e;
}

function orRemoveTags(set: ORSet, elem: string, tags: string[]) {
  const e = set[elem] ?? { a: [], r: [] };
  for (const t of tags) e.r = uniq(e.r, t);
  set[elem] = e;
}

const SYNCED_TABLES = [
  'apiary',
  'hive',
  'queen',
  'inspection',
  'task',
  'placement',
  'harvest',
  'treatment',
  'event'
];
const SET_COLS: Record<string, string[]> = { inspection: ['photo_keys'] };

// OR-Set delta carried in a set column of a change payload.
type SetDelta = { add?: string[]; remove?: string[]; removed_tags?: Record<string, string[]> };

// Per-field LWW + OR-Set: only newer fields are applied, set columns are
// merged by union.
async function applyRemote(ch: Change) {
  if (!SYNCED_TABLES.includes(ch.entity)) return;
  const db = await getDB();
  const cur = await db.get<any>(`SELECT * FROM ${ch.entity} WHERE id = ?`, [ch.entityId]);
  const fields: Record<string, unknown> =
    ch.op === ChangeOp.DELETE ? { deleted: 1 } : JSON.parse(ch.payloadJson);
  const setCols = SET_COLS[ch.entity] ?? [];

  if (!cur) {
    const fc: Record<string, string> = {};
    const cols = ['id'];
    const vals: unknown[] = [ch.entityId];
    for (const [k, v] of Object.entries(fields)) {
      if (k === 'id') continue;
      if (setCols.includes(k)) {
        const os = {};
        const d = v as SetDelta | undefined;
        for (const e of d?.add ?? []) orAdd(os, e, ch.hlc);
        cols.push(k);
        vals.push(JSON.stringify(os));
      } else {
        cols.push(k);
        vals.push(v);
        fc[k] = ch.hlc;
      }
    }
    cols.push('field_hlc');
    vals.push(JSON.stringify(fc));
    const ph = cols.map(() => '?').join(', ');
    await db.exec(`INSERT INTO ${ch.entity} (${cols.join(', ')}) VALUES (${ph})`, vals);
    return;
  }

  const fc = parseFieldClock(cur.field_hlc);
  const sets: string[] = [];
  const args: unknown[] = [];
  for (const [k, v] of Object.entries(fields)) {
    if (k === 'id' || k === 'field_hlc') continue;
    if (setCols.includes(k)) {
      const os = parseORSet(cur[k]);
      const d = v as SetDelta | undefined;
      for (const e of d?.add ?? []) orAdd(os, e, ch.hlc);
      for (const e of d?.remove ?? []) {
        // Newer deltas say which tags the origin observed; only those are
        // tombstoned (add-wins). Old deltas without tags remove all local ones.
        const tags = d?.removed_tags?.[e];
        if (tags) orRemoveTags(os, e, tags);
        else orRemove(os, e);
      }
      sets.push(`${k} = ?`);
      args.push(JSON.stringify(os));
    } else if (accept(fc, k, ch.hlc)) {
      sets.push(`${k} = ?`);
      args.push(v);
    }
  }
  if (sets.length === 0) return; // everything stale
  sets.push(`field_hlc = ?`);
  args.push(JSON.stringify(fc));
  args.push(ch.entityId);
  await db.exec(`UPDATE ${ch.entity} SET ${sets.join(', ')} WHERE id = ?`, args);
}

// ---------------------------------------------------------------------------
// End of release 0.4.1.
// ---------------------------------------------------------------------------

type Golden = {
  entity: string;
  entityId: string;
  scopeId: string;
  op: number;
  payloadJson: string;
  hlc: string;
};

const golden: Golden[] = JSON.parse(
  readFileSync(
    new URL(
      '../../../../server/internal/service/testdata/legacy_pull_v0.4.1.json',
      import.meta.url
    ),
    'utf8'
  )
);

const asChange = (g: Golden) => g as unknown as Change;

// A database as release 0.4.1 creates it (node:sqlite, in memory).
async function make041DB(): Promise<LocalDB> {
  const sqlite = new DatabaseSync(':memory:');
  const impl: LocalDB = {
    async exec(sql, params = []) {
      if (params.length === 0) sqlite.exec(sql);
      else sqlite.prepare(sql).run(...(params as any[]));
    },
    async all<T>(sql: string, params: unknown[] = []) {
      return sqlite.prepare(sql).all(...(params as any[])) as T[];
    },
    async get<T>(sql: string, params: unknown[] = []) {
      return sqlite.prepare(sql).get(...(params as any[])) as T | undefined;
    },
    async batch() {
      throw new Error('release 0.4.1 has no batch()');
    }
  };
  db041 = impl;
  await migrateLocal(impl);
  return impl;
}

const columns = async (table: string) =>
  (await db041.all<{ name: string }>(`PRAGMA table_info(${table})`)).map((c) => c.name);

beforeEach(async () => {
  await make041DB();
});

describe('a 0.4.1 app pulling from the current server', () => {
  it('stores every change the server sends it', async () => {
    expect(golden.length).toBeGreaterThan(SYNCED_TABLES.length * 3);
    for (const g of golden) await applyRemote(asChange(g));

    for (const table of SYNCED_TABLES) {
      // The row that was written in full and then edited, and the deleted one.
      const kept = await db041.get<any>(`SELECT * FROM ${table} WHERE id = ?`, [`${table}-1`]);
      expect(kept, table).toBeTruthy();
      expect(kept.deleted, table).toBe(0);
      const gone = await db041.get<any>(`SELECT deleted FROM ${table} WHERE id = ?`, [
        `${table}-2`
      ]);
      expect(gone?.deleted, table).toBe(1);
    }
    // The photo set went through add and remove deltas as 0.4.1 reads them.
    const insp = await db041.get<any>(
      `SELECT photo_keys FROM inspection WHERE id = 'inspection-1'`
    );
    const set = parseORSet(insp.photo_keys);
    const visible = Object.keys(set).filter((k) =>
      set[k]!.a.some((tag) => !set[k]!.r.includes(tag))
    );
    expect(visible).toEqual(['k2']);
  });

  it('is sent every column it has, and no other', async () => {
    const sent: Record<string, Set<string>> = {};
    for (const g of golden) {
      expect(SYNCED_TABLES, g.entity).toContain(g.entity);
      if (g.op === ChangeOp.DELETE) continue;
      const keys = (sent[g.entity] ??= new Set());
      for (const k of Object.keys(JSON.parse(g.payloadJson))) keys.add(k);
    }
    for (const table of SYNCED_TABLES) {
      const own = (await columns(table)).filter((c) => c !== 'id' && c !== 'field_hlc');
      expect([...sent[table]!].sort(), table).toEqual(own.sort());
    }
  });

  // The reason for all of the above, and the proof that this test notices a
  // leak: the same code fails on a single key from a later release.
  it('cannot store a column added after 0.4.1', async () => {
    const ok = golden.find((g) => g.entity === 'inspection' && g.entityId === 'inspection-1')!;
    await applyRemote(asChange(ok));
    for (const [entity, column] of [
      ['inspection', 'source'],
      ['treatment', 'amended_at'],
      ['harvest', 'amended_at']
    ]) {
      expect(await columns(entity!)).not.toContain(column);
      const leak = {
        entity: entity!,
        entityId: `${entity}-9`,
        scopeId: 'apiary-a',
        op: 1,
        payloadJson: JSON.stringify({ hive_id: 'hive-a', [column!]: 1 }),
        hlc: ok.hlc
      };
      await expect(applyRemote(asChange(leak)), `${entity}.${column}`).rejects.toThrow(/no column/);
    }
    // Also as an update of a row it already has.
    const update = {
      ...ok,
      payloadJson: JSON.stringify({ note: 'x', source: 2 }),
      hlc: ok.hlc + 'z'
    };
    await expect(applyRemote(asChange(update))).rejects.toThrow(/no such column/);
  });
});

// A photo added on a 0.4.1 device and deleted on an updated one, as the
// server tells the 0.4.1 device about it: server/internal/service/testdata/
// legacy_photo_v0.4.1.json, written by the Go test TestLegacyPhotoGolden.
const photoFeed: { added: Golden[]; deleted: Golden[]; fresh: Golden[] } = JSON.parse(
  readFileSync(
    new URL(
      '../../../../server/internal/service/testdata/legacy_photo_v0.4.1.json',
      import.meta.url
    ),
    'utf8'
  )
);

async function visitPhotos(): Promise<string[]> {
  const row = await db041.get<any>(`SELECT photo_keys FROM inspection WHERE id = 'visit-1'`);
  const set = parseORSet(row?.photo_keys);
  return Object.keys(set)
    .filter((k) => set[k]!.a.some((tag) => !set[k]!.r.includes(tag)))
    .sort();
}

const hivePicture = async () =>
  (await db041.get<{ photo: string }>(`SELECT photo FROM hive WHERE id = 'hive-a'`))?.photo;

describe('photos on a 0.4.1 device', () => {
  const [visitAdd, pictureSet] = photoFeed.added;
  const added: string[] = JSON.parse(visitAdd!.payloadJson).photo_keys.add;
  const picture: string = JSON.parse(pictureSet!.payloadJson).photo;

  it('are sent in the inline form it stores, never as photo records', () => {
    for (const g of [...photoFeed.added, ...photoFeed.deleted, ...photoFeed.fresh]) {
      expect(SYNCED_TABLES, g.entity).toContain(g.entity);
    }
    expect(added).toHaveLength(2);
    for (const p of [...added, picture]) expect(p).toMatch(/^data:image\/jpeg;base64,/);
  });

  it('shows the photos it added, then drops the ones an updated device deleted', async () => {
    for (const g of photoFeed.added) await applyRemote(asChange(g));
    expect(await visitPhotos()).toEqual([...added].sort());
    expect(await hivePicture()).toBe(picture);

    for (const g of photoFeed.deleted) await applyRemote(asChange(g));
    const left = await visitPhotos();
    expect(left).toHaveLength(1);
    expect(added).toContain(left[0]);
    expect(await hivePicture()).toBe('');
    const visit = await db041.get<any>(`SELECT note, deleted FROM inspection WHERE id = 'visit-1'`);
    expect(visit).toEqual({ note: 'two photos', deleted: 0 });
  });

  it('a device that syncs for the first time sees only the photo that is left', async () => {
    for (const g of photoFeed.added) await applyRemote(asChange(g));
    for (const g of photoFeed.deleted) await applyRemote(asChange(g));
    const expected = await visitPhotos();

    await make041DB();
    for (const g of photoFeed.fresh) await applyRemote(asChange(g));
    expect(await visitPhotos()).toEqual(expected);
    expect(await hivePicture()).toBe('');
  });
});
