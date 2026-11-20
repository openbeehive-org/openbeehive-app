// The account's settings record on the device: two devices and two
// workspace databases exchanging it through the real pull path, and the
// rule that decides between a device's own choice and the record.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { makeLocalDB, setCurrentDB } from '../../tests/localdb';
import type { LocalDB } from './db';

vi.mock('./db', async () => {
  const { getCurrentDB } = await import('../../tests/localdb');
  return { getDB: async () => getCurrentDB() };
});
vi.mock('./sync', async (importOriginal) => {
  const mod = await importOriginal<any>();
  return { ...mod, syncOnce: vi.fn(async () => {}) };
});
const applyLanguage = vi.fn();
vi.mock('../i18n', () => ({
  applyLanguage: (l: string) => applyLanguage(l),
  isLang: (v: unknown) => ['en', 'de', 'fr', 'es', 'it'].includes(v as string)
}));
const pullMock = vi.fn();
vi.mock('#lib/client.js', () => ({
  syncClient: {
    push: vi.fn(),
    pull: (...a: unknown[]) => pullMock(...a),
    snapshot: async () => ({ rows: [], nextPageToken: '', cursor: '' })
  }
}));

import { choose, readRecord, reconcile } from './userSettings';
import { pull } from './sync';
import { initialSyncPending } from './live';
import { decide, loadChoices, units, unitsFrom, type Choice } from '../prefs';

type Change = { entity: string; entityId: string; op: number; payloadJson: string; hlc: string };

const stamp = (ms: number, node: string) => `${String(ms).padStart(15, '0')}:00000:${node}`;

// One device: its own browser storage and the database of the workspace
// it has open.
type Device = { storage: Map<string, string>; db: LocalDB };

function use(dev: Device) {
  const s = dev.storage;
  (globalThis as any).localStorage = {
    getItem: (k: string) => s.get(k) ?? null,
    setItem: (k: string, v: string) => void s.set(k, String(v)),
    removeItem: (k: string) => void s.delete(k),
    clear: () => s.clear()
  };
  setCurrentDB(dev.db);
}

async function device(user: string): Promise<Device> {
  return { storage: new Map([['obh.userId', user]]), db: await makeLocalDB() };
}

async function drain(db: LocalDB): Promise<Change[]> {
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

// The server hands the changes to the device, which then applies them.
async function deliver(dev: Device, changes: Change[]) {
  use(dev);
  pullMock.mockResolvedValueOnce({ changes, nextCursor: '', hasMore: false });
  await pull();
  await reconcile();
}

const remote = (payload: Record<string, string>, hlc: string, user = 'u1'): Change => ({
  entity: 'user_settings',
  entityId: user,
  op: 1,
  payloadJson: JSON.stringify(payload),
  hlc
});

let A: Device, B: Device;

beforeEach(async () => {
  A = await device('u1');
  B = await device('u1');
  applyLanguage.mockReset();
  pullMock.mockReset();
  initialSyncPending.set(false);
  vi.useFakeTimers();
  vi.setSystemTime(1_000_000);
});
afterEach(() => vi.useRealTimers());

describe('the settings record across devices', () => {
  it('carries a choice to the other device, and the newer choice wins on both', async () => {
    use(A);
    await choose('weight_unit', 'lb');
    expect(get(units).weight).toBe('lb');
    await deliver(B, await drain(A.db));
    expect(unitsFrom(loadChoices()).weight).toBe('lb');

    // Offline: A picks Celsius, later B picks Fahrenheit. The changes cross
    // in either order.
    vi.setSystemTime(2_000_000);
    use(A);
    await choose('temp_unit', 'c');
    const fromA = await drain(A.db);
    vi.setSystemTime(3_000_000);
    use(B);
    await choose('temp_unit', 'f');
    const fromB = await drain(B.db);

    await deliver(B, fromA); // older: B keeps its own, newer choice
    expect(loadChoices().temp_unit?.v).toBe('f');
    await deliver(A, fromB);
    expect(loadChoices().temp_unit?.v).toBe('f');

    for (const dev of [A, B]) {
      use(dev);
      const rec = await readRecord('u1');
      expect(rec.weight_unit?.v).toBe('lb');
      expect(rec.temp_unit?.v).toBe('f');
      expect((await dev.db.all(`SELECT id FROM user_settings`)).length).toBe(1);
    }
    // Nothing was sent back and forth again.
    expect(await A.db.all(`SELECT * FROM outbox`)).toEqual([]);
    expect(await B.db.all(`SELECT * FROM outbox`)).toEqual([]);
  });

  it('switches the language by stamp, not by arrival', async () => {
    vi.setSystemTime(5_000_000);
    use(B);
    await choose('language', 'it');
    expect(applyLanguage).toHaveBeenLastCalledWith('it');
    applyLanguage.mockReset();

    // An older choice from another device arrives later: ignored.
    await deliver(B, [remote({ language: 'fr' }, stamp(4_000_000, 'devA'))]);
    expect(applyLanguage).not.toHaveBeenCalledWith('fr');
    expect(loadChoices().language?.v).toBe('it');
    expect((await readRecord('u1')).language?.v).toBe('it');

    // A newer one switches the app.
    await deliver(B, [remote({ language: 'de' }, stamp(6_000_000, 'devA'))]);
    expect(applyLanguage).toHaveBeenLastCalledWith('de');
    expect(loadChoices().language).toMatchObject({ v: 'de', u: 'u1' });
  });

  it('ignores values this app cannot apply', async () => {
    use(B);
    await deliver(B, [remote({ language: 'pt', weight_unit: 'st' }, stamp(6_000_000, 'devA'))]);
    expect(applyLanguage).not.toHaveBeenCalled();
    expect(loadChoices()).toEqual({});
  });
});

describe('two workspaces on one device', () => {
  it('sends a choice made in the other workspace once this one is complete, once', async () => {
    const X = A;
    use(X);
    await choose('weight_unit', 'lb'); // queued in workspace X, not sent yet
    const choice = loadChoices().weight_unit!;

    // The same device opens workspace Y: same storage, another database.
    const Y: Device = { storage: X.storage, db: await makeLocalDB() };
    use(Y);
    initialSyncPending.set(true); // the first download has not finished
    await reconcile();
    expect(await Y.db.all(`SELECT * FROM outbox`)).toEqual([]);
    expect(get(units).weight).toBe('lb'); // the device keeps showing it

    initialSyncPending.set(false); // downloaded: the record really lacks it
    await reconcile();
    const sent = await drain(Y.db);
    expect(sent).toEqual([
      {
        entity: 'user_settings',
        entityId: 'u1',
        op: 1,
        payloadJson: '{"weight_unit":"lb"}',
        hlc: choice.s
      }
    ]);
    // The scope is the personal one.
    await reconcile();
    expect(await Y.db.all(`SELECT * FROM outbox`)).toEqual([]);

    // Workspace X's copy arrives through the server: nothing changes.
    await deliver(Y, await drain(X.db));
    expect(await Y.db.all(`SELECT * FROM outbox`)).toEqual([]);
    expect(loadChoices().weight_unit).toEqual(choice);
  });

  it('never writes another account’s choice into the record', async () => {
    use(A);
    await choose('weight_unit', 'lb');
    // u2 signs in on the same device; its record has Celsius from long ago.
    A.storage.set('obh.userId', 'u2');
    const db2 = await makeLocalDB();
    use({ storage: A.storage, db: db2 });
    await deliver({ storage: A.storage, db: db2 }, [
      remote({ temp_unit: 'c', weight_unit: 'kg' }, stamp(10, 'old'), 'u2')
    ]);
    expect(loadChoices().weight_unit).toMatchObject({ v: 'kg', u: 'u2' });
    expect(await db2.all(`SELECT * FROM outbox`)).toEqual([]);
  });

  it('writes the scope and author of the signed-in account', async () => {
    use(A);
    await choose('temp_unit', 'f');
    const row = await A.db.get<any>(`SELECT scope_id, author_id FROM outbox`);
    expect(row).toEqual({ scope_id: 'user:u1', author_id: 'u1' });
  });
});

describe('decide', () => {
  const c = (v: string, s: string, u: string): Choice => ({ v, s, u });
  const rec = (v: string, s: string) => ({ v, s });
  const t1 = stamp(1, 'a');
  const t2 = stamp(2, 'a');

  it.each([
    ['no choice, record has a value', undefined, rec('lb', t1), true, 'adopt'],
    ['record newer than the choice', c('kg', t1, 'u1'), rec('lb', t2), true, 'adopt'],
    ['same stamp', c('lb', t1, 'u1'), rec('lb', t1), true, 'adopt'],
    ['choice newer, complete', c('kg', t2, 'u1'), rec('lb', t1), true, 'send'],
    ['choice newer, still downloading', c('kg', t2, 'u1'), rec('lb', t1), false, 'keep'],
    ['choice before sign-in is newer', c('kg', t2, ''), rec('lb', t1), true, 'keep'],
    ['another account chose later', c('kg', t2, 'u2'), rec('lb', t1), true, 'adopt'],
    ['record lacks it, own choice', c('kg', t1, 'u1'), undefined, true, 'send'],
    ['record lacks it, still downloading', c('kg', t1, 'u1'), undefined, false, 'keep'],
    ['record lacks it, another account', c('kg', t1, 'u2'), undefined, true, 'keep'],
    ['nothing anywhere', undefined, undefined, true, 'keep']
  ] as const)('%s', (_name, local, record, complete, want) => {
    expect(decide(local, record, 'u1', complete)).toBe(want);
  });
});
