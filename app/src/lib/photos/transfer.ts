// Moves full-size photos between this device and the server.
//
// Taking a photo never waits for the network: the image goes into the
// device store as "pending" and the record (with its thumbnail) into the
// local database, which syncs on its own. From here on this module gets the
// image to the server: when the device is online, again after every failure
// (with growing pauses), and again after a restart, for as long as the entry
// is pending. Other devices fetch the image the first time someone opens the
// photo and keep it as a cached copy.
import { writable, type Readable } from 'svelte/store';
import type { FileMeta, FileStore } from './files';

/** What the server said to an upload. */
export type UploadResult =
  | 'stored' // it has the image now (or had it already)
  | 'unknown' // it does not know the photo yet: its record has not synced
  | 'deleted' // the photo was deleted meanwhile
  | 'read_only' // this account may not upload (demo, read-only access)
  | 'refused' // it will never take this image (too large, not an image)
  | 'signed_out' // the session is not valid
  | 'retry'; // offline, or a server problem: try later

/** What the server said to a download. */
export type DownloadResult =
  | { blob: Blob }
  | 'missing' // not uploaded yet, or no such photo
  | 'signed_out'
  | 'retry';

export interface Transport {
  upload(id: string, image: Blob): Promise<UploadResult>;
  download(id: string): Promise<DownloadResult>;
}

/** Why a full-size photo cannot be shown right now. */
export type Unavailable = 'offline' | 'not_uploaded' | 'error';

/** What the UI shows next to a photo: nothing, or that its upload is due. */
export type UploadState = 'waiting' | 'failing' | 'blocked';

export interface TransferOptions {
  /** Unix ms; replaceable in tests. */
  now?: () => number;
  online?: () => boolean;
  /** Bytes the cached copies may take together. */
  cacheBudget?: number;
  /**
   * Whether the photo still exists (its record is on this device and not
   * deleted). Images of photos that do not are removed.
   */
  isLive: (id: string) => Promise<boolean>;
}

// Kept on the device for offline viewing; pending uploads do not count.
const DEFAULT_CACHE_BUDGET = 200 * 1024 * 1024;

const MINUTE = 60_000;
// Pauses between upload attempts.
const RETRY_FIRST = 5_000;
const RETRY_MAX = 10 * MINUTE;
// The record usually syncs within seconds; do not back off far.
const RETRY_UNKNOWN_MAX = MINUTE;
// Nothing changes quickly about these; ask again now and then.
const RETRY_BLOCKED = 60 * MINUTE;

function pause(attempts: number, max: number): number {
  return Math.min(max, RETRY_FIRST * 2 ** Math.min(attempts, 16));
}

export class PhotoTransfers {
  private readonly now: () => number;
  private readonly online: () => boolean;
  private readonly budget: number;
  private readonly states = writable<ReadonlyMap<string, UploadState>>(new Map());
  private uploading: Promise<void> | null = null;
  private again = false;
  private signedOut = false;

  constructor(
    private readonly store: FileStore,
    private readonly transport: Transport,
    private readonly opts: TransferOptions
  ) {
    this.now = opts.now ?? (() => Date.now());
    this.online = opts.online ?? (() => true);
    this.budget = opts.cacheBudget ?? DEFAULT_CACHE_BUDGET;
  }

  /** Photos whose image has not reached the server, by photo id. */
  get uploadStates(): Readable<ReadonlyMap<string, UploadState>> {
    return { subscribe: this.states.subscribe };
  }

  private async publish() {
    const map = new Map<string, UploadState>();
    for (const m of await this.store.list()) {
      if (m.state !== 'pending') continue;
      map.set(
        m.id,
        m.error === 'read_only' || m.error === 'refused'
          ? 'blocked'
          : m.error
            ? 'failing'
            : 'waiting'
      );
    }
    this.states.set(map);
  }

  /**
   * Keeps the image of a photo that was just taken. Resolves once it is
   * stored for good; only then may the photo's record be written.
   */
  async keep(id: string, image: Blob): Promise<void> {
    const meta: FileMeta = {
      id,
      state: 'pending',
      size: image.size,
      usedAt: this.now(),
      attempts: 0,
      nextTry: 0,
      error: ''
    };
    try {
      await this.store.put(meta, image);
    } catch (e) {
      // Out of space: cached copies can be fetched again, this image cannot.
      if (!(await this.evict(0))) throw e;
      await this.store.put(meta, image);
    }
    await this.publish();
  }

  /**
   * The server may have lost images it had (it was restored from a backup):
   * every image kept here is uploaded again. An image the server still has
   * is not stored twice.
   */
  async uploadAllAgain(): Promise<void> {
    for (const m of await this.store.list()) {
      if (m.state !== 'cached') continue;
      await this.store.update(m.id, { state: 'pending', attempts: 0, nextTry: 0, error: '' });
    }
    await this.publish();
  }

  /** Forgets the image of a photo (it was deleted, or never came to be). */
  async drop(id: string): Promise<void> {
    await this.store.delete(id);
    await this.publish();
  }

  /**
   * Brings the store in line with the photo records. Run at start: an entry
   * whose photo does not exist is what a tab that died between storing the
   * image and writing the record left behind, or a photo deleted elsewhere.
   */
  async reconcile(): Promise<void> {
    for (const m of await this.store.list()) {
      if (!(await this.opts.isLive(m.id))) await this.store.delete(m.id);
    }
    await this.publish();
  }

  /**
   * Uploads every pending image that is due. Calls made while a run is in
   * flight are folded into one more run after it.
   */
  uploadDue(): Promise<void> {
    if (this.uploading) {
      this.again = true;
      return this.uploading;
    }
    this.uploading = (async () => {
      try {
        do {
          this.again = false;
          await this.uploadOnce();
        } while (this.again);
      } finally {
        this.uploading = null;
      }
    })();
    return this.uploading;
  }

  /** The session works (again): uploads may be tried. */
  resume(): void {
    this.signedOut = false;
  }

  /**
   * The records of this device have reached the server: uploads that were
   * waiting for theirs are due now.
   */
  async recordsSynced(): Promise<void> {
    for (const m of await this.store.list()) {
      if (m.state === 'pending' && m.error === '' && m.nextTry > 0) {
        await this.store.update(m.id, { nextTry: 0 });
      }
    }
    await this.uploadDue();
  }

  private async uploadOnce() {
    if (this.signedOut || !this.online()) return;
    const due = (await this.store.list())
      .filter((m) => m.state === 'pending' && m.nextTry <= this.now())
      .sort((a, b) => a.usedAt - b.usedAt);
    for (const m of due) {
      const image = await this.store.get(m.id);
      if (!image) {
        await this.store.delete(m.id); // a record without its image is of no use
        continue;
      }
      const result = await this.transport.upload(m.id, image).catch((): UploadResult => 'retry');
      if (!(await this.settle(m, result))) break;
    }
    await this.publish();
  }

  // Records the outcome of one upload. Returns false when the rest of the
  // run cannot succeed either.
  private async settle(m: FileMeta, result: UploadResult): Promise<boolean> {
    const attempts = m.attempts + 1;
    const later = (error: string, wait: number) =>
      this.store.update(m.id, { attempts, error, nextTry: this.now() + wait });
    switch (result) {
      case 'stored':
        await this.store.update(m.id, { state: 'cached', error: '', usedAt: this.now() });
        await this.evict(this.budget);
        return true;
      case 'deleted':
        await this.store.delete(m.id);
        return true;
      case 'unknown':
        // Not a failure the user needs to hear about.
        await this.store.update(m.id, {
          attempts,
          nextTry: this.now() + pause(m.attempts, RETRY_UNKNOWN_MAX)
        });
        return true;
      case 'read_only':
      case 'refused':
        await later(result, RETRY_BLOCKED);
        return true;
      case 'signed_out':
        this.signedOut = true;
        return false;
      case 'retry':
        await later('retry', pause(m.attempts, RETRY_MAX));
        // The server or the network is the problem, not this image.
        return false;
    }
  }

  /**
   * The full-size image of a photo: from the device if it is here, else
   * fetched and kept for next time.
   */
  async open(id: string): Promise<{ blob: Blob } | { unavailable: Unavailable }> {
    const local = await this.store.get(id);
    if (local) {
      await this.store.update(id, { usedAt: this.now() });
      return { blob: local };
    }
    if (!this.online()) return { unavailable: 'offline' };
    const result = await this.transport.download(id).catch((): DownloadResult => 'retry');
    if (result === 'missing') return { unavailable: 'not_uploaded' };
    if (result === 'signed_out' || result === 'retry') {
      return { unavailable: this.online() ? 'error' : 'offline' };
    }
    try {
      await this.store.put(
        {
          id,
          state: 'cached',
          size: result.blob.size,
          usedAt: this.now(),
          attempts: 0,
          nextTry: 0,
          error: ''
        },
        result.blob
      );
      await this.evict(this.budget);
    } catch (e) {
      // Showing the photo matters more than keeping a copy of it.
      console.warn('photo not cached:', e);
    }
    return { blob: result.blob };
  }

  /**
   * The full-size image for an export: from the device if it is here, else
   * fetched without keeping a copy (an export must not push the photos
   * the user looks at out of the cache).
   */
  async read(id: string): Promise<{ blob: Blob } | { unavailable: Unavailable }> {
    const local = await this.store.get(id);
    if (local) return { blob: local };
    if (!this.online()) return { unavailable: 'offline' };
    const result = await this.transport.download(id).catch((): DownloadResult => 'retry');
    if (result === 'missing') return { unavailable: 'not_uploaded' };
    if (result === 'signed_out' || result === 'retry') {
      return { unavailable: this.online() ? 'error' : 'offline' };
    }
    return result;
  }

  /**
   * Removes the least recently used cached copies until the cache is within
   * `budget` bytes. Pending images are never touched. Returns whether
   * anything was removed.
   */
  async evict(budget: number): Promise<boolean> {
    const cached = (await this.store.list())
      .filter((m) => m.state === 'cached')
      .sort((a, b) => a.usedAt - b.usedAt);
    let total = cached.reduce((sum, m) => sum + m.size, 0);
    let removed = false;
    for (const m of cached) {
      if (total <= budget) break;
      await this.store.delete(m.id);
      total -= m.size;
      removed = true;
    }
    return removed;
  }
}
