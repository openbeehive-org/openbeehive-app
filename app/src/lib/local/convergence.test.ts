// Two-device offline convergence (backlog: "simulate two offline devices;
// assert per-field LWW + OR-Set converge").
//
// Device A writes through the real repo layer (repo.ts -> outbox), device B's
// concurrent edits are hand-stamped changes. Both sides then apply the other's
// changes through the real pull path (sync.ts applyRemote) — in OPPOSITE
// orders — and must end up with identical rows.
import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
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
vi.mock('#lib/client.js', () => ({
  syncClient: {
    push: vi.fn(async () => ({ serverCursor: '' })),
    pull: (...a: unknown[]) => pullMock(...a)
  }
}));

import { apiaries, setAdd, patch } from './repo';
import { pull } from './sync';

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
    const { setRemove } = await import('./repo');
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

    const { parseORSet, orValues } = await import('./merge');
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
