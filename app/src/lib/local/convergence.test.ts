// Two-device offline convergence (backlog: "simulate two offline devices;
// assert per-field LWW + OR-Set converge").
//
// Device A writes through the real repo layer (repo.ts -> outbox), device B's
// concurrent edits are hand-stamped changes. Both sides then apply the other's
// changes through the real pull path (sync.ts applyRemote), in OPPOSITE
// orders, and must end up with identical rows.
import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
import { get } from 'svelte/store';
import { makeLocalDB, setCurrentDB } from '../../tests/localdb';
import type { LocalDB } from './db';

vi.mock('./db', async () => {
  const { getCurrentDB } = await import('../../tests/localdb');
  return { getDB: async () => getCurrentDB() };
});
vi.mock('./sync', async (importOriginal) => {
  // Real pull/applyRemote, but repo's fire-and-forget syncOnce is muted.
  const mod = await importOriginal<any>();
  return { ...mod, syncOnce: vi.fn(async () => {}) };
});

const pullMock = vi.fn();
const pushMock = vi.fn();
const snapshotMock = vi.fn();
vi.mock('#lib/client.js', () => ({
  syncClient: {
    push: (...a: unknown[]) => pushMock(...a),
    pull: (...a: unknown[]) => pullMock(...a),
    snapshot: (...a: unknown[]) => snapshotMock(...a)
  }
}));

import { apiaries, setAdd, setRemove, patch } from './repo';
import { pull, push } from './sync';
import { resyncNotice } from './live';
import { mergeFields, parseSetDelta, parseORSet, tombstone, type RowState } from './merge';
import { orValues } from '../../tests/orset';

type Change = { entity: string; entityId: string; op: number; payloadJson: string; hlc: string };

const stamp = (ms: number, node: string) => `${String(ms).padStart(15, '0')}:00000:${node}`;

async function drainOutbox(db: LocalDB): Promise<Change[]> {
  const rows = await db.all<any>(`SELECT * FROM outbox ORDER BY hlc ASC`);
  await db.exec(`DELETE FROM outbox`);
  return rows.map((r) => ({
    entity: r.entity,
    entityId: r.entity_id,
    op: r.op,
    payloadJson: r.payload,
    hlc: r.hlc
  }));
}

async function applyOn(db: LocalDB, changes: Change[]) {
  setCurrentDB(db);
  pullMock.mockResolvedValueOnce({ changes, nextCursor: '', hasMore: false });
  await pull();
}

async function apiaryRow(db: LocalDB, id: string) {
  return db.get<any>(`SELECT id, name, note, address, deleted FROM apiary WHERE id = ?`, [id]);
}

let A: LocalDB, B: LocalDB;

beforeEach(async () => {
  A = await makeLocalDB();
  B = await makeLocalDB();
  localStorage.setItem('obh.orgId', 'org1');
  localStorage.setItem('obh.userId', 'userA');
  pullMock.mockReset();
  pushMock.mockReset();
  snapshotMock.mockReset();
  snapshotMock.mockResolvedValue({ rows: [], nextPageToken: '', cursor: '' });
  resyncNotice.set(false);
  vi.useFakeTimers();
});
afterEach(() => vi.useRealTimers());

describe('two offline devices converge', () => {
  it('per-field LWW: same field conflict + disjoint fields', async () => {
    // t=1000: A creates the apiary and both devices sync it.
    vi.setSystemTime(1_000_000);
    setCurrentDB(A);
    const id = await apiaries.create({ organization_id: 'org1', name: 'Origin', note: '' });
    const created = await drainOutbox(A);
    await applyOn(B, created);

    // Offline edits: B renames at t=1500 and writes a note; A renames at t=2000.
    const fromB: Change[] = [
      {
        entity: 'apiary',
        entityId: id,
        op: 1,
        payloadJson: JSON.stringify({ name: 'B-name', note: 'from B' }),
        hlc: stamp(1_500_000, 'devB')
      }
    ];
    await applyOn(B, fromB); // B's own edit lands on B first
    vi.setSystemTime(2_000_000);
    setCurrentDB(A);
    await apiaries.rename(id, 'A-name');
    const fromA = await drainOutbox(A);

    // Exchange in opposite orders.
    await applyOn(A, fromB);
    await applyOn(B, fromA);

    const a = await apiaryRow(A, id);
    const b = await apiaryRow(B, id);
    expect(a).toEqual(b);
    expect(a!.name).toBe('A-name'); // later stamp wins on both sides
    expect(a!.note).toBe('from B'); // disjoint field survives on both sides
  });

  it('delete vs concurrent edit: the later stamp decides on both devices', async () => {
    vi.setSystemTime(11_000_000);
    setCurrentDB(A);
    const id = await apiaries.create({ organization_id: 'org1', name: 'Doomed' });
    await applyOn(B, await drainOutbox(A));

    // B deletes at t=1500; A edits the name at t=2000 (after the delete stamp).
    const del: Change[] = [
      { entity: 'apiary', entityId: id, op: 2, payloadJson: '', hlc: stamp(11_500_000, 'devB') }
    ];
    await applyOn(B, del); // B's own delete lands on B first
    vi.setSystemTime(12_000_000);
    setCurrentDB(A);
    await apiaries.rename(id, 'Still here');
    const edit = await drainOutbox(A);

    await applyOn(A, del);
    await applyOn(B, edit);

    const a = await A.get<any>(`SELECT name, deleted FROM apiary WHERE id = ?`, [id]);
    const b = await B.get<any>(`SELECT name, deleted FROM apiary WHERE id = ?`, [id]);
    expect(a).toEqual(b); // convergence is the invariant that matters
    expect(a!.deleted).toBe(1); // delete stamped its own field; edit touched name only
    expect(a!.name).toBe('Still here');
  });

  it('OR-Set photos: concurrent add wins over remove, orders converge', async () => {
    vi.setSystemTime(21_000_000);
    setCurrentDB(A);
    await patch('inspection', 'i1', 'a1', { organization_id: 'org1', hive_id: 'h1', deleted: 0 });
    await setAdd('inspection', 'i1', 'a1', 'photo_keys', 'p1');
    await applyOn(B, await drainOutbox(A));

    // Concurrently: A removes p1 (having seen it), B adds p1 again + adds p2.
    vi.setSystemTime(22_000_000);
    setCurrentDB(A);
    await setRemove('inspection', 'i1', 'a1', 'photo_keys', 'p1');
    const fromA = await drainOutbox(A);

    const fromB: Change[] = [
      {
        entity: 'inspection',
        entityId: 'i1',
        op: 1,
        payloadJson: JSON.stringify({ photo_keys: { add: ['p1', 'p2'] } }),
        hlc: stamp(22_000_500, 'devB')
      }
    ];
    await applyOn(B, fromB); // B's own adds land on B first

    await applyOn(A, fromB);
    await applyOn(B, fromA);

    const a = await A.get<any>(`SELECT photo_keys FROM inspection WHERE id = 'i1'`);
    const b = await B.get<any>(`SELECT photo_keys FROM inspection WHERE id = 'i1'`);
    // Same visible set on both devices; B's unobserved re-add of p1 survives
    // A's remove (add-wins).
    expect(orValues(parseORSet(a!.photo_keys)).sort()).toEqual(['p1', 'p2']);
    expect(orValues(parseORSet(b!.photo_keys)).sort()).toEqual(['p1', 'p2']);
  });

  it('replaying the same changes twice is harmless (idempotent pull)', async () => {
    vi.setSystemTime(31_000_000);
    setCurrentDB(A);
    const id = await apiaries.create({ organization_id: 'org1', name: 'Once' });
    const changes = await drainOutbox(A);

    await applyOn(B, changes);
    const first = await apiaryRow(B, id);
    await applyOn(B, changes); // e.g. cursor reset / overlapping pages
    const second = await apiaryRow(B, id);

    expect(second).toEqual(first);
    expect(await B.all(`SELECT id FROM apiary`)).toHaveLength(1);
  });
});

// A server as the devices see it: one change log with a global sequence,
// Pull by cursor. Like every server before 0.5 it answers a push with that
// global sequence; a device must not mistake it for its own pull position.
function fakeServer() {
  const log: { seq: number; change: Change }[] = [];
  return {
    push: async (req: { changes: Change[] }) => {
      for (const change of req.changes) log.push({ seq: log.length + 1, change });
      return { serverCursor: String(log.length) };
    },
    pull: async (req: { cursor: string }) => {
      const after = Number(req.cursor || 0);
      const page = log.filter((e) => e.seq > after);
      return {
        changes: page.map((e) => e.change),
        nextCursor: page.length ? String(page[page.length - 1]!.seq) : req.cursor,
        hasMore: false
      };
    }
  };
}

describe('two devices syncing through a server', () => {
  it('a device that pushes after another one wrote still receives that write', async () => {
    const server = fakeServer();
    pushMock.mockImplementation(server.push);
    pullMock.mockImplementation(server.pull);
    const sync = async (db: LocalDB) => {
      setCurrentDB(db);
      await push();
      await pull();
    };

    // A creates the apiary; both devices have it.
    vi.setSystemTime(41_000_000);
    setCurrentDB(A);
    const id = await apiaries.create({ organization_id: 'org1', name: 'Shared', note: '' });
    await sync(A);
    await sync(B);
    expect((await apiaryRow(B, id))!.name).toBe('Shared');

    // B writes a note and syncs. A has not pulled it yet.
    vi.setSystemTime(42_000_000);
    setCurrentDB(B);
    await apiaries.setNote(id, 'from B');
    await sync(B);

    // A edits another field and syncs: push first, then pull.
    vi.setSystemTime(43_000_000);
    setCurrentDB(A);
    await apiaries.rename(id, 'Renamed by A');
    await sync(A);

    expect(await apiaryRow(A, id)).toMatchObject({ name: 'Renamed by A', note: 'from B' });
    await sync(B);
    expect(await apiaryRow(B, id)).toEqual(await apiaryRow(A, id));

    // Its own changes coming back through pull changed nothing on A.
    const fc = JSON.parse(
      (await A.get<any>(`SELECT field_hlc FROM apiary WHERE id = ?`, [id]))!.field_hlc
    );
    expect(fc.name > fc.note).toBe(true);
    expect(await A.all(`SELECT id FROM apiary`)).toHaveLength(1);
  });
});

// A server speaking protocol 1, reduced to what matters to a device: a
// change log with sequence numbers, the merged rows behind it, cursors
// "<epoch>:<seq>:<mark>" checked the way the real server checks them (the
// mark is the tag of the last server start before that position),
// snapshots, compaction, and a backup that can be restored. Like the real
// server it refuses a new row that lacks a column it requires.
type Logged = { seq: number; change: Change };
type ServerRow = RowState & { entity: string; id: string; fields: Record<string, unknown> };
type ServerState = {
  head: number;
  horizon: number;
  log: Logged[];
  rows: Map<string, ServerRow>;
  boots: { head: number; tag: number }[];
};

const REQUIRED: Record<string, string[]> = {
  apiary: ['name', 'created_at', 'updated_at'],
  hive: ['apiary_id', 'name']
};

function protocolServer() {
  const epoch = 7;
  const SET_COLS: Record<string, string[]> = { inspection: ['photo_keys'] };
  let state: ServerState = { head: 0, horizon: 0, log: [], rows: new Map(), boots: [] };
  let starts = 0;
  // The server process starts on the database as it is.
  const boot = () => {
    state.boots = state.boots.filter((b) => b.head !== state.head);
    state.boots.push({ head: state.head, tag: ++starts });
  };
  boot();
  const markBefore = (seq: number) =>
    state.boots.filter((b) => b.head < seq).sort((a, b) => b.head - a.head)[0]?.tag ?? 0;
  const cursorAt = (seq: number) => `${epoch}:${seq}:${markBefore(seq)}`;

  // Merges a change; returns false when it would create a row without a
  // column the table requires.
  function apply(ch: Change): boolean {
    const key = `${ch.entity}/${ch.entityId}`;
    const setCols = SET_COLS[ch.entity] ?? [];
    const fields: Record<string, unknown> = ch.op === 2 ? tombstone() : JSON.parse(ch.payloadJson);
    let row = state.rows.get(key);
    if (!row) {
      if ((REQUIRED[ch.entity] ?? []).some((c) => fields[c] == null)) return false;
      row = { entity: ch.entity, id: ch.entityId, fields: {}, clock: {}, sets: {} };
      state.rows.set(key, row);
    }
    for (const c of setCols) if (c in fields) fields[c] = parseSetDelta(fields[c]);
    for (const c of mergeFields(row, fields, setCols, ch.hlc)) {
      if (!setCols.includes(c)) row.fields[c] = fields[c];
    }
    return true;
  }

  // RESET (2), SNAPSHOT (1) or the sequence number to read after.
  function check(raw: string): { status: number; after: number } {
    if (raw === '') return state.horizon > 0 ? { status: 1, after: 0 } : { status: 0, after: 0 };
    const [e, seq, mark] = raw.split(':').map(Number);
    if (e !== epoch || !(seq! >= 0) || seq! > state.head) return { status: 2, after: 0 };
    if (markBefore(seq!) !== mark) return { status: 2, after: 0 };
    if (seq! < state.horizon) return { status: 1, after: 0 };
    return { status: 0, after: seq! };
  }

  return {
    push: async (req: { changes: Change[] }) => {
      const rejected: { index: number; code: string; reason: string }[] = [];
      req.changes.forEach((change, index) => {
        if (apply(change)) state.log.push({ seq: ++state.head, change });
        else rejected.push({ index, code: 'invalid', reason: 'a required column is missing' });
      });
      return { rejected };
    },
    pull: async (req: { cursor: string }) => {
      const { status, after } = check(req.cursor);
      if (status !== 0) return { changes: [], nextCursor: req.cursor, hasMore: false, status };
      const page = state.log.filter((e) => e.seq > after);
      return {
        changes: page.map((e) => e.change),
        nextCursor: cursorAt(state.head),
        hasMore: false,
        status: 0
      };
    },
    snapshot: async () => ({
      rows: [...state.rows.values()].map((r) => ({
        entity: r.entity,
        entityId: r.id,
        payloadJson: JSON.stringify({ ...r.fields, ...r.sets }),
        fieldHlcJson: JSON.stringify(r.clock)
      })),
      nextPageToken: '',
      cursor: cursorAt(state.head)
    }),
    backup: (): ServerState => structuredClone(state),
    // The backup is put back and the server is started on it.
    restore: (b: ServerState) => {
      state = structuredClone(b);
      boot();
    },
    // Drops the log up to and including seq; the rows stay.
    compact: (seq: number) => {
      state.horizon = seq;
      state.log = state.log.filter((e) => e.seq > seq);
    },
    // Removes log entries as a rebuilt demo or a deleted workspace does.
    forget: (keep: (e: Logged) => boolean) => {
      state.log = state.log.filter(keep);
    },
    head: () => state.head,
    logLength: () => state.log.length,
    row: (entity: string, id: string) => state.rows.get(`${entity}/${id}`)
  };
}

describe('two devices and a server that loses or forgets history', () => {
  let server: ReturnType<typeof protocolServer>;
  const sync = async (db: LocalDB) => {
    setCurrentDB(db);
    await push();
    await pull();
  };
  // Runs both devices until nothing is waiting anywhere.
  const settle = async () => {
    for (let round = 0; round < 6; round++) {
      await sync(A);
      await sync(B);
    }
    for (const db of [A, B]) expect(await db.all(`SELECT id FROM outbox`)).toHaveLength(0);
  };
  const tables = ['apiary', 'hive', 'inspection'];
  const contents = async (db: LocalDB) => {
    const out: Record<string, unknown[]> = {};
    for (const table of tables) {
      const rows = await db.all<any>(`SELECT * FROM ${table} ORDER BY id`);
      // Compared by meaning: the field clock as an object, the photo set by
      // what it shows (key and tag order differ between devices).
      out[table] = rows.map((r) => ({
        ...r,
        field_hlc: JSON.parse(r.field_hlc),
        ...('photo_keys' in r ? { photo_keys: orValues(parseORSet(r.photo_keys)).sort() } : {})
      }));
    }
    return out;
  };

  beforeEach(() => {
    server = protocolServer();
    pushMock.mockImplementation(server.push);
    pullMock.mockImplementation(server.pull);
    snapshotMock.mockImplementation(server.snapshot);
  });

  it('a restored backup: nothing the devices had is lost, and both end up equal', async () => {
    vi.setSystemTime(51_000_000);
    setCurrentDB(A);
    const id = await apiaries.create({ organization_id: 'org1', name: 'Orchard', note: '' });
    await patch('inspection', 'i1', id, { organization_id: 'org1', hive_id: 'h1', deleted: 0 });
    await setAdd('inspection', 'i1', id, 'photo_keys', 'p1');
    await settle();
    expect((await apiaryRow(B, id))!.name).toBe('Orchard'); // B started from a snapshot

    const backup = server.backup(); // last night's backup

    // After the backup: A renames, adds a photo and removes the first one,
    // and both devices sync it.
    vi.setSystemTime(52_000_000);
    setCurrentDB(A);
    await apiaries.rename(id, 'Renamed after the backup');
    await setAdd('inspection', 'i1', id, 'photo_keys', 'p2');
    await setRemove('inspection', 'i1', id, 'photo_keys', 'p1');
    // A also creates an apiary with a hive and edits both afterwards, so
    // their columns carry different stamps.
    const lost = await apiaries.create({ organization_id: 'org1', name: 'New', note: '' });
    await patch('hive', 'h-lost', lost, {
      organization_id: 'org1',
      apiary_id: lost,
      name: 'Hive',
      deleted: 0
    });
    vi.setSystemTime(52_500_000);
    await apiaries.rename(lost, 'New, renamed');
    await patch('hive', 'h-lost', lost, { name: 'Hive, renamed' });
    await settle();
    // B writes a note it has not uploaded when the server dies.
    vi.setSystemTime(53_000_000);
    setCurrentDB(B);
    await apiaries.setNote(id, 'written while the server was down');

    server.restore(backup);
    expect(server.row('apiary', id)!.fields.name).toBe('Orchard'); // the rename is gone
    expect(server.row('apiary', lost)).toBeUndefined(); // and so are the new rows

    // A third party keeps working with the restored server, so the sequence
    // numbers A and B had read are handed out again, to other changes, and
    // some of those entries are removed again before A and B come back.
    for (let i = 0; i < 40; i++) {
      await server.push({
        changes: [
          {
            entity: 'hive',
            entityId: `hx${i}`,
            op: 1,
            payloadJson: JSON.stringify({ apiary_id: id, name: `Hive ${i}`, deleted: 0 }),
            hlc: stamp(53_500_000 + i, 'devC')
          }
        ]
      });
    }
    server.forget((e) => e.seq % 2 === 0);

    vi.setSystemTime(54_000_000);
    await settle();

    // The server has the lost rename and photo changes back, and B's note.
    expect(server.row('apiary', id)!.fields).toMatchObject({
      name: 'Renamed after the backup',
      note: 'written while the server was down'
    });
    expect(orValues(server.row('inspection', 'i1')!.sets.photo_keys!)).toEqual(['p2']);
    // Both devices hold the same rows, including what was written after the restore.
    const a = await contents(A);
    expect(await contents(B)).toEqual(a);
    expect(a.apiary!.map((r: any) => r.name).sort()).toEqual([
      'New, renamed',
      'Renamed after the backup'
    ]);
    expect(a.inspection![0]).toMatchObject({ photo_keys: ['p2'] });
    expect(a.hive).toHaveLength(41); // the third party's forty and A's own
    expect(get(resyncNotice)).toBe(true);
    // Rows created and edited after the backup exist on the server again,
    // and it refused nothing the devices sent.
    expect(server.row('apiary', lost)!.fields.name).toBe('New, renamed');
    expect(server.row('hive', 'h-lost')!.fields).toMatchObject({
      apiary_id: lost,
      name: 'Hive, renamed'
    });
    for (const db of [A, B]) expect(await db.all(`SELECT id FROM outbox_failed`)).toHaveLength(0);
  });

  it('a compacted log: a device that was away catches up from a snapshot', async () => {
    vi.setSystemTime(61_000_000);
    setCurrentDB(A);
    const id = await apiaries.create({ organization_id: 'org1', name: 'Orchard', note: '' });
    await settle();

    // B is away. A keeps working; B edits offline.
    for (let i = 0; i < 5; i++) {
      vi.setSystemTime(62_000_000 + i * 1000);
      setCurrentDB(A);
      await apiaries.rename(id, `Name ${i}`);
      await patch('hive', `h${i}`, id, {
        organization_id: 'org1',
        apiary_id: id,
        name: `H${i}`,
        deleted: 0
      });
      await sync(A);
    }
    vi.setSystemTime(63_000_000);
    setCurrentDB(B);
    await apiaries.setNote(id, 'from B, offline');

    // The retention period passes: everything B has not read is compacted.
    server.compact(server.head());
    expect(server.logLength()).toBe(0);

    await settle();

    const a = await contents(A);
    expect(await contents(B)).toEqual(a);
    expect(a.hive).toHaveLength(5);
    expect(a.apiary![0]).toMatchObject({ name: 'Name 4', note: 'from B, offline' });
    // Nothing was lost on the server, so nothing is announced or re-sent.
    expect(get(resyncNotice)).toBe(false);

    // A device that joins now starts from the snapshot, too.
    const C = await makeLocalDB();
    await sync(C);
    expect(await contents(C)).toEqual(a);
  });
});
