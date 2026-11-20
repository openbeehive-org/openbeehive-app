// Which local store a signed-in account uses on this device.
//
// Several people can sign in to the same workspace on one device (a shared
// tablet in the club house). Their local data must not mix: the outbox holds
// changes that are uploaded with the signed-in account's session, and
// personal records (tasks without an apiary, the account's settings) belong
// to one person. So the local store is kept per account and workspace: the
// SQLite file and the photo image store are both named by the key chosen here.
//
// Keeping the other person's store (instead of asking whether to discard it)
// means their unsent changes simply wait until they sign in here again, and
// nobody has to decide about someone else's work.
//
// Stores created before accounts were recorded are named after the
// workspace alone. Such a store belongs to whoever opens it first, unless it
// holds traces of another account (changes they queued, their settings, their
// personal events): then it is recorded as theirs and the account at hand
// gets a store of its own. Every store records its owner in sync_meta.
import type { LocalDB } from './db';

/** The sync_meta key that names the account a local store belongs to. */
export const OWNER_KEY = 'owner_user';

/** File-safe form of an id, as used in local file and cache names. */
export const safeName = (id: string): string => id.replace(/[^a-zA-Z0-9_-]/g, '_');

/** Key of the store shared by the workspace (the name used before accounts were recorded). */
export const workspaceStoreKey = (org: string): string => safeName(org);

/** Key of an account's own store in a workspace. */
export const accountStoreKey = (org: string, user: string): string =>
  `${safeName(org)}-user-${safeName(user)}`;

// "local" is the fixed identity of an instance without sign-in; it is never
// another person.
const NOT_A_PERSON = ['', 'local'];

/**
 * An account other than `user` that left data in this store, or '' if there
 * is none.
 */
export async function otherAccountIn(db: LocalDB, user: string): Promise<string> {
  const skip = [user, ...NOT_A_PERSON];
  const marks = skip.map(() => '?').join(', ');
  const personal = skip.map((u) => 'user:' + u);
  const row = await db.get<{ u: string }>(
    `SELECT author_id AS u FROM outbox WHERE author_id NOT IN (${marks})
     UNION ALL SELECT author_id FROM outbox_failed WHERE author_id NOT IN (${marks})
     UNION ALL SELECT substr(scope_id, 6) FROM outbox
       WHERE scope_id LIKE 'user:%' AND scope_id NOT IN (${marks})
     UNION ALL SELECT id FROM user_settings WHERE id NOT IN (${marks})
     UNION ALL SELECT substr(scope_id, 6) FROM event
       WHERE scope_id LIKE 'user:%' AND scope_id NOT IN (${marks})
     LIMIT 1`,
    [...skip, ...skip, ...personal, ...skip, ...personal]
  );
  return row?.u ?? '';
}

async function ownerOf(db: LocalDB): Promise<string> {
  const row = await db.get<{ v: string }>(`SELECT v FROM sync_meta WHERE k = ?`, [OWNER_KEY]);
  return row?.v ?? '';
}

async function setOwner(db: LocalDB, user: string): Promise<void> {
  await db.batch([
    {
      sql: `INSERT INTO sync_meta (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v`,
      params: [OWNER_KEY, user]
    }
  ]);
}

/**
 * Whether the store may be used by `user`. An unowned store is recorded as
 * the user's, or as the other account's whose data it holds. `confirmed`
 * says the identity comes from the server or a sign-in; a guessed one never
 * claims a store.
 */
export async function mayUse(db: LocalDB, user: string, confirmed: boolean): Promise<boolean> {
  const owner = await ownerOf(db);
  if (owner) return owner === user;
  const other = await otherAccountIn(db, user);
  if (other) {
    await setOwner(db, other);
    return false;
  }
  if (confirmed) await setOwner(db, user);
  return true;
}

/**
 * Opens the store `user` works with in workspace `org` and returns its key.
 * `open` closes whatever store was open before and opens (or creates) the
 * one with the given key.
 */
export async function openOwnStore(
  open: (key: string) => Promise<LocalDB>,
  org: string,
  user: string,
  confirmed: boolean
): Promise<string> {
  const shared = workspaceStoreKey(org);
  if (await mayUse(await open(shared), user, confirmed)) return shared;
  const own = accountStoreKey(org, user);
  const db = await open(own);
  if (confirmed && !(await ownerOf(db))) await setOwner(db, user);
  return own;
}
