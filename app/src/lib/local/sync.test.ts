import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { ConnectError, Code } from '@connectrpc/connect';
import { makeLocalDB, setCurrentDB, getCurrentDB } from '../../tests/localdb';

vi.mock('./db', async () => {
  const { getCurrentDB } = await import('../../tests/localdb');
  return { getDB: async () => getCurrentDB() };
});

const pushMock = vi.fn();
const pullMock = vi.fn();
vi.mock('#lib/client.js', () => ({
  syncClient: {
    push: (...a: unknown[]) => pushMock(...a),
    pull: (...a: unknown[]) => pullMock(...a)
  }
}));

import {
  push,
  pull,
  syncOnce,
  syncNow,
  syncStatus,
  unsentCount,
  listFailed,
  retryFailed,
  discardFailed
} from './sync';
import { patch, setAdd, setRemove } from './repo';
import { hlc, formatHLC, parseHLC, MAX_DRIFT_MS } from './hlc';
import { parseFieldClock, parseORSet, orValues } from './merge';
import { storageState } from './live';

const t = (ms: number, node = 'remote') => `${String(ms).padStart(15, '0')}:00000:${node}`;

async function queueOutbox(entity: string, entityId: string, payload: unknown, stamp: string) {
  await getCurrentDB().exec(
    `INSERT INTO outbox (id, entity, entity_id, scope_id, op, payload, hlc, author_id)
     VALUES (?, ?, ?, ?, 1, ?, ?, 'u1')`,
    [crypto.randomUUID(), entity, entityId, entityId, JSON.stringify(payload), stamp]
  );
}

const outbox = () => getCurrentDB().all<any>(`SELECT * FROM outbox ORDER BY hlc ASC`);
const meta = async (k: string) =>
  (await getCurrentDB().get<{ v: string }>(`SELECT v FROM sync_meta WHERE k = ?`, [k]))?.v;
const emptyPull = { changes: [], nextCursor: '', hasMore: false };

// Every local write nudges a background sync. Tests that write through the
// repo and then drive push/pull themselves keep that run from starting (a
// tab that does not own the database never syncs).
async function withoutBackgroundSync<T>(fn: () => Promise<T>): Promise<T> {
  storageState.set('elsewhere');
  try {
    return await fn();
  } finally {
    storageState.set('ok');
  }
}

beforeEach(async () => {
  setCurrentDB(await makeLocalDB());
  localStorage.clear();
  localStorage.setItem('obh.orgId', 'org1');
  localStorage.setItem('obh.userId', 'u1');
  storageState.set('ok');
  hlc.syncTo(Date.now(), Date.now()); // trust the device clock again
  hlc.rebase();
  pushMock.mockReset();
  pullMock.mockReset();
  pullMock.mockResolvedValue(emptyPull);
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('push', () => {
  // The server once answered a push with its global change counter and the
  // device stored it as its pull cursor, skipping whatever other devices had
  // written in between. Only pull may move the cursor.
  it('clears the outbox and leaves the pull cursor alone', async () => {
    await getCurrentDB().exec(`INSERT INTO sync_meta (k, v) VALUES ('cursor', '7')`);
    await queueOutbox('apiary', 'a1', { name: 'X' }, t(1, 'local'));
    pushMock.mockResolvedValue({ serverCursor: '42' });

    await push();

    expect(pushMock).toHaveBeenCalledOnce();
    expect(await outbox()).toHaveLength(0);
    expect(await meta('cursor')).toBe('7');
  });

  it('keeps the outbox on transient errors and rethrows', async () => {
    await queueOutbox('apiary', 'a1', { name: 'X' }, t(1, 'local'));
    pushMock.mockRejectedValue(new ConnectError('boom', Code.Unavailable));

    await expect(push()).rejects.toThrow();
    expect(await outbox()).toHaveLength(1);
    expect(await listFailed()).toHaveLength(0);
  });

  it('drops the batch when the session is read-only (demo, read-only key)', async () => {
    await queueOutbox('apiary', 'a1', { name: 'X' }, t(1, 'local'));
    pushMock.mockRejectedValue(new ConnectError('read-only', Code.PermissionDenied));

    await push(); // must not throw

    expect(await outbox()).toHaveLength(0);
    // The local row itself is untouched; only the upload intent is dropped,
    // and a read-only session has nothing to fix in a problems list.
    expect(await listFailed()).toHaveLength(0);
  });

  it('does nothing when the outbox is empty', async () => {
    await push();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it('removes accepted entries and parks refused ones with code and reason', async () => {
    await queueOutbox('apiary', 'a1', { name: 'ok' }, t(1, 'local'));
    await queueOutbox('hive', 'h1', { name: 'foreign' }, t(2, 'local'));
    await queueOutbox('apiary', 'a2', { name: 'ok too' }, t(3, 'local'));
    pushMock.mockResolvedValue({
      rejected: [{ index: 1, code: 'wrong_tenant', reason: "change targets another tenant's data" }]
    });

    await push();

    expect(pushMock).toHaveBeenCalledOnce();
    expect(await outbox()).toHaveLength(0);
    const failed = await listFailed();
    expect(failed).toHaveLength(1);
    expect(failed[0]).toMatchObject({
      entity: 'hive',
      entityId: 'h1',
      code: 'wrong_tenant',
      reason: "change targets another tenant's data"
    });
    expect(failed[0]!.failedAt).toMatch(/^\d{4}-\d{2}-\d{2}T/);
    expect(await unsentCount()).toBe(1);
  });

  it('a refused change can be retried or discarded', async () => {
    await queueOutbox('hive', 'h1', { name: 'A' }, t(1, 'local'));
    await queueOutbox('hive', 'h2', { name: 'B' }, t(2, 'local'));
    pushMock.mockResolvedValueOnce({
      rejected: [
        { index: 0, code: 'invalid', reason: 'r' },
        { index: 1, code: 'forbidden_scope', reason: 'r' }
      ]
    });
    await push();
    const [first, second] = (await listFailed()).sort((a, b) =>
      a.entityId.localeCompare(b.entityId)
    );

    await discardFailed(second!.id);
    expect((await listFailed()).map((f) => f.entityId)).toEqual(['h1']);
    expect(get(syncStatus).failed).toBe(1);

    // Retry: back into the outbox with its payload and stamp, and sent again.
    pushMock.mockResolvedValueOnce({});
    await retryFailed(first!.id);
    expect(pushMock).toHaveBeenCalledTimes(2);
    expect(pushMock.mock.calls[1]![0].changes).toMatchObject([
      { entity: 'hive', entityId: 'h1', payloadJson: '{"name":"A"}', hlc: t(1, 'local') }
    ]);
    expect(await listFailed()).toHaveLength(0);
    expect(await outbox()).toHaveLength(0);
  });

  it('retrying everything moves all refused changes back at once', async () => {
    await queueOutbox('hive', 'h1', { name: 'A' }, t(1, 'local'));
    await queueOutbox('hive', 'h2', { name: 'B' }, t(2, 'local'));
    pushMock.mockResolvedValueOnce({
      rejected: [
        { index: 0, code: 'invalid', reason: 'r' },
        { index: 1, code: 'invalid', reason: 'r' }
      ]
    });
    await push();
    pushMock.mockResolvedValueOnce({ rejected: [{ index: 0, code: 'invalid', reason: 'again' }] });

    await retryFailed();

    expect(pushMock.mock.calls[1]![0].changes).toHaveLength(2);
    const failed = await listFailed();
    expect(failed.map((f) => [f.entityId, f.reason])).toEqual([['h1', 'again']]);
    await discardFailed();
    expect(await unsentCount()).toBe(0);
  });

  it('splits uploads by size so one request stays far below the server limit', async () => {
    const big = 'x'.repeat(1_500_000);
    for (let i = 1; i <= 5; i++) await queueOutbox('hive', `h${i}`, { photo: big }, t(i, 'local'));
    pushMock.mockResolvedValue({});

    await push();

    const sizes = pushMock.mock.calls.map((c) =>
      c[0].changes.reduce((n: number, ch: any) => n + ch.payloadJson.length, 0)
    );
    expect(sizes.length).toBeGreaterThan(1);
    for (const n of sizes) expect(n).toBeLessThanOrEqual(4 * 1024 * 1024);
    // Nothing lost, nothing sent twice, order kept.
    const sent = pushMock.mock.calls.flatMap((c) => c[0].changes.map((ch: any) => ch.entityId));
    expect(sent).toEqual(['h1', 'h2', 'h3', 'h4', 'h5']);
    expect(await outbox()).toHaveLength(0);
  });

  it('sends at most 200 changes per request', async () => {
    for (let i = 0; i < 450; i++)
      await queueOutbox('task', `t${i}`, { done: 1 }, t(i + 1, 'local'));
    pushMock.mockResolvedValue({});
    await push();
    expect(pushMock.mock.calls.map((c) => c[0].changes.length)).toEqual([200, 200, 50]);
  });

  it('a change larger than the budget still goes out, alone', async () => {
    await queueOutbox('hive', 'h1', { photo: 'x'.repeat(5 * 1024 * 1024) }, t(1, 'local'));
    await queueOutbox('hive', 'h2', { name: 'small' }, t(2, 'local'));
    pushMock.mockResolvedValue({});
    await push();
    expect(pushMock.mock.calls.map((c) => c[0].changes.length)).toEqual([1, 1]);
  });

  it('isolates the change a refused request chokes on and parks only that one', async () => {
    for (let i = 1; i <= 4; i++)
      await queueOutbox('hive', `h${i}`, { name: `n${i}` }, t(i, 'local'));
    // The server refuses every request that contains h3.
    pushMock.mockImplementation(async (req: any) => {
      if (req.changes.some((c: any) => c.entityId === 'h3')) {
        throw new ConnectError('message too large', Code.ResourceExhausted);
      }
      return {};
    });

    await push();

    expect(await outbox()).toHaveLength(0);
    const failed = await listFailed();
    expect(failed.map((f) => [f.entityId, f.code])).toEqual([['h3', 'too_large']]);
  });
});

describe('clock correction', () => {
  it('re-stamps changes the server refused as stamped ahead, and sends them again', async () => {
    // This device's clock runs 2 hours ahead while it writes offline.
    const realNow = Date.now();
    vi.spyOn(Date, 'now').mockImplementation(() => realNow + 2 * 3_600_000);
    hlc.rebase();
    await withoutBackgroundSync(async () => {
      await patch('apiary', 'a1', 'a1', { organization_id: 'org1', name: 'Early', deleted: 0 });
      await patch('apiary', 'a1', 'a1', { name: 'Early bird' });
      await patch('inspection', 'i1', 'a1', { organization_id: 'org1', hive_id: 'h1', deleted: 0 });
      await setAdd('inspection', 'i1', 'a1', 'photo_keys', 'p1');
      await setRemove('inspection', 'i1', 'a1', 'photo_keys', 'p1');
    });
    const before = await outbox();
    expect(before).toHaveLength(5);

    // The server knows the real time and refuses stamps >10 min ahead.
    const seen: any[][] = [];
    pushMock.mockImplementation(async (req: any) => {
      seen.push(req.changes);
      const rejected = req.changes
        .map((c: any, index: number) => ({ index, ms: parseHLC(c.hlc)!.ms }))
        .filter((c: any) => c.ms > realNow + MAX_DRIFT_MS)
        .map((c: any) => ({ index: c.index, code: 'clock_ahead', reason: 'ahead' }));
      return { rejected, serverTimeMs: BigInt(realNow) };
    });

    await push();

    // Second request: same changes, same order, stamps the server accepts.
    expect(seen).toHaveLength(2);
    expect(seen[1]!.map((c) => [c.entity, c.entityId])).toEqual(
      before.map((p) => [p.entity, p.entity_id])
    );
    const stamps = seen[1]!.map((c) => c.hlc as string);
    expect([...stamps].sort()).toEqual(stamps);
    for (const s of stamps) expect(parseHLC(s)!.ms).toBeLessThanOrEqual(realNow + 1000);
    expect(await outbox()).toHaveLength(0);
    expect(await listFailed()).toHaveLength(0); // re-stamped, not parked

    // The device agrees with what it sent: field clocks and OR-Set tags
    // carry the new stamps, and the remove names the add's new tag.
    const row = await getCurrentDB().get<any>(`SELECT * FROM apiary WHERE id = 'a1'`);
    const fc = parseFieldClock(row.field_hlc);
    expect(fc.organization_id).toBe(stamps[0]);
    expect(fc.name).toBe(stamps[1]);
    const insp = await getCurrentDB().get<any>(`SELECT * FROM inspection WHERE id = 'i1'`);
    expect(parseORSet(insp.photo_keys).p1).toEqual({ a: [stamps[3]], r: [stamps[3]] });
    expect(JSON.parse(seen[1]![4].payloadJson).photo_keys.removed_tags.p1).toEqual([stamps[3]]);

    // And new writes are stamped with the corrected clock from now on.
    expect(hlc.skewMs).toBeLessThan(-3_500_000);
    expect(parseHLC(hlc.now())!.ms).toBeLessThanOrEqual(realNow + 1000);
  });

  it('parks a change that is refused as ahead even after re-stamping', async () => {
    await queueOutbox('apiary', 'a1', { name: 'X' }, formatHLC(Date.now() + 3_600_000, 0, 'local'));
    pushMock.mockResolvedValue({
      rejected: [{ index: 0, code: 'clock_ahead', reason: 'ahead' }],
      serverTimeMs: BigInt(Date.now())
    });

    await push();

    expect(pushMock).toHaveBeenCalledTimes(2); // original, re-stamped; then it stops
    expect(await outbox()).toHaveLength(0);
    expect((await listFailed()).map((f) => f.code)).toEqual(['clock_ahead']);
  });

  it('parks instead of re-stamping when the server did not say what time it is', async () => {
    await queueOutbox('apiary', 'a1', { name: 'X' }, formatHLC(Date.now() + 3_600_000, 0, 'local'));
    pushMock.mockResolvedValue({ rejected: [{ index: 0, code: 'clock_ahead', reason: 'ahead' }] });
    await push();
    expect(pushMock).toHaveBeenCalledOnce();
    expect(await listFailed()).toHaveLength(1);
  });

  it('learns the server time from pull as well', async () => {
    pullMock.mockResolvedValueOnce({ ...emptyPull, serverTimeMs: BigInt(Date.now() - 600_000) });
    await pull();
    expect(Math.round(hlc.skewMs / 60_000)).toBe(-10);
    expect(hlc.physical()).toBeLessThan(Date.now() - 500_000);
  });
});

describe('pull / applyRemote', () => {
  it('inserts an unknown row with per-field clocks from the change stamp', async () => {
    pullMock.mockResolvedValueOnce({
      changes: [
        {
          entity: 'apiary',
          entityId: 'a1',
          op: 1,
          hlc: t(5),
          payloadJson: JSON.stringify({ organization_id: 'o1', name: 'Remote', deleted: 0 })
        }
      ],
      nextCursor: 'c1',
      hasMore: false
    });

    await pull();

    const row = await getCurrentDB().get<any>(`SELECT * FROM apiary WHERE id = 'a1'`);
    expect(row).toMatchObject({ name: 'Remote', deleted: 0 });
    expect(parseFieldClock(row!.field_hlc).name).toBe(t(5));
    expect(await meta('cursor')).toBe('c1');
  });

  it('applies only newer fields (per-field LWW), keeps newer local ones', async () => {
    // Local row: name stamped at 10, note at 1.
    await getCurrentDB().exec(
      `INSERT INTO apiary (id, name, note, deleted, field_hlc) VALUES ('a1', 'LocalName', 'old', 0, ?)`,
      [JSON.stringify({ name: t(10, 'local'), note: t(1, 'local') })]
    );

    pullMock.mockResolvedValueOnce({
      changes: [
        {
          entity: 'apiary',
          entityId: 'a1',
          op: 1,
          hlc: t(5),
          payloadJson: JSON.stringify({ name: 'RemoteName', note: 'new' })
        }
      ],
      nextCursor: '',
      hasMore: false
    });

    await pull();

    const row = await getCurrentDB().get<any>(`SELECT * FROM apiary WHERE id = 'a1'`);
    expect(row!.name).toBe('LocalName'); // local stamp 10 beats remote 5
    expect(row!.note).toBe('new'); // remote 5 beats local 1
  });

  it('a remote delete tombstones the row', async () => {
    await getCurrentDB().exec(
      `INSERT INTO apiary (id, name, deleted, field_hlc) VALUES ('a1', 'X', 0, '{}')`
    );
    pullMock.mockResolvedValueOnce({
      changes: [{ entity: 'apiary', entityId: 'a1', op: 2, hlc: t(9), payloadJson: '' }],
      nextCursor: '',
      hasMore: false
    });

    await pull();
    const row = await getCurrentDB().get<any>(`SELECT deleted FROM apiary WHERE id = 'a1'`);
    expect(row!.deleted).toBe(1);
  });

  it('merges OR-Set deltas instead of overwriting', async () => {
    await getCurrentDB().exec(
      `INSERT INTO inspection (id, photo_keys, deleted, field_hlc) VALUES ('i1', ?, 0, '{}')`,
      [JSON.stringify({ p1: { a: [t(1, 'local')], r: [] } })]
    );

    pullMock.mockResolvedValueOnce({
      changes: [
        {
          entity: 'inspection',
          entityId: 'i1',
          op: 1,
          hlc: t(6),
          payloadJson: JSON.stringify({ photo_keys: { add: ['p2'], remove: [] } })
        }
      ],
      nextCursor: '',
      hasMore: false
    });

    await pull();
    const row = await getCurrentDB().get<any>(`SELECT photo_keys FROM inspection WHERE id = 'i1'`);
    expect(orValues(parseORSet(row!.photo_keys)).sort()).toEqual(['p1', 'p2']);
  });

  // The server and the app must read removed_tags the same way: an empty
  // list means "observed nothing", only a delta without the field is the
  // old remove-everything form.
  it('an empty removed_tags list removes nothing; a missing one removes all', async () => {
    const seed = JSON.stringify({
      p1: { a: [t(1, 'local')], r: [] },
      p2: { a: [t(1, 'x')], r: [] }
    });
    await getCurrentDB().exec(
      `INSERT INTO inspection (id, photo_keys, deleted, field_hlc) VALUES ('i1', ?, 0, '{}')`,
      [seed]
    );
    pullMock.mockResolvedValueOnce({
      changes: [
        {
          entity: 'inspection',
          entityId: 'i1',
          op: 1,
          hlc: t(6),
          payloadJson: JSON.stringify({ photo_keys: { remove: ['p1'], removed_tags: { p1: [] } } })
        },
        {
          entity: 'inspection',
          entityId: 'i1',
          op: 1,
          hlc: t(7),
          payloadJson: JSON.stringify({ photo_keys: { remove: ['p2'] } })
        }
      ],
      nextCursor: '',
      hasMore: false
    });
    await pull();
    const row = await getCurrentDB().get<any>(`SELECT photo_keys FROM inspection WHERE id = 'i1'`);
    expect(orValues(parseORSet(row!.photo_keys))).toEqual(['p1']);
  });

  it('ignores changes for unknown entities', async () => {
    pullMock.mockResolvedValueOnce({
      changes: [
        {
          entity: 'nonsense; DROP TABLE apiary',
          entityId: 'x',
          op: 1,
          hlc: t(1),
          payloadJson: '{}'
        },
        { entity: 'outbox', entityId: 'x', op: 1, hlc: t(1), payloadJson: '{"entity":"apiary"}' },
        { entity: 'sync_meta', entityId: 'x', op: 2, hlc: t(1), payloadJson: '' }
      ],
      nextCursor: '',
      hasMore: false
    });
    await pull(); // must not throw, must not touch tables
    expect(await getCurrentDB().all(`SELECT * FROM apiary`)).toHaveLength(0);
    expect(await outbox()).toHaveLength(0);
  });

  // Payload keys used to be pasted into the statement as column names: one
  // member could run SQL in every teammate's local database, or stop their
  // sync for good with a key that is not a column.
  it('never uses a payload key as SQL: hostile and unknown keys are dropped', async () => {
    await getCurrentDB().exec(
      `INSERT INTO apiary (id, name, note, deleted, field_hlc) VALUES ('keep', 'Keep', 'n', 0, '{}')`
    );
    const hostile = {
      name: 'Renamed',
      "note = 'owned' WHERE 1=1; DELETE FROM apiary; --": 1,
      'name) VALUES (1); DROP TABLE hive; --': 1,
      column_of_a_newer_app: 'ignored',
      lat: { nested: true },
      lng: [1, 2]
    };
    pullMock.mockResolvedValueOnce({
      changes: [
        {
          entity: 'apiary',
          entityId: 'new',
          op: 1,
          hlc: t(5),
          payloadJson: JSON.stringify(hostile)
        },
        {
          entity: 'apiary',
          entityId: 'keep',
          op: 1,
          hlc: t(6),
          payloadJson: JSON.stringify(hostile)
        },
        { entity: 'apiary', entityId: 'after', op: 1, hlc: t(7), payloadJson: '{"name":"After"}' }
      ],
      nextCursor: 'c9',
      hasMore: false
    });

    await pull(); // must not throw: the changes behind it still arrive

    const rows = await getCurrentDB().all<any>(
      `SELECT id, name, note, lat FROM apiary ORDER BY id`
    );
    expect(rows).toEqual([
      { id: 'after', name: 'After', note: null, lat: null },
      { id: 'keep', name: 'Renamed', note: 'n', lat: null },
      { id: 'new', name: 'Renamed', note: null, lat: null }
    ]);
    expect(
      await getCurrentDB().all(`SELECT name FROM sqlite_master WHERE name = 'hive'`)
    ).toHaveLength(1);
    expect(await meta('cursor')).toBe('c9');
  });

  it('skips changes with a malformed stamp or payload and keeps going', async () => {
    const ahead = formatHLC(Date.now() + 86_400_000, 0, 'remote');
    pullMock.mockResolvedValueOnce({
      changes: [
        { entity: 'apiary', entityId: 'bad1', op: 1, hlc: 'zzzz', payloadJson: '{"name":"x"}' },
        { entity: 'apiary', entityId: 'bad2', op: 1, hlc: t(5), payloadJson: '{not json' },
        { entity: 'apiary', entityId: 'bad3', op: 1, hlc: t(5), payloadJson: '[1]' },
        { entity: 'apiary', entityId: '', op: 1, hlc: t(5), payloadJson: '{"name":"x"}' },
        { entity: 'apiary', entityId: 'far', op: 1, hlc: ahead, payloadJson: '{"name":"Far"}' },
        { entity: 'apiary', entityId: 'ok', op: 1, hlc: t(6), payloadJson: '{"name":"Ok"}' }
      ],
      nextCursor: 'c1',
      hasMore: false
    });

    await pull();

    const ids = (await getCurrentDB().all<any>(`SELECT id FROM apiary ORDER BY id`)).map(
      (r) => r.id
    );
    expect(ids).toEqual(['far', 'ok']);
    // A stamp from a broken clock is merged, but this device's clock does
    // not follow it: its own next stamp stays near real time.
    expect(parseHLC(hlc.now())!.ms).toBeLessThan(Date.now() + 60_000);
  });

  it('follows hasMore pagination until done', async () => {
    pullMock
      .mockResolvedValueOnce({
        changes: [
          {
            entity: 'apiary',
            entityId: 'a1',
            op: 1,
            hlc: t(1),
            payloadJson: '{"name":"1","deleted":0}'
          }
        ],
        nextCursor: 'c1',
        hasMore: true
      })
      .mockResolvedValueOnce({
        changes: [
          {
            entity: 'apiary',
            entityId: 'a2',
            op: 1,
            hlc: t(2),
            payloadJson: '{"name":"2","deleted":0}'
          }
        ],
        nextCursor: 'c2',
        hasMore: false
      });

    await pull();
    expect(pullMock).toHaveBeenCalledTimes(2);
    expect(pullMock.mock.calls[1]![0]).toMatchObject({ cursor: 'c1' });
    expect(await getCurrentDB().all(`SELECT * FROM apiary`)).toHaveLength(2);
    expect(await meta('cursor')).toBe('c2');
  });

  it('stops when the server claims more pages without moving the cursor', async () => {
    pullMock.mockResolvedValue({ changes: [], nextCursor: '', hasMore: true });
    await pull();
    expect(pullMock).toHaveBeenCalledOnce();
  });

  // Devices that ran the cursor bug have gaps. The first pull of a fixed app
  // starts over once; merging is idempotent, so that only fills the gaps.
  it('re-pulls from the start once on a device with a cursor from before the fix', async () => {
    await getCurrentDB().exec(`INSERT INTO sync_meta (k, v) VALUES ('cursor', '42')`);
    pullMock.mockResolvedValue({ changes: [], nextCursor: '50', hasMore: false });

    await pull();
    expect(pullMock.mock.calls[0]![0]).toMatchObject({ cursor: '' });
    expect(await meta('cursor')).toBe('50');

    await pull(); // repaired: continues from where it is
    expect(pullMock.mock.calls[1]![0]).toMatchObject({ cursor: '50' });
  });

  // A remote change and a local edit of the same row both read the field
  // clock, merge and write it back. Side by side, the later write erased
  // the other's stamp.
  it('does not interleave with a local write to the same row', async () => {
    const db = getCurrentDB();
    await db.exec(
      `INSERT INTO apiary (id, name, note, deleted, field_hlc) VALUES ('a1', 'N', 'old', 0, '{}')`
    );
    // Reading a row's merge state takes a moment, so two read-then-write
    // sequences that start together overlap unless something keeps them apart.
    const get = db.get.bind(db);
    db.get = (async (sql: string, params?: unknown[]) => {
      const row = await get(sql, params);
      if (sql.includes('field_hlc')) await new Promise((r) => setTimeout(r, 5));
      return row;
    }) as typeof db.get;
    pullMock.mockResolvedValueOnce({
      changes: [
        { entity: 'apiary', entityId: 'a1', op: 1, hlc: t(5), payloadJson: '{"note":"remote"}' }
      ],
      nextCursor: '',
      hasMore: false
    });

    await withoutBackgroundSync(() =>
      Promise.all([pull(), patch('apiary', 'a1', 'a1', { name: 'Local' })])
    );

    const row = await db.get<any>(`SELECT name, note, field_hlc FROM apiary WHERE id = 'a1'`);
    expect(row).toMatchObject({ name: 'Local', note: 'remote' });
    const fc = parseFieldClock(row!.field_hlc);
    expect(fc.note).toBe(t(5));
    expect(parseHLC(fc.name)).not.toBeNull();
  });
});

describe('syncOnce and the status store', () => {
  it('still pulls when push fails (a stuck outbox must not block fresh data)', async () => {
    await queueOutbox('apiary', 'a1', { name: 'X' }, t(1, 'local'));
    pushMock.mockRejectedValue(new ConnectError('down', Code.Unavailable));
    pullMock.mockResolvedValue({
      changes: [
        {
          entity: 'apiary',
          entityId: 'r1',
          op: 1,
          hlc: t(3),
          payloadJson: '{"name":"remote","deleted":0}'
        }
      ],
      nextCursor: '',
      hasMore: false
    });

    await syncOnce();

    expect(await getCurrentDB().get<any>(`SELECT * FROM apiary WHERE id = 'r1'`)).toBeTruthy();
    expect(await outbox()).toHaveLength(1); // kept for retry
    expect(get(syncStatus)).toMatchObject({ state: 'error', pending: 1, failed: 0 });
  });

  it('reports pending and failed counts and the time of the last complete run', async () => {
    await queueOutbox('apiary', 'a1', { name: 'ok' }, t(1, 'local'));
    await queueOutbox('apiary', 'a2', { name: 'bad' }, t(2, 'local'));
    pushMock.mockResolvedValue({ rejected: [{ index: 1, code: 'invalid', reason: 'r' }] });
    const started = Date.now();

    await syncOnce();

    const s = get(syncStatus);
    expect(s).toMatchObject({ state: 'idle', pending: 0, failed: 1 });
    expect(s.lastSyncedAt).toBeGreaterThanOrEqual(started);
    expect(Number(await meta('last_sync'))).toBe(s.lastSyncedAt);
  });

  it('shows offline without touching the network', async () => {
    await queueOutbox('apiary', 'a1', { name: 'X' }, t(1, 'local'));
    Object.defineProperty(globalThis.navigator, 'onLine', { value: false, configurable: true });
    try {
      await syncOnce();
    } finally {
      Object.defineProperty(globalThis.navigator, 'onLine', { value: true, configurable: true });
    }
    expect(pullMock).not.toHaveBeenCalled();
    expect(get(syncStatus)).toMatchObject({ state: 'offline', pending: 1 });
  });

  it('does nothing while another tab owns the database', async () => {
    storageState.set('elsewhere');
    await syncOnce();
    expect(pullMock).not.toHaveBeenCalled();
  });

  it('stops asking once the server rejects the session, until told to try again', async () => {
    await queueOutbox('apiary', 'a1', { name: 'X' }, t(1, 'local'));
    pushMock.mockRejectedValue(new ConnectError('no session', Code.Unauthenticated));

    await syncOnce();
    expect(get(syncStatus)).toMatchObject({ state: 'signed_out', pending: 1 });
    expect(pullMock).not.toHaveBeenCalled(); // no point pulling either
    expect(await outbox()).toHaveLength(1); // nothing is dropped

    await syncOnce();
    await syncOnce();
    expect(pushMock).toHaveBeenCalledOnce(); // not hammering

    pushMock.mockResolvedValue({});
    await syncNow();
    expect(get(syncStatus)).toMatchObject({ state: 'idle', pending: 0 });
  });

  it('a session that expires during pull also ends in signed_out', async () => {
    pullMock.mockRejectedValue(new ConnectError('no session', Code.Unauthenticated));
    await syncOnce();
    expect(get(syncStatus).state).toBe('signed_out');
    pullMock.mockResolvedValue(emptyPull);
    await syncNow();
    expect(get(syncStatus).state).toBe('idle');
  });
});
