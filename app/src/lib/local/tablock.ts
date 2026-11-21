// One tab at a time owns the local database: the OPFS file can only be
// opened once. The tab that does not get the lock shows a notice, and "Use
// it here" takes the lock over.
//
// Web Locks does the bookkeeping: the owner holds an exclusive lock for as
// long as it lives; a takeover requests the same lock with `steal`, which
// hands it over at once and rejects the previous owner's request, so that
// tab learns it has lost the database.

export const TAB_LOCK = 'openbeehive-local-db';
const TAKEOVER_FLAG = 'obh.takeover';

// owner: this tab holds the lock. blocked: another tab does.
// unsupported: the browser has no Web Locks; nothing is guarded.
export type TabClaim = 'owner' | 'blocked' | 'unsupported';

// Tries to become the owning tab. With steal the lock is taken from whoever
// holds it. onLost runs if this tab was the owner and another tab took over.
export function claimTab(
  locks: LockManager | undefined,
  steal: boolean,
  onLost: () => void
): Promise<TabClaim> {
  if (!locks) return Promise.resolve('unsupported');
  return new Promise<TabClaim>((resolve) => {
    let owner = false;
    locks
      .request(TAB_LOCK, steal ? { steal: true } : { ifAvailable: true }, (lock) => {
        if (!lock) {
          resolve('blocked');
          return undefined;
        }
        owner = true;
        resolve('owner');
        // Never settles: the lock is held until the tab closes or is robbed.
        return new Promise<void>(() => {});
      })
      .catch(() => {
        if (owner) onLost();
        else resolve('unsupported');
      });
  });
}

// "Use it here": reload this tab and let it steal the lock on the way up.
// A reload, not an in-place switch, because a tab that lost the lock has
// torn its database connection down.
export function requestTakeover() {
  sessionStorage.setItem(TAKEOVER_FLAG, '1');
  location.reload();
}

// True once after requestTakeover() reloaded the tab.
export function takeoverRequested(): boolean {
  try {
    const asked = sessionStorage.getItem(TAKEOVER_FLAG) === '1';
    sessionStorage.removeItem(TAKEOVER_FLAG);
    return asked;
  } catch {
    return false;
  }
}
