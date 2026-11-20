// Photos, as the UI uses them: add, delete, show.
//
// A photo is a synced record with a thumbnail (lib/local/repo.ts) plus a
// full-size image that travels separately (transfer.ts) and is kept on the
// device (files.ts). This module ties the two together for the workspace
// the device is signed in to.
import { derived, get, type Readable } from 'svelte/store';
import { storageState } from '../local/live';
import { photoRecords, type PhotoOwner, type PhotoRow } from '../local/repo';
import { syncStatus } from '../local/sync';
import type { PreparedPhoto } from '../image';
import { openFileStore } from './files';
import { httpTransport } from './http';
import { PhotoTransfers, type Unavailable, type UploadState } from './transfer';

export type { PhotoRow, PhotoOwner, Unavailable, UploadState };

const API_URL =
  import.meta.env.BEEHIVE_API_URL ??
  (typeof window !== 'undefined' ? window.location.origin : 'http://localhost:8080');

let instance: PhotoTransfers | null = null;

// One per page load: switching the workspace reloads the app.
function transfers(): PhotoTransfers {
  if (!instance) {
    instance = new PhotoTransfers(
      openFileStore(localStorage.getItem('obh.orgId') ?? 'local'),
      httpTransport({ baseUrl: API_URL, token: () => localStorage.getItem('session') }),
      { isLive: photoRecords.isLive, online: () => navigator.onLine }
    );
  }
  return instance;
}

/** Upload state per photo id, for photos whose image is still only here. */
export const uploadStates: Readable<ReadonlyMap<string, UploadState>> = {
  subscribe: (run, invalidate) => transfers().uploadStates.subscribe(run, invalidate)
};

/** Number of photos waiting for their upload. */
export const waitingUploads: Readable<number> = derived(uploadStates, (m) => m.size);

/**
 * Adds a photo to a record. The image is stored on the device before the
 * record is written, so a record never exists without its image; the upload
 * follows on its own. Returns the photo's id.
 */
export async function addPhoto(
  owner: { entity: PhotoOwner; id: string },
  prepared: PreparedPhoto,
  opts: { replace?: boolean } = {}
): Promise<string> {
  const id = crypto.randomUUID();
  const t = transfers();
  await t.keep(id, prepared.full);
  let replaced: string[];
  try {
    replaced = await photoRecords.create(
      id,
      owner,
      {
        thumb: prepared.thumb,
        content_type: prepared.contentType,
        size_bytes: prepared.full.size
      },
      opts
    );
  } catch (e) {
    await t.drop(id);
    throw e;
  }
  for (const old of replaced) await t.drop(old);
  if (replaced.length > 0) await photoRecords.eraseDeleted();
  void t.uploadDue();
  return id;
}

/** Sets a hive's picture; the previous one is deleted. */
export const setHivePicture = (hiveId: string, prepared: PreparedPhoto) =>
  addPhoto({ entity: 'hive', id: hiveId }, prepared, { replace: true });

/** Deletes a photo for good: here now, everywhere else once synced. */
export async function deletePhoto(id: string): Promise<void> {
  await photoRecords.remove(id);
  await transfers().drop(id);
}

/** The full-size image, from the device or fetched from the server. */
export const openPhoto = (id: string) => transfers().open(id);

/** Tries the uploads that are due now. */
export const uploadPhotosNow = () => transfers().uploadDue();

let started = false;

/**
 * Starts the background work: resume uploads left from earlier sessions,
 * retry on a timer and when the network returns, and tidy up after photos
 * that were deleted on other devices.
 */
export function startPhotoTransfers() {
  if (started || typeof window === 'undefined') return;
  started = true;
  const t = transfers();
  const tidy = async () => {
    await photoRecords.eraseDeleted();
    await t.reconcile();
  };
  const run = () => {
    // Another tab owns this device's data and does the uploading.
    if (get(storageState) === 'elsewhere') return;
    void uploadPhotosNow().catch((e) => console.warn('photo upload deferred:', e));
  };
  void tidy()
    .catch((e) => console.warn('photo store not tidied:', e))
    .then(run);
  setInterval(run, 30_000);
  window.addEventListener('online', run);
  // A finished sync means: records taken here have reached the server (their
  // uploads can go through now), and deletes from elsewhere have arrived.
  let last = '';
  syncStatus.subscribe((s) => {
    if (s.state === 'idle' && last !== 'idle') {
      t.resume();
      void tidy()
        .catch(() => undefined)
        .then(() => t.recordsSynced())
        .catch((e) => console.warn('photo upload deferred:', e));
    }
    last = s.state;
  });
}
