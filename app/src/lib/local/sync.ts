// The sync engine. Runs in the background: pushes the outbox up (Push),
// pulls foreign changes (Pull) and applies them via last-writer-wins.
// Works offline (queued) and syncs automatically once connected.

import { ConnectError, Code } from '@connectrpc/connect';
import { getDB } from './db';
import { hlc } from './hlc';
import { parseFieldClock, accept, parseORSet, orAdd, orRemove, orRemoveTags } from './merge';
import { bumpData, initialSyncPending } from './live';
import { syncClient } from '$lib/client'; // generated Connect client (SyncService)

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

async function getCursor(): Promise<string> {
  const db = await getDB();
  const r = await db.get<{ v: string }>(`SELECT v FROM sync_meta WHERE k = 'cursor'`);
  return r?.v ?? '';
}
async function setCursor(c: string) {
  const db = await getDB();
  await db.exec(
    `INSERT INTO sync_meta (k, v) VALUES ('cursor', ?)
     ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
    [c]
  );
}

// --- Push: local outbox to the server ---
export async function push() {
  const db = await getDB();
  const pending = await db.all<any>(`SELECT * FROM outbox ORDER BY hlc ASC LIMIT 200`);
  if (pending.length === 0) return;

  const changes = pending.map((p) => ({
    entity: p.entity,
    entityId: p.entity_id,
    scopeId: p.scope_id,
    op: p.op,
    payloadJson: p.payload,
    hlc: p.hlc,
    authorId: p.author_id
  }));

  let res;
  try {
    res = await syncClient.push({ changes });
  } catch (e) {
    // Permission denied is permanent for this batch (read-only demo session,
    // unwritable scope): retrying every 15s can never succeed and would keep
    // the outbox full forever. Drop the entries — the data itself stays in
    // the local tables, only the (rejected) upload intent is discarded.
    if (e instanceof ConnectError && e.code === Code.PermissionDenied) {
      console.warn('push rejected as read-only — keeping changes locally:', e.message);
      for (const p of pending) {
        await db.exec(`DELETE FROM outbox WHERE id = ?`, [p.id]);
      }
      return;
    }
    throw e; // transient (offline, 5xx): keep the outbox and retry later
  }

  // Remove successfully transmitted entries from the outbox.
  for (const p of pending) {
    await db.exec(`DELETE FROM outbox WHERE id = ?`, [p.id]);
  }
  // Conflicts: the server has a newer version -> it comes back via Pull anyway.
  if (res.serverCursor) await setCursor(res.serverCursor);
}

// --- Pull: apply remote changes ---
export async function pull() {
  await getDB();
  let cursor = await getCursor();
  let hasMore = true;

  while (hasMore) {
    const res = await syncClient.pull({ cursor, limit: 200 });

    for (const ch of res.changes) {
      hlc.recv(ch.hlc);
      await applyRemote(ch);
    }
    cursor = res.nextCursor;
    await setCursor(cursor);
    hasMore = res.hasMore;
    // Notify open pages per applied batch, not once at the end: a large first
    // sync fills the UI progressively instead of after the whole download.
    if (res.changes.length > 0) bumpData();
  }
  // This device now holds a full replica — an empty table is a real empty state.
  initialSyncPending.set(false);
}

// Per-field LWW + OR-Set: only newer fields are applied, set columns are
// merged by union.
async function applyRemote(ch: any) {
  if (!SYNCED_TABLES.includes(ch.entity)) return;
  const db = await getDB();
  const cur = await db.get<any>(`SELECT * FROM ${ch.entity} WHERE id = ?`, [ch.entityId]);
  const fields = ch.op === 2 /* delete */ ? { deleted: 1 } : JSON.parse(ch.payloadJson);
  const setCols = SET_COLS[ch.entity] ?? [];

  if (!cur) {
    const fc: Record<string, string> = {};
    const cols = ['id'];
    const vals: unknown[] = [ch.entityId];
    for (const [k, v] of Object.entries(fields) as [string, any][]) {
      if (k === 'id') continue;
      if (setCols.includes(k)) {
        const os = {};
        for (const e of v?.add ?? []) orAdd(os, e, ch.hlc);
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
  for (const [k, v] of Object.entries(fields) as [string, any][]) {
    if (k === 'id' || k === 'field_hlc') continue;
    if (setCols.includes(k)) {
      const os = parseORSet(cur[k]);
      for (const e of v?.add ?? []) orAdd(os, e, ch.hlc);
      for (const e of v?.remove ?? []) {
        // Newer deltas say which tags the origin observed; only those are
        // tombstoned (add-wins). Old deltas without tags remove all local ones.
        const tags = v?.removed_tags?.[e];
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

// --- Orchestration ---
let running = false;
let rerun = false;

// Single-flight: the UI fires syncOnce() after every local write, so many calls
// can overlap. Running them concurrently would open competing SQLite write
// transactions on the server (-> "database is locked"). Serialize instead, and
// if new changes arrived while a run was in flight, loop once more so the latest
// outbox entries are pushed without waiting for the periodic timer.
export async function syncOnce() {
  if (!navigator.onLine) {
    // No pull is coming: what is stored locally is all there is right now, so
    // pages must show their real (possibly empty) state, not a syncing hint.
    initialSyncPending.set(false);
    return;
  }
  if (running) {
    rerun = true;
    return;
  }
  running = true;
  try {
    do {
      rerun = false;
      // A failing push (offline blip, server error) must not stop the pull:
      // fresh remote data matters even while local changes wait in the outbox.
      try {
        await push();
      } catch (e) {
        console.warn('push deferred:', e);
      }
      await pull();
    } while (rerun);
  } catch (e) {
    console.warn('sync deferred:', e);
    // Give up the syncing hint — a later retry delivers data via bumpData.
    initialSyncPending.set(false);
  } finally {
    running = false;
  }
}

export function startSync() {
  void (async () => {
    // A non-empty cursor proves this device has pulled before and holds a full
    // replica. Resolve the pending flag before the first network round-trip so
    // a refresh with a genuinely empty account shows its CTA immediately.
    try {
      if (await getCursor()) initialSyncPending.set(false);
    } catch {
      /* flag resolves with the first sync instead */
    }
    await syncOnce();
  })();
  setInterval(syncOnce, 15_000); // periodic fallback
  window.addEventListener('online', () => void syncOnce()); // immediately on reconnect
}
