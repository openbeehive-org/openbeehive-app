// The sync engine. Runs in the background: pushes the outbox up (Push),
// pulls foreign changes (Pull) and applies them via last-writer-wins.
// Works offline (queued) and syncs automatically once connected.

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
  tombstone,
  type FieldClock,
  type ORSet,
  type RowState,
  type SetDelta
} from './merge';
import { columnsOf, setColsOf, toScalar } from './tables';
import { bumpData, initialSyncPending, storageState } from './live';
import { syncClient } from '#lib/client.js'; // generated Connect client (SyncService)
import { ChangeOp, UpgradeRequiredSchema, type Change } from '../proto/openbeehive/v1/sync_pb';

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

// Version of the pull logic this device's cursor was built by. Raising it
// makes every device pull the change log once more from the start (merging
// is idempotent, so that only fills gaps).
//   1: the cursor used to be overwritten with a counter returned by Push,
//      so a device that pushed skipped what others had written before.
const SYNC_EPOCH = 1;

async function repairCursor(db: LocalDB) {
  if (Number((await getMeta('sync_epoch')) ?? 0) >= SYNC_EPOCH) return;
  await db.batch([setMetaStmt('cursor', ''), setMetaStmt('sync_epoch', String(SYNC_EPOCH))]);
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
  await repairCursor(db);
  let cursor = await getCursor();
  let hasMore = true;

  while (hasMore) {
    const sentAt = Date.now();
    const res = await syncClient.pull({ cursor, limit: 200 });
    learnServerTime(res.serverTimeMs, sentAt);

    for (const ch of res.changes) await applyRemote(db, ch);
    // A page that does not move the cursor cannot be followed by another.
    hasMore = res.hasMore && res.nextCursor !== cursor;
    cursor = res.nextCursor;
    await setMeta('cursor', cursor);
    // Notify open pages per applied batch, not once at the end: a large first
    // sync fills the UI progressively instead of after the whole download.
    if (res.changes.length > 0) bumpData();
  }
  // This device now holds a full replica: an empty table is a real empty state.
  initialSyncPending.set(false);
}

// The merge state of a local row: field clock and set columns. entity must
// be a synced table.
async function loadRow(db: LocalDB, entity: string, id: string) {
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
  let raw: unknown;
  try {
    raw = JSON.parse(ch.payloadJson);
  } catch {
    return null;
  }
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
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
    const value = (c: string) => (setCols.includes(c) ? JSON.stringify(row.sets[c]) : fields[c]);

    if (!row.exists) {
      if (changed.length === 0) return;
      const names = ['id', ...changed, 'field_hlc'];
      await db.exec(
        `INSERT INTO ${ch.entity} (${names.join(', ')}) VALUES (${names.map(() => '?').join(', ')})`,
        [ch.entityId, ...changed.map(value), JSON.stringify(row.clock)]
      );
      return;
    }
    if (changed.length === 0) return; // everything stale
    await db.exec(
      `UPDATE ${ch.entity} SET ${[...changed, 'field_hlc'].map((c) => `${c} = ?`).join(', ')}
       WHERE id = ?`,
      [...changed.map(value), JSON.stringify(row.clock), ch.entityId]
    );
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
    } catch {
      /* flag resolves with the first sync instead */
    }
    await syncOnce();
  })();
  setInterval(syncOnce, 15_000); // periodic fallback
  window.addEventListener('online', () => void syncOnce()); // immediately on reconnect
  window.addEventListener('offline', () => void syncOnce()); // show "offline" at once
}
