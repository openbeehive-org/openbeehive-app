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
const snapshotMock = vi.fn();
vi.mock('#lib/client.js', () => ({
  syncClient: {
    push: (...a: unknown[]) => pushMock(...a),
    pull: (...a: unknown[]) => pullMock(...a),
    snapshot: (...a: unknown[]) => snapshotMock(...a)
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
  discardFailed,
  dismissResyncNotice
} from './sync';
import { UpgradeRequiredSchema } from '../proto/openbeehive/v1/sync_pb';
import { patch, setAdd, setRemove } from './repo';
import { hlc, formatHLC, parseHLC, MAX_DRIFT_MS } from './hlc';
import { parseFieldClock, parseORSet } from './merge';
import { orValues } from '../../tests/orset';
import { readOnlyRefusals, resyncNotice, storageState } from './live';

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
// A device without a cursor loads a snapshot first; by default the
// workspace is empty and Pull continues from 'snap'.
const emptySnapshot = { rows: [], nextPageToken: '', cursor: 'snap' };

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
  snapshotMock.mockReset();
  snapshotMock.mockResolvedValue(emptySnapshot);
  resyncNotice.set(false);
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
    const refusals = get(readOnlyRefusals);

    await push(); // must not throw

    expect(await outbox()).toHaveLength(0);
    // Retrying cannot help and a read-only session has nothing to fix in a
    // problems list; the user is told instead.
    expect(await listFailed()).toHaveLength(0);
    expect(get(readOnlyRefusals)).toBe(refusals + 1);
  });

  it('undoes a refused read-only change: the next pull restores what the server holds', async () => {
    await getCurrentDB().exec(`INSERT INTO sync_meta (k, v) VALUES ('cursor', '9:40:aa')`);
    // A server row edited here and a task created here; neither may be stored.
    await withoutBackgroundSync(async () => {
      await patch('apiary', 'a1', 'a1', {
        organization_id: 'org1',
        name: 'Server name',
        deleted: 0
      });
      await getCurrentDB().exec(`DELETE FROM outbox`);
      await patch('apiary', 'a1', 'a1', { name: 'Renamed in the demo' });
      await patch('task', 't1', '', { organization_id: 'org1', title: 'New', deleted: 0 });
    });
    pushMock.mockRejectedValue(new ConnectError('read-only', Code.PermissionDenied));
    snapshotMock.mockResolvedValueOnce({
      rows: [
        snapRow(
          'apiary',
          'a1',
          { organization_id: 'org1', name: 'Server name', deleted: 0 },
          { name: t(5), deleted: t(5) }
        )
      ],
      nextPageToken: '',
      cursor: '9:50:bb'
    });

    await syncOnce();

    expect(snapshotMock).toHaveBeenCalledOnce();
    const a = await getCurrentDB().get<any>(`SELECT name FROM apiary WHERE id = 'a1'`);
    expect(a?.name).toBe('Server name');
    expect(await getCurrentDB().all(`SELECT id FROM task`)).toHaveLength(0);
    expect(await outbox()).toHaveLength(0);
    expect(get(syncStatus)).toMatchObject({ state: 'idle', pending: 0, failed: 0 });
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

  // Element names come from other devices. One that is also the name of a
  // built-in object property must neither throw nor stop the pull.
  it('set elements named like built-in properties are merged like any other', async () => {
    await getCurrentDB().exec(
      `INSERT INTO inspection (id, photo_keys, deleted, field_hlc) VALUES ('i1', ?, 0, '{}')`,
      [JSON.stringify({ p1: { a: [t(1, 'local')], r: [] } })]
    );
    const delta = (d: unknown, ms: number) => ({
      entity: 'inspection',
      entityId: 'i1',
      op: 1,
      hlc: t(ms),
      payloadJson: JSON.stringify({ photo_keys: d })
    });
    pullMock.mockResolvedValueOnce({
      changes: [
        delta({ add: ['constructor'] }, 2),
        {
          ...delta({ add: ['__proto__'] }, 3),
          payloadJson: '{"photo_keys":{"add":["__proto__"]}}'
        },
        delta({ remove: ['toString'], removed_tags: {} }, 4),
        delta({ remove: ['valueOf'], removed_tags: { hasOwnProperty: [t(1)] } }, 5),
        {
          entity: 'apiary',
          entityId: 'a1',
          op: 1,
          hlc: t(6),
          payloadJson: JSON.stringify({ name: 'After', deleted: 0 })
        }
      ],
      nextCursor: 'c9',
      hasMore: false
    });

    await pull();

    const row = await getCurrentDB().get<any>(`SELECT photo_keys FROM inspection WHERE id = 'i1'`);
    expect(orValues(parseORSet(row!.photo_keys)).sort()).toEqual([
      '__proto__',
      'constructor',
      'p1'
    ]);
    const a = await getCurrentDB().get<any>(`SELECT name FROM apiary WHERE id = 'a1'`);
    expect(a!.name).toBe('After');
    expect(await meta('cursor')).toBe('c9');
  });

  it('a snapshot set with built-in property names loads', async () => {
    snapshotMock.mockResolvedValueOnce({
      rows: [
        snapRow(
          'inspection',
          'i1',
          {
            photo_keys: { constructor: { a: [t(1)], r: [] }, toString: { a: [t(1)], r: [t(1)] } },
            deleted: 0
          },
          { deleted: t(1) }
        )
      ],
      nextPageToken: '',
      cursor: '9:1:aa'
    });
    await pull();
    const row = await getCurrentDB().get<any>(`SELECT photo_keys FROM inspection WHERE id = 'i1'`);
    expect(orValues(parseORSet(row!.photo_keys))).toEqual(['constructor']);
    expect(await meta('cursor')).toBe('9:1:aa');
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
  // starts over once, from the current rows; merging is idempotent, so that
  // only fills the gaps.
  it('loads the current rows once on a device with a cursor from before the fix', async () => {
    await getCurrentDB().exec(`INSERT INTO sync_meta (k, v) VALUES ('cursor', '42')`);
    pullMock.mockResolvedValue({ changes: [], nextCursor: '50', hasMore: false });

    await pull();
    expect(snapshotMock).toHaveBeenCalledOnce();
    expect(pullMock.mock.calls[0]![0]).toMatchObject({ cursor: 'snap' });
    expect(await meta('cursor')).toBe('50');

    await pull(); // repaired: continues from where it is
    expect(snapshotMock).toHaveBeenCalledOnce();
    expect(pullMock.mock.calls[1]![0]).toMatchObject({ cursor: '50' });
  });

  // Until 0.5 the server returned a user's personal tasks in every workspace
  // the user belongs to, so they were stored in each workspace's database.
  it('removes personal tasks of other workspaces once, and nothing else', async () => {
    const db = getCurrentDB();
    const task = (id: string, org: string | null, apiary: string) =>
      db.exec(
        `INSERT INTO task (id, organization_id, apiary_id, title, deleted) VALUES (?, ?, ?, 't', 0)`,
        [id, org, apiary]
      );
    await task('mine', 'org1', '');
    await task('leaked', 'org2', '');
    await task('leaked-unsent', 'org2', ''); // written here: it waits in the outbox
    await task('shared-elsewhere', 'org2', 'ap9'); // tied to an apiary: not a personal task
    await task('no-workspace', null, '');
    await queueOutbox('task', 'leaked-unsent', { title: 't' }, t(1, 'local'));

    await pull();

    const left = (await db.all<{ id: string }>(`SELECT id FROM task ORDER BY id`)).map((r) => r.id);
    expect(left).toEqual(['leaked-unsent', 'mine', 'no-workspace', 'shared-elsewhere']);

    await task('later', 'org2', '');
    await pull(); // the repair does not run again
    expect(await db.get(`SELECT id FROM task WHERE id = 'later'`)).toBeTruthy();
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

// A snapshot row as the server sends it.
const snapRow = (
  entity: string,
  entityId: string,
  fields: Record<string, unknown>,
  stamps: Record<string, string>
) => ({
  entity,
  entityId,
  payloadJson: JSON.stringify(fields),
  fieldHlcJson: JSON.stringify(stamps)
});

const RESET = 2; // PullStatus.RESET_REQUIRED
const SNAPSHOT = 1; // PullStatus.SNAPSHOT_REQUIRED

describe('snapshot bootstrap', () => {
  it('a device without a cursor loads the current rows page by page, then pulls', async () => {
    snapshotMock
      .mockResolvedValueOnce({
        rows: [
          snapRow(
            'apiary',
            'a1',
            { organization_id: 'org1', name: 'Orchard', note: 'n', lat: 48.2, deleted: 0 },
            { name: t(5), note: t(7), deleted: t(5) }
          )
        ],
        nextPageToken: 'p2',
        cursor: '9:40:aa'
      })
      .mockResolvedValueOnce({
        rows: [
          snapRow('hive', 'h1', { apiary_id: 'a1', name: 'H1', deleted: 1 }, { deleted: t(8) })
        ],
        nextPageToken: '',
        cursor: '9:40:aa'
      });
    pullMock.mockResolvedValueOnce({ changes: [], nextCursor: '9:41:bb', hasMore: false });

    await pull();

    expect(snapshotMock.mock.calls.map((c) => c[0].pageToken)).toEqual(['', 'p2']);
    expect(pullMock.mock.calls[0]![0]).toMatchObject({ cursor: '9:40:aa' });
    expect(await meta('cursor')).toBe('9:41:bb');
    const a = await getCurrentDB().get<any>(`SELECT * FROM apiary WHERE id = 'a1'`);
    expect(a).toMatchObject({ organization_id: 'org1', name: 'Orchard', note: 'n', lat: 48.2 });
    // Stamps arrive per column; a column without one has none here either.
    expect(parseFieldClock(a!.field_hlc)).toEqual({ name: t(5), note: t(7), deleted: t(5) });
    // Tombstones are rows like any other.
    const h = await getCurrentDB().get<any>(`SELECT * FROM hive WHERE id = 'h1'`);
    expect(h).toMatchObject({ name: 'H1', deleted: 1 });
  });

  it('stores a page of rows in one transaction', async () => {
    const db = getCurrentDB();
    const writes = { exec: 0, batch: 0 };
    const exec = db.exec.bind(db);
    const batch = db.batch.bind(db);
    db.exec = async (sql, params) => {
      if (!/^\s*(SELECT|PRAGMA)/i.test(sql)) writes.exec++;
      return exec(sql, params);
    };
    db.batch = async (stmts) => {
      if (stmts.length > 0) writes.batch++;
      return batch(stmts);
    };
    const rows = Array.from({ length: 20 }, (_, i) =>
      snapRow('hive', `h${i}`, { apiary_id: 'a1', name: `H${i}`, deleted: 0 }, { name: t(5) })
    );
    // The same row twice in a page: the later stamp wins.
    rows.push(snapRow('hive', 'h0', { name: 'H0 renamed' }, { name: t(6) }));
    snapshotMock.mockResolvedValueOnce({ rows, nextPageToken: '', cursor: '9:40:aa' });
    pullMock.mockResolvedValueOnce({ changes: [], nextCursor: '9:40:aa', hasMore: false });
    writes.exec = writes.batch = 0;

    await pull();

    expect((await db.all(`SELECT id FROM hive`)).length).toBe(20);
    expect(await db.get(`SELECT name FROM hive WHERE id = 'h0'`)).toEqual({ name: 'H0 renamed' });
    // Two transactions for the page (split at the repeated row) plus the
    // cursor and sync bookkeeping, not one per row.
    expect(writes.exec + writes.batch).toBeLessThan(10);
  });

  it('keeps no cursor from a snapshot that broke off, and starts it over', async () => {
    const page1 = {
      rows: [snapRow('apiary', 'a1', { name: 'Orchard', deleted: 0 }, { name: t(5) })],
      nextPageToken: 'p2',
      cursor: '9:40:aa'
    };
    snapshotMock
      .mockResolvedValueOnce(page1)
      .mockRejectedValueOnce(new ConnectError('gone', Code.Unavailable));
    await expect(pull()).rejects.toThrow();
    expect(await meta('cursor')).toBe('');
    expect(pullMock).not.toHaveBeenCalled();

    snapshotMock
      .mockResolvedValueOnce(page1)
      .mockResolvedValueOnce({ rows: [], nextPageToken: '', cursor: '9:40:aa' });
    await pull();
    expect(snapshotMock.mock.calls.map((c) => c[0].pageToken)).toEqual(['', 'p2', '', 'p2']);
    expect(await meta('cursor')).toBe('9:40:aa');
    expect(await getCurrentDB().all(`SELECT id FROM apiary`)).toHaveLength(1);
  });

  it('never overwrites a newer local edit and leaves the outbox alone', async () => {
    await withoutBackgroundSync(() =>
      patch('apiary', 'a1', 'a1', { organization_id: 'org1', name: 'Local name', deleted: 0 })
    );
    const local = parseFieldClock(
      (await getCurrentDB().get<any>(`SELECT field_hlc FROM apiary WHERE id = 'a1'`))!.field_hlc
    );
    const before = await outbox();
    expect(before).toHaveLength(1);
    const older = t(parseHLC(local.name)!.ms - 60_000);
    const newer = t(parseHLC(local.name)!.ms + 60_000);
    snapshotMock.mockResolvedValueOnce({
      rows: [
        snapRow(
          'apiary',
          'a1',
          { name: 'Server name', note: 'server note', address: 'unstamped', deleted: 1, lat: 1 },
          { name: older, note: newer, deleted: older }
        )
      ],
      nextPageToken: '',
      cursor: 'c'
    });

    await pull();

    const row = await getCurrentDB().get<any>(`SELECT * FROM apiary WHERE id = 'a1'`);
    // name and deleted were written here after the server's stamps; note is
    // newer on the server; address and lat have no stamp on either side.
    expect(row).toMatchObject({
      name: 'Local name',
      deleted: 0,
      note: 'server note',
      address: 'unstamped',
      lat: 1
    });
    expect(parseFieldClock(row!.field_hlc)).toEqual({ ...local, note: newer });
    expect(await outbox()).toEqual(before);
  });

  it('takes a column without a stamp only where this device has none either', async () => {
    await getCurrentDB().exec(
      `INSERT INTO apiary (id, name, note, deleted, field_hlc) VALUES ('a1', 'Local', 'old', 0, ?)`,
      [JSON.stringify({ name: t(3, 'local') })]
    );
    snapshotMock.mockResolvedValueOnce({
      rows: [snapRow('apiary', 'a1', { name: 'Server', note: 'server' }, {})],
      nextPageToken: '',
      cursor: 'c'
    });
    await pull();
    const row = await getCurrentDB().get<any>(`SELECT name, note FROM apiary WHERE id = 'a1'`);
    expect(row).toEqual({ name: 'Local', note: 'server' });
  });

  it('merges set columns by union of their tags', async () => {
    await getCurrentDB().exec(
      `INSERT INTO inspection (id, photo_keys, deleted, field_hlc) VALUES ('i1', ?, 0, '{}')`,
      [JSON.stringify({ p1: { a: [t(1, 'local')], r: [] }, p3: { a: [t(2, 'local')], r: [] } })]
    );
    snapshotMock.mockResolvedValueOnce({
      rows: [
        snapRow(
          'inspection',
          'i1',
          {
            photo_keys: { p1: { a: [t(1, 'local')], r: [t(1, 'local')] }, p2: { a: [t(4)], r: [] } }
          },
          {}
        )
      ],
      nextPageToken: '',
      cursor: 'c'
    });
    await pull();
    const row = await getCurrentDB().get<any>(`SELECT photo_keys FROM inspection WHERE id = 'i1'`);
    // p1 was removed on the server, p2 added there, p3 added here.
    expect(orValues(parseORSet(row!.photo_keys)).sort()).toEqual(['p2', 'p3']);
  });

  it('skips rows and columns this app does not know', async () => {
    snapshotMock.mockResolvedValueOnce({
      rows: [
        snapRow('gadget', 'g1', { name: 'x' }, { name: t(1) }),
        snapRow('sync_meta', 'cursor', { v: 'x' }, {}),
        snapRow('apiary', 'a1', { name: 'ok', colour: 'blue', nested: { a: 1 } }, { name: t(1) }),
        { entity: 'apiary', entityId: 'a2', payloadJson: 'not json', fieldHlcJson: '{}' },
        { entity: 'apiary', entityId: 'a3', payloadJson: '[1]', fieldHlcJson: '{}' }
      ],
      nextPageToken: '',
      cursor: 'c'
    });
    await pull();
    const rows = await getCurrentDB().all<any>(`SELECT id, name FROM apiary`);
    expect(rows).toEqual([{ id: 'a1', name: 'ok' }]);
    expect(await meta('cursor')).toBe('c');
  });

  it('reads the change log from the start on a server without snapshots', async () => {
    snapshotMock.mockRejectedValue(new ConnectError('unimplemented', Code.Unimplemented));
    pullMock.mockResolvedValueOnce({
      changes: [
        { entity: 'apiary', entityId: 'a1', op: 1, hlc: t(5), payloadJson: '{"name":"R"}' }
      ],
      nextCursor: '7',
      hasMore: false
    });
    await pull();
    expect(pullMock.mock.calls[0]![0]).toMatchObject({ cursor: '' });
    expect(await meta('cursor')).toBe('7');
    expect(await getCurrentDB().all(`SELECT id FROM apiary`)).toHaveLength(1);
  });

  it('loads a snapshot when its cursor is behind the compacted log, and re-sends nothing', async () => {
    await getCurrentDB().exec(
      `INSERT INTO sync_meta (k, v) VALUES ('cursor', '9:3:aa'), ('sync_epoch', '2')`
    );
    await getCurrentDB().exec(
      `INSERT INTO apiary (id, name, deleted, field_hlc) VALUES ('a1', 'Mine', 0, ?)`,
      [JSON.stringify({ name: t(3, 'local') })]
    );
    await queueOutbox('apiary', 'a1', { note: 'waiting' }, t(4, 'local'));
    pullMock
      .mockResolvedValueOnce({
        changes: [],
        nextCursor: '9:3:aa',
        hasMore: false,
        status: SNAPSHOT
      })
      .mockResolvedValueOnce({ changes: [], nextCursor: '9:80:cc', hasMore: false });
    snapshotMock.mockResolvedValueOnce({
      rows: [snapRow('apiary', 'a2', { name: 'Theirs', deleted: 0 }, { name: t(9) })],
      nextPageToken: '',
      cursor: '9:78:bb'
    });

    await pull();

    expect(pullMock.mock.calls.map((c) => c[0].cursor)).toEqual(['9:3:aa', '9:78:bb']);
    expect(await meta('cursor')).toBe('9:80:cc');
    expect(await getCurrentDB().all(`SELECT id FROM apiary`)).toHaveLength(2);
    expect(await outbox()).toHaveLength(1); // only what was already waiting
    expect(get(resyncNotice)).toBe(false);
  });
});

describe('reset: the server is not the database this device synced with', () => {
  // Builds a device that has synced before: rows written over time, a photo
  // set with history, a deleted row, a personal task. Returns the stamps.
  async function seedSyncedDevice() {
    const db = getCurrentDB();
    await db.exec(
      `INSERT INTO sync_meta (k, v) VALUES ('cursor', '9:150:aa'), ('sync_epoch', '2')`
    );
    const s = {
      made: t(100, 'devA'),
      renamed: t(200, 'devB'),
      photo: t(300, 'devA'),
      gone: t(400, 'devA')
    };
    await db.exec(
      `INSERT INTO apiary (id, organization_id, name, note, lat, deleted, field_hlc) VALUES ('a1', 'org1', 'Renamed', 'n', 48.2, 0, ?)`,
      [JSON.stringify({ organization_id: s.made, name: s.renamed, note: s.made, deleted: s.made })]
    );
    await db.exec(
      `INSERT INTO hive (id, organization_id, apiary_id, name, deleted, field_hlc) VALUES ('h1', 'org1', 'a1', 'H1', 1, ?)`,
      [JSON.stringify({ apiary_id: s.made, name: s.made, deleted: s.gone })]
    );
    await db.exec(
      `INSERT INTO inspection (id, hive_id, note, photo_keys, deleted, field_hlc) VALUES ('i1', 'h1', 'calm', ?, 0, ?)`,
      [
        JSON.stringify({
          p1: { a: [s.made], r: [s.made] },
          p2: { a: [s.photo], r: [] }
        }),
        JSON.stringify({ hive_id: s.made, note: s.made, deleted: s.made })
      ]
    );
    await db.exec(
      `INSERT INTO task (id, organization_id, apiary_id, title, done, deleted, field_hlc) VALUES ('t1', 'org1', '', 'jars', 0, 0, ?)`,
      [JSON.stringify({ title: s.made, done: s.made })]
    );
    await db.exec(
      `INSERT INTO event (id, scope_id, hive_id, title, deleted, field_hlc) VALUES ('e1', 'a1', 'h1', 'Created', 0, ?)`,
      [JSON.stringify({ title: s.made })]
    );
    return s;
  }

  const queued = async () =>
    (await outbox()).map((o) => ({
      entity: o.entity,
      id: o.entity_id,
      scope: o.scope_id,
      op: o.op,
      hlc: o.hlc,
      payload: JSON.parse(o.payload)
    }));

  it('queues every local row again by stamp, loads a snapshot and tells the user', async () => {
    const s = await seedSyncedDevice();
    // One change of this device has not been uploaded yet; one was refused.
    await queueOutbox('apiary', 'a1', { name: 'Renamed' }, s.renamed);
    await getCurrentDB().exec(
      `INSERT INTO outbox_failed (id, entity, entity_id, scope_id, op, payload, hlc, author_id, code, reason, failed_at)
       VALUES ('f1', 'event', 'e1', 'a1', 1, '{"title":"Created"}', ?, 'u1', 'invalid', 'r', 'now')`,
      [s.made]
    );
    pullMock
      .mockResolvedValueOnce({ changes: [], nextCursor: '9:150:aa', hasMore: false, status: RESET })
      .mockResolvedValueOnce({ changes: [], nextCursor: '9:101:cc', hasMore: false });
    snapshotMock.mockResolvedValueOnce({
      rows: [snapRow('apiary', 'a1', { name: 'Before the backup' }, { name: t(100, 'devA') })],
      nextPageToken: '',
      cursor: '9:100:bb'
    });

    await pull();

    // The snapshot did not undo what this device knows better.
    const a = await getCurrentDB().get<any>(`SELECT name FROM apiary WHERE id = 'a1'`);
    expect(a!.name).toBe('Renamed');
    expect(await meta('cursor')).toBe('9:101:cc');

    const out = await queued();
    const of = (entity: string, hlc: string) =>
      out.filter((o) => o.entity === entity && o.hlc === hlc);
    // One change per row and stamp. The oldest one carries every stamped
    // column, so it can create the row on a server that lost it (a new row
    // without its name would be refused); the later ones set what they stamped.
    expect(of('apiary', s.made)).toEqual([
      {
        entity: 'apiary',
        id: 'a1',
        scope: 'a1',
        op: 1,
        hlc: s.made,
        payload: { organization_id: 'org1', name: 'Renamed', note: 'n', deleted: 0 }
      }
    ]);
    // The rename was still waiting in the outbox: it is not queued twice.
    expect(of('apiary', s.renamed)).toHaveLength(1);
    // A deleted row comes back as deleted, in its apiary's scope.
    expect(of('hive', s.made)[0]).toMatchObject({
      scope: 'a1',
      payload: { apiary_id: 'a1', name: 'H1', deleted: 1 }
    });
    expect(of('hive', s.gone)[0]).toMatchObject({ scope: 'a1', op: 1, payload: { deleted: 1 } });
    expect(Object.keys(of('hive', s.gone)[0]!.payload)).toEqual(['deleted']);
    // Set columns: an add per tag, and the removal with the tags it names.
    expect(of('inspection', s.made)[0]).toMatchObject({
      scope: 'a1', // the apiary of its hive
      payload: { hive_id: 'h1', note: 'calm', deleted: 0, photo_keys: { add: ['p1'] } }
    });
    expect(of('inspection', s.photo)[0]!.payload).toEqual({
      photo_keys: { add: ['p2'], remove: ['p1'], removed_tags: { p1: [s.made] } }
    });
    // A personal task goes to the user's own scope.
    expect(of('task', s.made)[0]).toMatchObject({
      scope: 'user:u1',
      payload: { title: 'jars', done: 0 }
    });
    // What the server refused before is not sent again behind the user's back.
    expect(of('event', s.made)).toHaveLength(0);
    expect(out).toHaveLength(7);

    expect(get(resyncNotice)).toBe(true);
    expect(await meta('resynced')).toBe('1');
    await dismissResyncNotice();
    expect(get(resyncNotice)).toBe(false);
    expect(await meta('resynced')).toBe('');
  });

  // The server may have lost a newer apiary. Changes go up oldest first, so
  // an old change of a hive that was moved there later must not wait for it.
  it('uploads a change that is older than the apiary of its row in the oldest scope', async () => {
    const db = getCurrentDB();
    await db.exec(
      `INSERT INTO sync_meta (k, v) VALUES ('cursor', '9:150:aa'), ('sync_epoch', '2')`
    );
    for (const [id, at] of [
      ['a-new', t(250, 'devA')],
      ['a-old', t(50, 'devA')]
    ]) {
      await db.exec(`INSERT INTO apiary (id, name, deleted, field_hlc) VALUES (?, 'A', 0, ?)`, [
        id,
        JSON.stringify({ name: at })
      ]);
    }
    await db.exec(
      `INSERT INTO hive (id, apiary_id, name, deleted, field_hlc) VALUES ('h1', 'a-new', 'H1', 0, ?)`,
      [JSON.stringify({ name: t(100, 'devA'), apiary_id: t(300, 'devA') })]
    );
    await db.exec(
      `INSERT INTO task (id, apiary_id, title, deleted, field_hlc) VALUES ('t1', 'a-new', 'shared', 0, ?)`,
      [JSON.stringify({ title: t(400, 'devA') })]
    );
    pullMock
      .mockResolvedValueOnce({ changes: [], nextCursor: '9:150:aa', hasMore: false, status: RESET })
      .mockResolvedValueOnce({ changes: [], nextCursor: '', hasMore: false });

    await pull();

    const scopes = Object.fromEntries((await queued()).map((o) => [`${o.id}@${o.hlc}`, o.scope]));
    expect(scopes).toEqual({
      [`a-old@${t(50, 'devA')}`]: 'a-old',
      [`a-new@${t(250, 'devA')}`]: 'a-new',
      [`h1@${t(100, 'devA')}`]: 'a-old', // written before a-new existed
      [`h1@${t(300, 'devA')}`]: 'a-new',
      [`t1@${t(400, 'devA')}`]: 'a-new' // a task with an apiary is shared
    });
  });

  it('does not queue everything twice when the snapshot fails after the reset', async () => {
    await seedSyncedDevice();
    pullMock.mockResolvedValue({
      changes: [],
      nextCursor: '9:150:aa',
      hasMore: false,
      status: RESET
    });
    snapshotMock.mockRejectedValueOnce(new ConnectError('down', Code.Unavailable));
    await expect(pull()).rejects.toThrow();
    const first = await outbox();
    expect(first.length).toBeGreaterThan(0);
    expect(await meta('cursor')).toBe('9:150:aa');
    expect(get(resyncNotice)).toBe(false);

    // The queue is uploaded, then the same position is refused once more.
    await getCurrentDB().exec(`DELETE FROM outbox`);
    pullMock
      .mockReset()
      .mockResolvedValueOnce({ changes: [], nextCursor: '9:150:aa', hasMore: false, status: RESET })
      .mockResolvedValueOnce({ changes: [], nextCursor: '', hasMore: false });
    await pull();
    expect(await outbox()).toHaveLength(0);
    expect(await meta('cursor')).toBe('snap');
    expect(get(resyncNotice)).toBe(true);
  });

  it('gives up when the server refuses the cursor of its own snapshot', async () => {
    await seedSyncedDevice();
    pullMock.mockResolvedValue({ changes: [], nextCursor: '', hasMore: false, status: RESET });
    await expect(pull()).rejects.toThrow(/own snapshot/);
    expect(pullMock).toHaveBeenCalledTimes(2);
    expect(snapshotMock).toHaveBeenCalledOnce();
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

  // The server turns this app version away (see UpgradeRequired in
  // sync.proto). That is not a failure to retry every few seconds.
  it('stops and says so when the server requires a newer app', async () => {
    const tooOld = () =>
      new ConnectError('upgrade required', Code.FailedPrecondition, undefined, [
        { desc: UpgradeRequiredSchema, value: { minProtocol: 2 } }
      ]);
    await queueOutbox('apiary', 'a1', { name: 'X' }, t(1, 'local'));
    pushMock.mockRejectedValue(tooOld());
    pullMock.mockRejectedValue(tooOld());

    await syncOnce();
    expect(get(syncStatus)).toMatchObject({ state: 'update_required', pending: 1 });
    expect(await outbox()).toHaveLength(1); // nothing is dropped
    expect(pullMock).not.toHaveBeenCalled();

    await syncOnce();
    await syncOnce();
    expect(pushMock).toHaveBeenCalledOnce(); // not hammering

    // After the update (or when the user asks) it tries again.
    pushMock.mockResolvedValue({});
    pullMock.mockResolvedValue(emptyPull);
    await syncNow();
    expect(get(syncStatus)).toMatchObject({ state: 'idle', pending: 0 });
  });

  it('also when only pull is refused; any other failed precondition is retried', async () => {
    pullMock.mockRejectedValue(
      new ConnectError('upgrade required', Code.FailedPrecondition, undefined, [
        { desc: UpgradeRequiredSchema, value: { minProtocol: 2 } }
      ])
    );
    await syncOnce();
    expect(get(syncStatus).state).toBe('update_required');

    pullMock.mockRejectedValue(new ConnectError('not now', Code.FailedPrecondition));
    await syncNow();
    expect(get(syncStatus).state).toBe('error');
    pullMock.mockResolvedValue(emptyPull);
    await syncOnce(); // an ordinary error does not switch sync off
    expect(get(syncStatus).state).toBe('idle');
  });

  it('uploads what a reset queued in the same run', async () => {
    const db = getCurrentDB();
    await db.exec(
      `INSERT INTO sync_meta (k, v) VALUES ('cursor', '9:150:aa'), ('sync_epoch', '2')`
    );
    await db.exec(
      `INSERT INTO apiary (id, organization_id, name, deleted, field_hlc) VALUES ('a1', 'org1', 'Mine', 0, ?)`,
      [JSON.stringify({ name: t(100, 'devA') })]
    );
    pullMock
      .mockResolvedValueOnce({ changes: [], nextCursor: '9:150:aa', hasMore: false, status: 2 })
      .mockResolvedValue({ changes: [], nextCursor: '9:101:cc', hasMore: false });
    pushMock.mockResolvedValue({});

    await syncOnce();

    expect(pushMock).toHaveBeenCalledOnce();
    expect(pushMock.mock.calls[0]![0].changes).toMatchObject([
      { entity: 'apiary', entityId: 'a1', hlc: t(100, 'devA'), payloadJson: '{"name":"Mine"}' }
    ]);
    expect(get(syncStatus)).toMatchObject({ state: 'idle', pending: 0 });
    expect(get(resyncNotice)).toBe(true);
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
