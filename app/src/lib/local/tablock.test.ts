import { describe, it, expect, vi, beforeEach } from 'vitest';
import { claimTab, requestTakeover, takeoverRequested, TAB_LOCK } from './tablock';

// Web Locks as far as the tab lock uses it: one exclusive lock, ifAvailable
// and steal. A stolen lock rejects its holder's request with an AbortError.
function fakeLocks() {
  let holder: { reject: (e: Error) => void } | null = null;
  const request = (name: string, opts: LockOptions, cb: (lock: Lock | null) => unknown) =>
    new Promise<unknown>((resolve, reject) => {
      expect(name).toBe(TAB_LOCK);
      if (holder && !opts.steal) {
        expect(opts.ifAvailable).toBe(true);
        resolve(cb(null));
        return;
      }
      holder?.reject(new DOMException('stolen', 'AbortError'));
      holder = { reject };
      Promise.resolve(cb({ name, mode: 'exclusive' })).then(resolve, reject);
    });
  return { request } as unknown as LockManager;
}

describe('one tab owns the local database', () => {
  it('the first tab becomes the owner, a second one is blocked', async () => {
    const locks = fakeLocks();
    const lost = vi.fn();
    expect(await claimTab(locks, false, lost)).toBe('owner');
    expect(await claimTab(locks, false, vi.fn())).toBe('blocked');
    expect(lost).not.toHaveBeenCalled();
  });

  it('"use it here" takes the lock over and tells the previous owner', async () => {
    const locks = fakeLocks();
    const firstLost = vi.fn();
    const secondLost = vi.fn();
    expect(await claimTab(locks, false, firstLost)).toBe('owner');

    expect(await claimTab(locks, true, secondLost)).toBe('owner');
    await Promise.resolve();
    expect(firstLost).toHaveBeenCalledOnce();
    expect(secondLost).not.toHaveBeenCalled();

    // And back again: the first tab can take it back the same way.
    expect(await claimTab(locks, true, vi.fn())).toBe('owner');
    await Promise.resolve();
    expect(secondLost).toHaveBeenCalledOnce();
  });

  it('is unguarded where the browser has no Web Locks or refuses them', async () => {
    expect(await claimTab(undefined, false, vi.fn())).toBe('unsupported');
    const refusing = {
      request: () => Promise.reject(new DOMException('insecure', 'SecurityError'))
    } as unknown as LockManager;
    const lost = vi.fn();
    expect(await claimTab(refusing, false, lost)).toBe('unsupported');
    expect(lost).not.toHaveBeenCalled();
  });
});

describe('takeover request', () => {
  beforeEach(() => {
    const store = new Map<string, string>();
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k)
    });
  });

  it('survives exactly one reload', () => {
    const reload = vi.fn();
    vi.stubGlobal('location', { reload });
    expect(takeoverRequested()).toBe(false);

    requestTakeover();
    expect(reload).toHaveBeenCalledOnce();
    expect(takeoverRequested()).toBe(true); // the reloaded tab steals the lock
    expect(takeoverRequested()).toBe(false); // a later reload does not
    vi.unstubAllGlobals();
  });

  it('is simply absent where sessionStorage is blocked', () => {
    vi.stubGlobal('sessionStorage', {
      getItem: () => {
        throw new Error('blocked');
      }
    });
    expect(takeoverRequested()).toBe(false);
    vi.unstubAllGlobals();
  });
});
