// The sync engine. Runs in the background: pushes the outbox up (Push),
// pulls foreign changes (Pull) and applies them via last-writer-wins.
// Works offline (queued) and syncs automatically once connected.
//
// A device that has no pull position yet, or whose position the server can
// no longer serve, loads the current rows instead (Snapshot) and continues
// with Pull from there.

import { ConnectError, Code } from '@connectrpc/connect';
import { get, writable, type Readable } from 'svelte/store';
import { getDB, type LocalDB, type Statement } from './db';
import { hlc, isHLC, formatHLC } from './hlc';
import { exclusive } from './lock';
import {
  parseFieldClock,
  parseORSet,
  parseSetDelta,
  mergeFields,
  mergeSnapshot,
  toORSet,
  tombstone,
  type FieldClock,
  type ORSet,
  type RowState,
  type SetDelta
} from './merge';
import { SYNCED_TABLES, columnsOf, setColsOf, toScalar } from './tables';
import { bumpData, initialSyncPending, resyncNotice, storageState } from './live';
import { syncClient } from '#lib/client.js'; // generated Connect client (SyncService)
import {
  ChangeOp,
  PullStatus,
  UpgradeRequiredSchema,
  type Change,
  type SnapshotRow
} from '../proto/openbeehive/v1/sync_pb';

// --- Status, for the status chip and the "sync problems" list ---

// idle: nothing to do or all done. syncing: a run is in flight. offline: no
// network. error: the last run failed and will be retried. signed_out: the
// server no longer accepts this session; sync stays off until sign-in.
// update_required: the server no longer talks to this version of the app;
// sync stays off until the app is updated.
export type SyncState = 'idle' | 'syncing' | 'offline' | 'error' | 'signed_out' | 'update_required';

export interface SyncStatus {
  state: SyncState;
  pending: number; // local changes waiting to be uploaded
  failed: number; // changes the server refused; see listFailed()
  lastSyncedAt: number | null; // unix ms of the last complete run on this device
}

const status = writable<SyncStatus>({ state: 'idle', pending: 0, failed: 0, lastSyncedAt: null });
export const syncStatus: Readable<SyncStatus> = { subscribe: status.subscribe };

const setState = (state: SyncState) => status.update((s) => ({ ...s, state }));

async function counts(db: LocalDB) {
  const r = await db.get<{ pending: number; failed: number }>(
    `SELECT (SELECT COUNT(*) FROM outbox) AS pending,
            (SELECT COUNT(*) FROM outbox_failed) AS failed`
  );
  return { pending: r?.pending ?? 0, failed: r?.failed ?? 0 };
}

async function refreshCounts() {
  const c = await counts(await getDB());
  status.update((s) => ({ ...s, ...c }));
}

// Local changes that have not reached the server: waiting or refused. What
// signing out (which clears the session, not the data) would leave behind
// unsynced; ask before doing it when this is not 0.
export async function unsentCount(): Promise<number> {
  const c = await counts(await getDB());
  return c.pending + c.failed;
}

// --- sync_meta: cursor and bookkeeping ---

async function getMeta(k: string): Promise<string | undefined> {
  const db = await getDB();
  const r = await db.get<{ v: string }>(`SELECT v FROM sync_meta WHERE k = ?`, [k]);
  return r?.v;
}

const setMetaStmt = (k: string, v: string): Statement => ({
  sql: `INSERT INTO sync_meta (k, v) VALUES (?, ?)
        ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
  params: [k, v]
});

async function setMeta(k: string, v: string) {
  const db = await getDB();
  await db.batch([setMetaStmt(k, v)]);
}

const getCursor = async () => (await getMeta('cursor')) ?? '';

// Version of the local repairs this database has been through (kept under
// the key "sync_epoch"). Raising it runs them once more on every device and
// makes the device load the current rows again (merging is idempotent, so
// that only fills gaps).
//   1: the cursor used to be overwritten with a counter returned by Push,
//      so a device that pushed skipped what others had written before.
//   2: Pull used to return a user's personal tasks in every workspace, so
//      tasks of other workspaces ended up in this one's database. They are
//      removed here; nothing of this workspace is.
const LOCAL_REPAIR = 2;

const workspaceId = () => localStorage.getItem('obh.orgId') ?? 'local';
const userId = () => localStorage.getItem('obh.userId') ?? 'local';

async function repairLocal(db: LocalDB) {
  if (Number((await getMeta('sync_epoch')) ?? 0) >= LOCAL_REPAIR) return;
  await db.batch([
    {
      // A personal task (no apiary) that names another workspace was pulled
      // from there. One that still waits in the outbox was written here.
      sql: `DELETE FROM task
            WHERE COALESCE(apiary_id, '') = ''
              AND COALESCE(organization_id, '') NOT IN ('', ?)
              AND id NOT IN (SELECT entity_id FROM outbox WHERE entity = 'task')`,
      params: [workspaceId()]
    },
    setMetaStmt('cursor', ''),
    setMetaStmt('sync_epoch', String(LOCAL_REPAIR))
  ]);
}

// Every response carries the server's wall clock; the middle of the round
// trip is the device time it belongs to.
function learnServerTime(serverTimeMs: bigint | number | undefined, sentAt: number) {
  const serverMs = Number(serverTimeMs ?? 0);
  if (serverMs > 0) hlc.syncTo(serverMs, (sentAt + Date.now()) / 2);
}

// --- Push: local outbox to the server ---

// One request stays far below the server's 16 MiB limit, also when every
// character takes three bytes.
const MAX_PUSH_CHANGES = 200;
const MAX_PUSH_CHARS = 4 * 1024 * 1024;

type OutboxRow = {
  id: string;
  entity: string;
  entity_id: string;
  scope_id: string;
  op: number;
  payload: string;
  hlc: string;
  author_id: string;
};

// The oldest outbox entries that fit one request, by count and by size.
// A single entry larger than the budget still goes out, alone.
async function nextBatch(db: LocalDB, limit: number): Promise<OutboxRow[]> {
  const head = await db.all<{ id: string; n: number }>(
    `SELECT id, length(payload) AS n FROM outbox ORDER BY hlc ASC, id ASC LIMIT ?`,
    [limit]
  );
  const ids: string[] = [];
  let chars = 0;
  for (const h of head) {
    chars += (h.n ?? 0) + 256;
    if (ids.length > 0 && chars > MAX_PUSH_CHARS) break;
    ids.push(h.id);
  }
  if (ids.length === 0) return [];
  return db.all<OutboxRow>(
    `SELECT * FROM outbox WHERE id IN (${ids.map(() => '?').join(', ')}) ORDER BY hlc ASC, id ASC`,
    ids
  );
}

const dropStmt = (p: OutboxRow): Statement => ({
  sql: `DELETE FROM outbox WHERE id = ?`,
  params: [p.id]
});

// Moves an entry out of the outbox into the list of refused changes.
const parkStmts = (p: OutboxRow, code: string, reason: string): Statement[] => [
  {
    sql: `INSERT OR REPLACE INTO outbox_failed
            (id, entity, entity_id, scope_id, op, payload, hlc, author_id, code, reason, failed_at)
          VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    params: [
      p.id,
      p.entity,
      p.entity_id,
      p.scope_id,
      p.op,
      p.payload,
      p.hlc,
      p.author_id,
      code,
      reason,
      new Date().toISOString()
    ]
  },
  dropStmt(p)
];

// What a failed Push request means for its batch.
//   drop     read-only session: the server takes no uploads at all, so the
//            intent is discarded (the data stays in the local tables)
//   isolate  the request itself was refused (too large, malformed): one of
//            its changes is at fault; halve the batch until it stands alone
//   retry    everything else (offline, 5xx, signed out): keep and retry later
function pushFailure(e: unknown): 'drop' | 'isolate' | 'retry' {
  if (!(e instanceof ConnectError)) return 'retry';
  if (e.code === Code.PermissionDenied) return 'drop';
  if (e.code === Code.InvalidArgument || e.code === Code.ResourceExhausted) return 'isolate';
  return 'retry';
}

export async function push() {
  const db = await getDB();
  let limit = MAX_PUSH_CHANGES;
  let restamped = false;
  for (;;) {
    const batch = await nextBatch(db, limit);
    if (batch.length === 0) return;

    const sentAt = Date.now();
    let res;
    try {
      res = await syncClient.push({
        changes: batch.map((p) => ({
          entity: p.entity,
          entityId: p.entity_id,
          scopeId: p.scope_id,
          op: p.op,
          payloadJson: p.payload,
          hlc: p.hlc,
          authorId: p.author_id
        }))
      });
    } catch (e) {
      const failure = pushFailure(e);
      if (failure === 'retry') throw e;
      if (failure === 'drop') {
        console.warn('push rejected as read-only, keeping changes locally:', e);
        await db.batch(batch.map(dropStmt));
      } else if (batch.length > 1) {
        limit = Math.floor(batch.length / 2);
      } else {
        const code = (e as ConnectError).code === Code.ResourceExhausted ? 'too_large' : 'invalid';
        await db.batch(parkStmts(batch[0]!, code, (e as ConnectError).rawMessage));
      }
      continue;
    }
    learnServerTime(res.serverTimeMs, sentAt);
    limit = Math.min(MAX_PUSH_CHANGES, limit * 2);

    // Accepted entries leave the outbox. Refused ones are kept where the
    // user can see them, except "clock_ahead": those only carry a stamp from
    // a device clock that ran ahead, which is repaired below.
    const rejected = new Map((res.rejected ?? []).map((r) => [r.index, r]));
    const canRestamp = !restamped && Number(res.serverTimeMs ?? 0) > 0;
    const stmts: Statement[] = [];
    let ahead = 0;
    batch.forEach((p, i) => {
      const r = rejected.get(i);
      if (!r) stmts.push(dropStmt(p));
      else if (r.code === 'clock_ahead' && canRestamp) ahead++;
      else stmts.push(...parkStmts(p, r.code || 'invalid', r.reason));
    });
    await db.batch(stmts);
    if (ahead > 0) {
      await restampAhead(db);
      restamped = true;
    }
  }
}

// The server refused changes as stamped ahead of its clock: this device's
// clock ran (or runs) ahead. By now the clock is corrected with the server's
// time, so every outbox entry that still lies in the future is given a
// fresh stamp, in the old order, and is sent again. The same stamps sit in
// the local rows (field clocks, OR-Set add tags) and in later remove deltas;
// they are rewritten along, so the device keeps agreeing with what it sends.
async function restampAhead(db: LocalDB) {
  await exclusive(async () => {
    const now = formatHLC(hlc.physical(), 99_999, '~');
    const rows = (await db.all<OutboxRow>(`SELECT * FROM outbox ORDER BY hlc ASC, id ASC`)).filter(
      (p) => !isHLC(p.hlc) || p.hlc > now
    );
    if (rows.length === 0) return;

    hlc.rebase();
    const fresh = new Map<string, string>(); // old stamp -> new stamp
    for (const p of rows) fresh.set(p.hlc, hlc.now());
    const swap = (tags: string[]) => tags.map((t) => fresh.get(t) ?? t);

    const stmts: Statement[] = [];
    const touched = new Map<string, { entity: string; id: string; row: RowState }>();
    for (const p of rows) {
      stmts.push({
        sql: `UPDATE outbox SET hlc = ?, payload = ? WHERE id = ?`,
        params: [fresh.get(p.hlc)!, swapRemovedTags(p, swap), p.id]
      });
      const key = `${p.entity}\n${p.entity_id}`;
      if (touched.has(key) || !(await columnsOf(db, p.entity))) continue;
      const row = await loadRow(db, p.entity, p.entity_id);
      if (!row.exists) continue;
      for (const [field, stamp] of Object.entries(row.clock)) {
        row.clock[field] = fresh.get(stamp) ?? stamp;
      }
      for (const set of Object.values(row.sets)) {
        for (const e of Object.values(set)) {
          e.a = swap(e.a);
          e.r = swap(e.r);
        }
      }
      touched.set(key, { entity: p.entity, id: p.entity_id, row });
    }
    for (const t of touched.values()) stmts.push(storeRowState(t.entity, t.id, t.row));
    await db.batch(stmts);
  });
}

// The entry's payload with the tags in its set removes passed through swap
// (a remove names the add stamps it observed, which may just have changed).
// Payloads without such tags are returned untouched.
function swapRemovedTags(p: OutboxRow, swap: (tags: string[]) => string[]): string {
  const setCols = setColsOf(p.entity);
  if (setCols.length === 0) return p.payload;
  let payload: Record<string, unknown>;
  try {
    payload = JSON.parse(p.payload);
  } catch {
    return p.payload;
  }
  let swapped = false;
  for (const c of setCols) {
    const d = parseSetDelta(payload?.[c]);
    if (!d?.removed_tags) continue;
    for (const e of Object.keys(d.removed_tags)) d.removed_tags[e] = swap(d.removed_tags[e]!);
    payload[c] = d;
    swapped = true;
  }
  return swapped ? JSON.stringify(payload) : p.payload;
}

// --- Pull: apply remote changes ---
export async function pull() {
  const db = await getDB();
  await repairLocal(db);
  let cursor = await getCursor();
  // A device without a position starts from the current rows, not from the
  // first change ever made. (A server without snapshots returns no cursor
  // and the log is read from the start, as before.)
  if (!cursor) cursor = await loadSnapshot(db);
  let redirected = false;

  for (;;) {
    const sentAt = Date.now();
    const res = await syncClient.pull({ cursor, limit: 200 });
    learnServerTime(res.serverTimeMs, sentAt);

    if (res.status === PullStatus.RESET_REQUIRED || res.status === PullStatus.SNAPSHOT_REQUIRED) {
      // Once per run: a server that also refuses the cursor of the snapshot
      // it just handed out would keep this loop going for ever.
      if (redirected) throw new Error('the server refused the cursor of its own snapshot');
      redirected = true;
      const reset = res.status === PullStatus.RESET_REQUIRED;
      if (reset) await requeueLocal(db, cursor);
      cursor = await loadSnapshot(db);
      if (!cursor) throw new Error('the server asked for a snapshot but offers none');
      if (reset) {
        await showResyncNotice();
        rerun = true; // upload what was queued in this run, not at the next tick
      }
      continue;
    }

    for (const ch of res.changes) await applyRemote(db, ch);
    const next = res.nextCursor || cursor;
    // A page that does not move the cursor cannot be followed by another.
    const more = res.hasMore && next !== cursor;
    cursor = next;
    await setMeta('cursor', cursor);
    // Notify open pages per applied batch, not once at the end: a large first
    // sync fills the UI progressively instead of after the whole download.
    if (res.changes.length > 0) bumpData();
    if (!more) break;
  }
  // This device now holds a full replica: an empty table is a real empty state.
  initialSyncPending.set(false);
}

// --- Snapshot: the current rows instead of the whole change log ---

// Loads the current rows of the workspace page by page, merges them into
// the local tables and stores the cursor Pull continues from. The cursor is
// stored only after the last page: an interrupted snapshot starts over,
// which is harmless because merging a row twice changes nothing. Local
// edits newer than the server's rows stay, and the outbox is not touched.
// Returns '' when the server has no snapshots (a release before 0.5).
async function loadSnapshot(db: LocalDB): Promise<string> {
  let pageToken = '';
  let cursor: string;
  do {
    const sentAt = Date.now();
    let res;
    try {
      res = await syncClient.snapshot({ pageToken });
    } catch (e) {
      if (pageToken === '' && e instanceof ConnectError && e.code === Code.Unimplemented) return '';
      throw e;
    }
    learnServerTime(res.serverTimeMs, sentAt);
    for (const row of res.rows) await applySnapshotRow(db, row);
    if (res.rows.length > 0) bumpData();
    cursor = res.cursor;
    pageToken = res.nextPageToken;
  } while (pageToken);
  if (cursor) await setMeta('cursor', cursor);
  return cursor;
}

const text = (v: unknown) => (typeof v === 'string' ? v : '');

const isObject = (v: unknown): v is Record<string, unknown> =>
  !!v && typeof v === 'object' && !Array.isArray(v);

function parseObject(json: string): Record<string, unknown> | null {
  try {
    const v: unknown = JSON.parse(json);
    return isObject(v) ? v : null;
  } catch {
    return null;
  }
}

// Merges one snapshot row into the local table, column by column (see
// mergeSnapshot). Like a pulled change, a row this device cannot make sense
// of is skipped.
async function applySnapshotRow(db: LocalDB, snap: SnapshotRow) {
  const cols = await columnsOf(db, snap.entity);
  if (!cols || !snap.entityId) return; // not a table this app syncs
  const payload = parseObject(snap.payloadJson);
  if (!payload) {
    console.warn('skipping snapshot row with an unreadable payload:', snap.entity, snap.entityId);
    return;
  }
  const stamps = parseFieldClock(snap.fieldHlcJson);
  const setCols = setColsOf(snap.entity);
  const fields: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(payload)) {
    if (k === 'id' || k === 'field_hlc' || !cols.has(k)) continue;
    if (setCols.includes(k)) {
      fields[k] = toORSet(v);
      continue;
    }
    const value = toScalar(v);
    if (value !== undefined) fields[k] = value;
  }

  await exclusive(async () => {
    const row = await loadRow(db, snap.entity, snap.entityId);
    const changed = mergeSnapshot(row, fields, stamps, setCols);
    await storeMerged(db, snap.entity, snap.entityId, row, changed, fields);
  });
  // Every later local stamp must sort after what this row carries.
  for (const stamp of Object.values(stamps)) hlc.recv(stamp);
}

// Writes the merged columns of a row and its field clock. Set columns are
// stored from the merged state in row.sets, scalars from fields.
async function storeMerged(
  db: LocalDB,
  entity: string,
  id: string,
  row: RowState & { exists: boolean },
  changed: string[],
  fields: Record<string, unknown>
) {
  const setCols = setColsOf(entity);
  const value = (c: string) => (setCols.includes(c) ? JSON.stringify(row.sets[c]) : fields[c]);
  if (!row.exists) {
    if (changed.length === 0) return;
    const names = ['id', ...changed, 'field_hlc'];
    await db.exec(
      `INSERT INTO ${entity} (${names.join(', ')}) VALUES (${names.map(() => '?').join(', ')})`,
      [id, ...changed.map(value), JSON.stringify(row.clock)]
    );
    return;
  }
  if (changed.length === 0) return; // everything stale
  await db.exec(
    `UPDATE ${entity} SET ${[...changed, 'field_hlc'].map((c) => `${c} = ?`).join(', ')}
     WHERE id = ?`,
    [...changed.map(value), JSON.stringify(row.clock), id]
  );
}

// --- Reset: the server is not the database this device synced with ---

// The server was restored from a backup or replaced, so it may lack changes
// this device has. Every local row is queued for upload again, as ordinary
// changes: one per row and stamp, carrying the columns last written with
// that stamp. The stamps are the existing ones, so where the server still
// has a change the merge does nothing, and where it lost one the value comes
// back. What already waits in the outbox stays as it is.
//
// staleCursor is the position the server refused. It is remembered once the
// queue is complete: if the snapshot that follows fails, the next run finds
// the same position refused again and must not queue everything twice.
async function requeueLocal(db: LocalDB, staleCursor: string) {
  if (staleCursor && (await getMeta('requeued_for')) === staleCursor) return;

  const key = (entity: string, id: string, stamp: string) => `${entity}\n${id}\n${stamp}`;
  const queued = new Set(
    (
      await db.all<{ entity: string; entity_id: string; hlc: string }>(
        `SELECT entity, entity_id, hlc FROM outbox
         UNION ALL SELECT entity, entity_id, hlc FROM outbox_failed`
      )
    ).map((o) => key(o.entity, o.entity_id, o.hlc))
  );
  const hiveApiary = new Map(
    (await db.all<{ id: string; apiary_id: string | null }>(`SELECT id, apiary_id FROM hive`)).map(
      (h) => [h.id, h.apiary_id ?? '']
    )
  );
  // When each apiary was first written, and the oldest of them.
  const born = new Map<string, string>();
  let oldest = '';
  for (const a of await db.all<{ id: string; field_hlc: string | null }>(
    `SELECT id, field_hlc FROM apiary`
  )) {
    const first = Object.values(parseFieldClock(a.field_hlc)).filter(isHLC).sort()[0] ?? '';
    born.set(a.id, first);
    if (!oldest || first < born.get(oldest)!) oldest = a.id;
  }
  // The scope a change is uploaded in. The server only checks that it is
  // one the user may write to (an apiary it knows, or the user's own), and
  // keeps personal tasks to their owner. Changes go up oldest first, so a
  // change older than the apiary its row is in now (a hive moved there
  // later) names the oldest apiary instead: the server may have lost the
  // newer one and would refuse the scope until that apiary arrives.
  const scopeOf = (entity: string, row: Record<string, unknown>, stamp: string) => {
    if (entity === 'apiary') return text(row.id);
    if (entity === 'task' && !text(row.apiary_id)) return `user:${userId()}`;
    const scope =
      text(row.scope_id) || text(row.apiary_id) || hiveApiary.get(text(row.hive_id)) || oldest;
    return (born.get(scope) ?? '') > stamp ? oldest : scope;
  };

  const PAGE = 200;
  for (const entity of SYNCED_TABLES) {
    const cols = await columnsOf(db, entity);
    if (!cols) continue;
    const setCols = setColsOf(entity);
    let after = '';
    for (;;) {
      const rows = await db.all<Record<string, unknown>>(
        `SELECT * FROM ${entity} WHERE id > ? ORDER BY id LIMIT ?`,
        [after, PAGE]
      );
      const stmts: Statement[] = [];
      for (const row of rows) {
        const id = text(row.id);
        for (const [stamp, payload] of rowChanges(row, cols, setCols)) {
          const scope = scopeOf(entity, row, stamp);
          // Without any apiary there is nowhere to upload a shared row to.
          if (!scope || queued.has(key(entity, id, stamp))) continue;
          stmts.push({
            sql: `INSERT INTO outbox (id, entity, entity_id, scope_id, op, payload, hlc, author_id)
                  VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
            params: [
              crypto.randomUUID(),
              entity,
              id,
              scope,
              JSON.stringify(payload),
              stamp,
              userId()
            ]
          });
        }
      }
      await db.batch(stmts);
      if (rows.length < PAGE) break;
      after = text(rows[rows.length - 1]!.id);
    }
  }
  if (staleCursor) await setMeta('requeued_for', staleCursor);
}

// The changes that reproduce a local row on the server: per stamp in the
// row's field clock, the columns that carry it with their current values.
// A set column contributes an add per element and tag, and one removal
// naming the tags it has tombstoned. Columns without a stamp were never
// written through sync and are left out.
//
// The oldest change carries every stamped column, not only its own: if the
// server no longer has the row, that change has to create it, and the
// server refuses a new row that lacks a required column (a name or date
// that was edited later would be missing). The later changes then set the
// columns they stamped. Where the server has the row, a column in the
// oldest change is older than or as old as what is stored and is ignored.
function rowChanges(
  row: Record<string, unknown>,
  cols: Set<string>,
  setCols: readonly string[]
): Map<string, Record<string, unknown>> {
  const out = new Map<string, Record<string, unknown>>();
  const at = (stamp: string) => {
    let payload = out.get(stamp);
    if (!payload) out.set(stamp, (payload = {}));
    return payload;
  };
  const scalars: Record<string, unknown> = {};
  for (const [col, stamp] of Object.entries(parseFieldClock(text(row.field_hlc)))) {
    if (!isHLC(stamp) || col === 'id' || !cols.has(col) || setCols.includes(col)) continue;
    const value = toScalar(row[col]);
    if (value !== undefined) scalars[col] = at(stamp)[col] = value;
  }
  for (const col of setCols) {
    const removed: Record<string, string[]> = {};
    let last = '';
    for (const [elem, e] of Object.entries(parseORSet(text(row[col])))) {
      for (const tag of e.a.filter(isHLC)) {
        const delta = (at(tag)[col] ??= { add: [] }) as SetDelta;
        (delta.add ??= []).push(elem);
        if (tag > last) last = tag;
      }
      if (e.r.length > 0) removed[elem] = e.r;
    }
    // A removal is not tied to a stamp of its own; it travels with the
    // newest add of the column.
    if (last && Object.keys(removed).length > 0) {
      const delta = at(last)[col] as SetDelta;
      delta.remove = Object.keys(removed);
      delta.removed_tags = removed;
    }
  }
  const oldest = [...out.keys()].sort()[0];
  if (oldest !== undefined) out.set(oldest, { ...scalars, ...out.get(oldest) });
  return out;
}

// --- Notice: this device was synchronised again from scratch ---

async function showResyncNotice() {
  await setMeta('resynced', '1');
  resyncNotice.set(true);
}

// The user has read the notice.
export async function dismissResyncNotice() {
  resyncNotice.set(false);
  await setMeta('resynced', '').catch(() => undefined);
}

// The merge state of a local row: field clock and set columns. entity must
// be a synced table.
async function loadRow(
  db: LocalDB,
  entity: string,
  id: string
): Promise<RowState & { exists: boolean }> {
  const setCols = setColsOf(entity);
  const cur = await db.get<Record<string, string | null>>(
    `SELECT ${['field_hlc', ...setCols].join(', ')} FROM ${entity} WHERE id = ?`,
    [id]
  );
  const sets: Record<string, ORSet> = {};
  for (const c of setCols) sets[c] = parseORSet(cur?.[c]);
  const clock: FieldClock = parseFieldClock(cur?.field_hlc);
  return { exists: !!cur, clock, sets };
}

function storeRowState(entity: string, id: string, row: RowState): Statement {
  const setCols = setColsOf(entity);
  return {
    sql: `UPDATE ${entity} SET ${['field_hlc', ...setCols].map((c) => `${c} = ?`).join(', ')}
          WHERE id = ?`,
    params: [
      JSON.stringify(row.clock),
      ...setCols.map((c) => JSON.stringify(row.sets[c] ?? {})),
      id
    ]
  };
}

// The fields of a remote change this device can store: only columns its own
// schema has (a newer server or app may send more; they are skipped), only
// scalar values, and well-formed set deltas. Payload keys never become part
// of a statement unless they are column names of the local table. Returns
// null when the payload is unusable as a whole.
function remoteFields(ch: Change, cols: Set<string>): Record<string, unknown> | null {
  if (ch.op === ChangeOp.DELETE) return tombstone();
  const raw = parseObject(ch.payloadJson);
  if (!raw) return null;
  const setCols = setColsOf(ch.entity);
  const fields: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(raw)) {
    if (k === 'id' || k === 'field_hlc' || !cols.has(k)) continue;
    if (setCols.includes(k)) {
      const d: SetDelta | null = parseSetDelta(v);
      if (d) fields[k] = d;
      continue;
    }
    const value = toScalar(v);
    if (value !== undefined) fields[k] = value;
  }
  return fields;
}

// Per-field LWW + OR-Set: only newer fields are applied, set columns are
// merged by union. A change this device cannot make sense of (unknown table,
// malformed stamp or payload) is skipped: it must not stop the changes
// behind it. Database errors are not swallowed; the pull is retried.
async function applyRemote(db: LocalDB, ch: Change) {
  const cols = await columnsOf(db, ch.entity);
  if (!cols) return; // not a table this app syncs
  if (!ch.entityId || !isHLC(ch.hlc)) {
    console.warn('skipping remote change without a usable id or stamp:', ch.entity, ch.hlc);
    return;
  }
  const fields = remoteFields(ch, cols);
  if (!fields) {
    console.warn('skipping remote change with an unreadable payload:', ch.entity, ch.entityId);
    return;
  }
  const setCols = setColsOf(ch.entity);

  // Read, merge and write as one step: a local write to the same row in
  // between would be overwritten together with its field stamps.
  await exclusive(async () => {
    const row = await loadRow(db, ch.entity, ch.entityId);
    const changed = mergeFields(row, fields, setCols, ch.hlc);
    await storeMerged(db, ch.entity, ch.entityId, row, changed, fields);
  });
  // A stamp the clock cannot follow (too far ahead) is still merged above;
  // it just does not drag this device's clock along.
  hlc.recv(ch.hlc);
}

// --- Refused changes ("sync problems") ---

export interface FailedChange {
  id: string;
  entity: string;
  entityId: string;
  op: number;
  code: string; // machine-readable cause, e.g. "wrong_tenant", "invalid"
  reason: string; // the server's plain-English explanation
  failedAt: string; // ISO timestamp
}

export async function listFailed(): Promise<FailedChange[]> {
  const db = await getDB();
  const rows = await db.all<Record<string, string | number>>(
    `SELECT id, entity, entity_id, op, code, reason, failed_at FROM outbox_failed
     ORDER BY failed_at DESC, hlc DESC`
  );
  return rows.map((r) => ({
    id: String(r.id),
    entity: String(r.entity),
    entityId: String(r.entity_id),
    op: Number(r.op),
    code: String(r.code ?? ''),
    reason: String(r.reason ?? ''),
    failedAt: String(r.failed_at ?? '')
  }));
}

// Puts one refused change (or, without an id, all of them) back into the
// outbox and syncs. The server decides again; what it still refuses returns
// to the list.
export async function retryFailed(id?: string) {
  const db = await getDB();
  const where = id === undefined ? '' : ' WHERE id = ?';
  const params = id === undefined ? [] : [id];
  await db.batch([
    {
      sql: `INSERT OR REPLACE INTO outbox (id, entity, entity_id, scope_id, op, payload, hlc, author_id)
            SELECT id, entity, entity_id, scope_id, op, payload, hlc, author_id FROM outbox_failed${where}`,
      params
    },
    { sql: `DELETE FROM outbox_failed${where}`, params }
  ]);
  await syncNow();
}

// Gives up on uploading one refused change (or all). The record itself
// stays on this device as it is; it just never reaches the server.
export async function discardFailed(id?: string) {
  const db = await getDB();
  if (id === undefined) await db.batch([{ sql: `DELETE FROM outbox_failed` }]);
  else await db.batch([{ sql: `DELETE FROM outbox_failed WHERE id = ?`, params: [id] }]);
  await refreshCounts();
}

// --- Orchestration ---
let running = false;
let rerun = false;
// Why sync is switched off: the server rejected the session, or it no
// longer talks to this version of the app. Asking again every few seconds
// cannot help; sync resumes on the next page load or syncNow().
let halted: 'signed_out' | 'update_required' | null = null;

const isUnauthenticated = (e: unknown) =>
  e instanceof ConnectError && e.code === Code.Unauthenticated;

// The server refuses this app version (see UpgradeRequired in sync.proto).
const isUpgradeRequired = (e: unknown) =>
  e instanceof ConnectError &&
  e.code === Code.FailedPrecondition &&
  e.findDetails(UpgradeRequiredSchema).length > 0;

// Failures that end the run and switch sync off, as opposed to the ones
// that are simply retried.
const haltReason = (e: unknown) =>
  isUnauthenticated(e) ? 'signed_out' : isUpgradeRequired(e) ? 'update_required' : null;

// Single-flight: the UI fires syncOnce() after every local write, so many calls
// can overlap. Running them concurrently would open competing SQLite write
// transactions on the server (-> "database is locked"). Serialize instead, and
// if new changes arrived while a run was in flight, loop once more so the latest
// outbox entries are pushed without waiting for the periodic timer.
export async function syncOnce() {
  // Another tab owns the database; this one shows a notice and waits.
  if (get(storageState) === 'elsewhere') return;
  await refreshCounts().catch(() => undefined);
  if (halted) return;
  if (!navigator.onLine) {
    // No pull is coming: what is stored locally is all there is right now, so
    // pages must show their real (possibly empty) state, not a syncing hint.
    initialSyncPending.set(false);
    setState('offline');
    return;
  }
  if (running) {
    rerun = true;
    return;
  }
  running = true;
  setState('syncing');
  let failure: unknown;
  try {
    do {
      rerun = false;
      // A failing push (offline blip, server error) must not stop the pull:
      // fresh remote data matters even while local changes wait in the outbox.
      try {
        await push();
      } catch (e) {
        if (haltReason(e)) throw e;
        console.warn('push deferred:', e);
        failure = e;
      }
      await pull();
    } while (rerun);
  } catch (e) {
    console.warn('sync deferred:', e);
    failure = e;
    // Give up the syncing hint; a later retry delivers data via bumpData.
    initialSyncPending.set(false);
  } finally {
    running = false;
  }

  await refreshCounts().catch(() => undefined);
  halted = haltReason(failure);
  if (halted) {
    setState(halted);
  } else if (failure !== undefined) {
    setState(navigator.onLine ? 'error' : 'offline');
  } else {
    const at = Date.now();
    status.update((s) => ({ ...s, state: 'idle', lastSyncedAt: at }));
    await setMeta('last_sync', String(at)).catch(() => undefined);
  }
}

// A sync the user asked for (status chip, retry): also tries again after
// the server had rejected the session or the app version.
export async function syncNow() {
  halted = null;
  await syncOnce();
}

let started = false;

export function startSync() {
  if (started) return;
  started = true;
  void (async () => {
    // A non-empty cursor proves this device has pulled before and holds a full
    // replica. Resolve the pending flag before the first network round-trip so
    // a refresh with a genuinely empty account shows its CTA immediately.
    try {
      if (await getCursor()) initialSyncPending.set(false);
      const last = Number(await getMeta('last_sync'));
      if (last > 0) status.update((s) => ({ ...s, lastSyncedAt: last }));
      if (await getMeta('resynced')) resyncNotice.set(true);
    } catch {
      /* flag resolves with the first sync instead */
    }
    await syncOnce();
  })();
  setInterval(syncOnce, 15_000); // periodic fallback
  window.addEventListener('online', () => void syncOnce()); // immediately on reconnect
  window.addEventListener('offline', () => void syncOnce()); // show "offline" at once
}
