import { describe, expect, it } from 'vitest';
import { makeLocalDB } from '../../tests/localdb';
import type { LocalDB } from './db';
import { accountStoreKey, mayUse, openOwnStore, otherAccountIn, OWNER_KEY } from './owner';

const ORG = 'org-1';

// A device: stores by key, created on first open, like the OPFS files.
function device() {
  const stores = new Map<string, LocalDB>();
  const opened: string[] = [];
  return {
    stores,
    opened,
    open: async (key: string) => {
      opened.push(key);
      if (!stores.has(key)) stores.set(key, await makeLocalDB());
      return stores.get(key)!;
    }
  };
}

async function queue(db: LocalDB, author: string, scope = 'apiary-1') {
  await db.exec(
    `INSERT INTO outbox (id, entity, entity_id, scope_id, op, payload, hlc, author_id)
     VALUES (?, 'task', ?, ?, 1, '{}', 'h', ?)`,
    [crypto.randomUUID(), crypto.randomUUID(), scope, author]
  );
}

const owner = async (db: LocalDB) =>
  (await db.get<{ v: string }>(`SELECT v FROM sync_meta WHERE k = ?`, [OWNER_KEY]))?.v;

describe('one local store per account and workspace', () => {
  it('gives the first account the workspace store and records it as the owner', async () => {
    const d = device();
    expect(await openOwnStore(d.open, ORG, 'alice', true)).toBe(ORG);
    expect(await owner(d.stores.get(ORG)!)).toBe('alice');
    // Alice comes back: same store.
    expect(await openOwnStore(d.open, ORG, 'alice', true)).toBe(ORG);
  });

  it("never hands another account's unsent changes to the next one", async () => {
    const d = device();
    await openOwnStore(d.open, ORG, 'alice', true);
    await queue(d.stores.get(ORG)!, 'alice');

    // Bob signs in to the same workspace on this device.
    const key = await openOwnStore(d.open, ORG, 'bob', true);
    expect(key).toBe(accountStoreKey(ORG, 'bob'));
    const bobs = d.stores.get(key)!;
    expect(await bobs.all(`SELECT * FROM outbox`)).toEqual([]);
    expect(await owner(bobs)).toBe('bob');
    // Alice's change waits for her in her store.
    expect(await d.stores.get(ORG)!.all(`SELECT * FROM outbox`)).toHaveLength(1);
    expect(await openOwnStore(d.open, ORG, 'alice', true)).toBe(ORG);
  });

  it('recognises a store from before owners were recorded by what another account left', async () => {
    const d = device();
    const old = await d.open(ORG);
    await queue(old, 'alice', 'user:alice');
    d.opened.length = 0;

    expect(await openOwnStore(d.open, ORG, 'bob', true)).toBe(accountStoreKey(ORG, 'bob'));
    expect(await owner(old)).toBe('alice');
    expect(d.opened).toEqual([ORG, accountStoreKey(ORG, 'bob')]);
  });

  it('lets the owner of an old store keep it', async () => {
    const d = device();
    await queue(await d.open(ORG), 'alice');
    expect(await openOwnStore(d.open, ORG, 'alice', true)).toBe(ORG);
  });

  it('does not let a guessed identity claim a store', async () => {
    const db = await makeLocalDB();
    expect(await mayUse(db, 'local', false)).toBe(true);
    expect(await owner(db)).toBeUndefined();
    expect(await mayUse(db, 'alice', true)).toBe(true);
    expect(await owner(db)).toBe('alice');
  });
});

describe('otherAccountIn', () => {
  it('finds personal records and settings of another account', async () => {
    const db = await makeLocalDB();
    expect(await otherAccountIn(db, 'bob')).toBe('');
    await db.exec(`INSERT INTO user_settings (id) VALUES ('bob')`);
    expect(await otherAccountIn(db, 'bob')).toBe('');
    await db.exec(`INSERT INTO event (id, scope_id) VALUES ('e1', 'user:alice')`);
    expect(await otherAccountIn(db, 'bob')).toBe('alice');
    expect(await otherAccountIn(db, 'alice')).toBe('bob');
  });

  it('ignores the single-user identity and changes without an author', async () => {
    const db = await makeLocalDB();
    await queue(db, 'local');
    await queue(db, '');
    expect(await otherAccountIn(db, 'bob')).toBe('');
  });
});
