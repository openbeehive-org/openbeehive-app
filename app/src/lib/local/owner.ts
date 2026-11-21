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
// gets a store of its own. Personal tasks do not name their account, so a
// store holding some is taken over only by an account that left traces of its
// own in it; anyone else gets a store of their own and the old one stays
// unclaimed. Every store records its owner in sync_meta.
//
// An unclaimed store that holds nothing unsent (no queued or failed changes,
// no photo waiting for upload) only repeats what the server has. Once the
// account's own store has finished its first sync, it is removed from the
// device (see discardUnclaimed); a store with anything unsent is kept.
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

/**
 * Whether the store holds personal tasks (tasks without an apiary) that
 * cannot be told apart from another account's: there are some, and nothing
 * in the store is traceably `user`'s.
 */
export async function unattributedPersonal(db: LocalDB, user: string): Promise<boolean> {
  const tasks = await db.get(`SELECT 1 AS x FROM task WHERE COALESCE(apiary_id, '') = '' LIMIT 1`);
  if (!tasks) return false;
  const mine = await db.get(
    `SELECT 1 AS x FROM outbox WHERE author_id = ?
     UNION ALL SELECT 1 FROM outbox_failed WHERE author_id = ?
     UNION ALL SELECT 1 FROM user_settings WHERE id = ?
     UNION ALL SELECT 1 FROM event WHERE scope_id = ?
     LIMIT 1`,
    [user, user, user, 'user:' + user]
  );
  return !mine;
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
  // Someone's personal tasks, perhaps not this account's: leave the store to
  // whoever can show it is theirs.
  if (!NOT_A_PERSON.includes(user) && (await unattributedPersonal(db, user))) return false;
  if (confirmed) await setOwner(db, user);
  return true;
}

/** Whether the store holds changes that never reached the server. */
export async function unsentIn(db: LocalDB): Promise<boolean> {
  const row = await db.get(
    `SELECT 1 AS x FROM outbox UNION ALL SELECT 1 FROM outbox_failed LIMIT 1`
  );
  return !!row;
}

/**
 * Opens the store `user` works with in workspace `org` and returns its key.
 * `open` closes whatever store was open before and opens (or creates) the
 * one with the given key. When the workspace's old store is passed over but
 * belongs to nobody and holds no unsent changes, `unclaimed` is told its key.
 */
export async function openOwnStore(
  open: (key: string) => Promise<LocalDB>,
  org: string,
  user: string,
  confirmed: boolean,
  unclaimed?: (key: string) => void
): Promise<string> {
  const shared = workspaceStoreKey(org);
  const old = await open(shared);
  if (await mayUse(old, user, confirmed)) return shared;
  if (confirmed && !(await ownerOf(old)) && !(await unsentIn(old))) unclaimed?.(shared);
  const own = accountStoreKey(org, user);
  const db = await open(own);
  if (confirmed && !(await ownerOf(db))) await setOwner(db, user);
  return own;
}

/**
 * Removes an unclaimed store (see openOwnStore) from the device, unless a
 * photo of it still waits for upload. Call it once the account's own store
 * has finished its first sync. Reports whether the store was removed.
 */
export async function discardUnclaimed(
  key: string,
  device: {
    /** Whether the store's photo images include one not yet uploaded. */
    photosPending: (key: string) => Promise<boolean>;
    /** Deletes the store's database file and its photo images. */
    remove: (key: string) => Promise<void>;
  }
): Promise<boolean> {
  if (await device.photosPending(key)) return false;
  await device.remove(key);
  return true;
}
