// Removing a workspace's data from this device, for shared devices: the
// local database and every cached file that belongs to the workspace.
import { wipeLocalDB } from './local/db.js';

type Cleaner = (orgId: string) => Promise<void>;
const cleaners: Cleaner[] = [];

/**
 * Registers a clean-up for data a module keeps on the device per workspace
 * outside the local database (for example downloaded photos). It runs when
 * the user signs out with "also remove data from this device".
 */
export function onDeviceDataRemoval(cleaner: Cleaner): void {
  cleaners.push(cleaner);
}

/** File-safe form of a workspace id, as used in local file and cache names. */
export const storageId = (orgId: string): string => orgId.replace(/[^a-zA-Z0-9_-]/g, '_');

/** The Cache Storage caches that hold files of the workspace. */
export function workspaceCaches(names: string[], orgId: string): string[] {
  const id = storageId(orgId);
  return id ? names.filter((name) => name.includes(id)) : [];
}

/**
 * Deletes what this device stores for the workspace that is open: its local
 * database (including changes that were never uploaded), caches named after
 * it, and whatever registered cleaners look after. Each step is attempted
 * even if another fails; the first failure is thrown at the end.
 */
export async function removeDeviceData(orgId: string): Promise<void> {
  const steps: Promise<void>[] = [wipeLocalDB(), ...cleaners.map((clean) => clean(orgId))];
  if (typeof caches !== 'undefined') {
    steps.push(
      caches.keys().then(async (names) => {
        await Promise.all(workspaceCaches(names, orgId).map((name) => caches.delete(name)));
      })
    );
  }
  const failed = (await Promise.allSettled(steps)).find((s) => s.status === 'rejected');
  if (failed) throw (failed as PromiseRejectedResult).reason;
}
