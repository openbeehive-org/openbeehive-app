import { describe, it, expect, beforeEach, vi } from 'vitest';
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

import { push, pull, syncOnce } from './sync';
import { parseFieldClock, parseORSet, orValues } from './merge';

const t = (ms: number, node = 'remote') => `${String(ms).padStart(15, '0')}:00000:${node}`;

async function queueOutbox(entity: string, entityId: string, payload: unknown, hlc: string) {
  await getCurrentDB().exec(
    `INSERT INTO outbox (id, entity, entity_id, scope_id, op, payload, hlc, author_id)
     VALUES (?, ?, ?, ?, 1, ?, ?, 'u1')`,
    [crypto.randomUUID(), entity, entityId, entityId, JSON.stringify(payload), hlc]
  );
}

beforeEach(async () => {
  setCurrentDB(await makeLocalDB());
  pushMock.mockReset();
  pullMock.mockReset();
  pullMock.mockResolvedValue({ changes: [], nextCursor: '', hasMore: false });
});

describe('push', () => {
  it('clears the outbox and stores the server cursor on success', async () => {
    await queueOutbox('apiary', 'a1', { name: 'X' }, t(1, 'local'));
    pushMock.mockResolvedValue({ serverCursor: '42' });

    await push();

    expect(pushMock).toHaveBeenCalledOnce();
    expect(await getCurrentDB().all(`SELECT * FROM outbox`)).toHaveLength(0);
    const cur = await getCurrentDB().get<any>(`SELECT v FROM sync_meta WHERE k = 'cursor'`);
    expect(cur!.v).toBe('42');
  });

  it('keeps the outbox on transient errors and rethrows', async () => {
    await queueOutbox('apiary', 'a1', { name: 'X' }, t(1, 'local'));
    pushMock.mockRejectedValue(new ConnectError('boom', Code.Unavailable));

    await expect(push()).rejects.toThrow();
    expect(await getCurrentDB().all(`SELECT * FROM outbox`)).toHaveLength(1);
  });

  it('drops the batch when the server says permission denied (read-only demo)', async () => {
    await queueOutbox('apiary', 'a1', { name: 'X' }, t(1, 'local'));
    pushMock.mockRejectedValue(new ConnectError('read-only', Code.PermissionDenied));

    await push(); // must not throw

    expect(await getCurrentDB().all(`SELECT * FROM outbox`)).toHaveLength(0);
    // The local row itself is untouched; only the upload intent is dropped.
  });

  it('does nothing when the outbox is empty', async () => {
    await push();
    expect(pushMock).not.toHaveBeenCalled();
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
    const cur = await getCurrentDB().get<any>(`SELECT v FROM sync_meta WHERE k = 'cursor'`);
    expect(cur!.v).toBe('c1');
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

  it('ignores changes for unknown entities', async () => {
    pullMock.mockResolvedValueOnce({
      changes: [
        {
          entity: 'nonsense; DROP TABLE apiary',
          entityId: 'x',
          op: 1,
          hlc: t(1),
          payloadJson: '{}'
        }
      ],
      nextCursor: '',
      hasMore: false
    });
    await pull(); // must not throw, must not touch tables
    expect(await getCurrentDB().all(`SELECT * FROM apiary`)).toHaveLength(0);
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
    expect(await getCurrentDB().all(`SELECT * FROM apiary`)).toHaveLength(2);
    const cur = await getCurrentDB().get<any>(`SELECT v FROM sync_meta WHERE k = 'cursor'`);
    expect(cur!.v).toBe('c2');
  });
});

describe('syncOnce', () => {
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
    expect(await getCurrentDB().all(`SELECT * FROM outbox`)).toHaveLength(1); // kept for retry
  });
});
