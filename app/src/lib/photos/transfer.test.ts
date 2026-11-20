import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import { MemoryFileStore, type FileMeta, type FileStore } from './files';
import {
  PhotoTransfers,
  type DownloadResult,
  type Transport,
  type UploadResult,
  type UploadState
} from './transfer';

const SECOND = 1000;
const MINUTE = 60 * SECOND;

const image = (bytes: number, fill = 'x') => new Blob([fill.repeat(bytes)], { type: 'image/jpeg' });

// A server that can be told what to answer, and remembers what it was sent.
class FakeServer implements Transport {
  stored = new Map<string, Blob>();
  uploads: string[] = [];
  downloads: string[] = [];
  /** What the next uploads are answered with; 'stored' once exhausted. */
  answers: UploadResult[] = [];
  /** Throw like fetch does without a network. */
  unreachable = false;

  async upload(id: string, blob: Blob): Promise<UploadResult> {
    this.uploads.push(id);
    if (this.unreachable) throw new TypeError('Failed to fetch');
    const answer = this.answers.shift() ?? 'stored';
    if (answer === 'stored') this.stored.set(id, blob);
    return answer;
  }
  async download(id: string): Promise<DownloadResult> {
    this.downloads.push(id);
    if (this.unreachable) throw new TypeError('Failed to fetch');
    const blob = this.stored.get(id);
    return blob ? { blob } : 'missing';
  }
}

// One device: its store survives "restarts" (a new PhotoTransfers over it).
class Device {
  store: FileStore = new MemoryFileStore();
  server: FakeServer;
  clock = 1_000_000;
  online = true;
  live = new Set<string>();
  budget = 1000;
  transfers!: PhotoTransfers;

  constructor(server = new FakeServer()) {
    this.server = server;
    this.start();
  }
  /** What reopening the app does: fresh in-memory state over the same store. */
  start() {
    this.transfers = new PhotoTransfers(this.store, this.server, {
      now: () => this.clock,
      online: () => this.online,
      cacheBudget: this.budget,
      isLive: async (id) => this.live.has(id)
    });
  }
  /** Takes a photo the way the app does: image first, then the record. */
  async take(id: string, bytes = 100) {
    await this.transfers.keep(id, image(bytes));
    this.live.add(id);
  }
  async meta(id: string): Promise<FileMeta | undefined> {
    return (await this.store.list()).find((m) => m.id === id);
  }
  states(): Record<string, UploadState> {
    return Object.fromEntries(get(this.transfers.uploadStates));
  }
}

let d: Device;
beforeEach(() => {
  d = new Device();
});

describe('taking photos offline', () => {
  it('keeps the image on the device and shows it as waiting', async () => {
    d.online = false;
    await d.take('p1');
    await d.transfers.uploadDue();

    expect(d.server.uploads).toEqual([]);
    expect(await d.meta('p1')).toMatchObject({ state: 'pending', size: 100, attempts: 0 });
    expect(d.states()).toEqual({ p1: 'waiting' });
    // The full image can be looked at without a network.
    const opened = await d.transfers.open('p1');
    expect('blob' in opened && opened.blob.size).toBe(100);
    expect(d.server.downloads).toEqual([]);
  });

  it('uploads once the device is online again, oldest first', async () => {
    d.online = false;
    await d.take('p1');
    d.clock += SECOND;
    await d.take('p2');

    d.online = true;
    await d.transfers.uploadDue();

    expect(d.server.uploads).toEqual(['p1', 'p2']);
    expect(d.server.stored.get('p1')?.size).toBe(100);
    expect(await d.meta('p1')).toMatchObject({ state: 'cached', error: '' });
    expect(d.states()).toEqual({});
  });
});

describe('retrying', () => {
  it('waits longer after every failure and stops at ten minutes', async () => {
    await d.take('p1');
    d.server.unreachable = true;
    const waits: number[] = [];
    for (let i = 0; i < 12; i++) {
      await d.transfers.uploadDue();
      const m = (await d.meta('p1'))!;
      expect(m.state).toBe('pending');
      expect(m.attempts).toBe(i + 1);
      waits.push(m.nextTry - d.clock);
      d.clock = m.nextTry;
    }
    expect(waits.slice(0, 4)).toEqual([5 * SECOND, 10 * SECOND, 20 * SECOND, 40 * SECOND]);
    expect(Math.max(...waits)).toBe(10 * MINUTE);
    expect(d.states()).toEqual({ p1: 'failing' });

    d.server.unreachable = false;
    await d.transfers.uploadDue();
    expect(await d.meta('p1')).toMatchObject({ state: 'cached', error: '' });
    expect(d.states()).toEqual({});
  });

  it('does not try again before the pause is over', async () => {
    await d.take('p1');
    d.server.answers = ['retry'];
    await d.transfers.uploadDue();
    await d.transfers.uploadDue();
    d.clock += 4 * SECOND;
    await d.transfers.uploadDue();
    expect(d.server.uploads).toEqual(['p1']);
    d.clock += SECOND;
    await d.transfers.uploadDue();
    expect(d.server.uploads).toEqual(['p1', 'p1']);
  });

  it('gives up the run when the server cannot be reached, not one photo at a time', async () => {
    await d.take('p1');
    d.clock += 1;
    await d.take('p2');
    d.server.unreachable = true;
    await d.transfers.uploadDue();
    expect(d.server.uploads).toEqual(['p1']);
    // The one that was not tried is due at once when the network is back.
    d.server.unreachable = false;
    await d.transfers.uploadDue();
    expect(d.server.uploads).toEqual(['p1', 'p2']);
  });

  it('waits for the record when the server does not know the photo yet', async () => {
    await d.take('p1');
    d.server.answers = ['unknown'];
    await d.transfers.uploadDue();
    expect(await d.meta('p1')).toMatchObject({ state: 'pending', error: '', attempts: 1 });
    expect(d.states()).toEqual({ p1: 'waiting' }); // not a failure to show

    // The sync that carries the record has finished: no need to sit out the pause.
    await d.transfers.recordsSynced();
    expect(d.server.uploads).toEqual(['p1', 'p1']);
    expect(await d.meta('p1')).toMatchObject({ state: 'cached' });
  });

  it('drops the image when the photo was deleted meanwhile', async () => {
    await d.take('p1');
    d.server.answers = ['deleted'];
    await d.transfers.uploadDue();
    expect(await d.meta('p1')).toBeUndefined();
    expect(await d.store.get('p1')).toBeUndefined();
    expect(d.states()).toEqual({});
  });

  it('keeps an image the account may not upload, and asks again later', async () => {
    await d.take('p1');
    d.clock += 1;
    await d.take('p2');
    d.server.answers = ['read_only', 'refused'];
    await d.transfers.uploadDue();
    expect(d.states()).toEqual({ p1: 'blocked', p2: 'blocked' });
    expect(await d.store.get('p1')).toBeDefined();

    d.clock += 59 * MINUTE;
    await d.transfers.uploadDue();
    expect(d.server.uploads).toHaveLength(2);
    d.clock += 2 * MINUTE;
    await d.transfers.uploadDue();
    expect(d.server.uploads).toHaveLength(4);
    expect(d.states()).toEqual({});
  });

  it('stops while signed out and continues after sign-in', async () => {
    await d.take('p1');
    d.clock += 1;
    await d.take('p2');
    d.server.answers = ['signed_out'];
    await d.transfers.uploadDue();
    await d.transfers.uploadDue();
    expect(d.server.uploads).toEqual(['p1']);
    expect(await d.meta('p1')).toMatchObject({ state: 'pending', attempts: 0 });

    d.transfers.resume();
    await d.transfers.uploadDue();
    expect(d.server.uploads).toEqual(['p1', 'p1', 'p2']);
  });

  it('runs one upload per image when triggered many times at once', async () => {
    await d.take('p1');
    await Promise.all([d.transfers.uploadDue(), d.transfers.uploadDue(), d.transfers.uploadDue()]);
    expect(d.server.uploads).toEqual(['p1']);
  });
});

describe('a tab killed halfway', () => {
  it('after the image was stored but before the record was written: the image goes', async () => {
    await d.transfers.keep('p1', image(100)); // the tab dies here: no record
    await d.take('p2');

    d.start();
    await d.transfers.reconcile();
    expect(await d.meta('p1')).toBeUndefined();
    expect(await d.store.get('p1')).toBeUndefined();
    expect(await d.meta('p2')).toMatchObject({ state: 'pending' });
    await d.transfers.uploadDue();
    expect(d.server.uploads).toEqual(['p2']);
  });

  it('after the record was written but before the upload: the upload resumes', async () => {
    d.online = false;
    await d.take('p1');

    d.start(); // the app is opened again, now online
    d.online = true;
    await d.transfers.reconcile();
    expect(d.states()).toEqual({ p1: 'waiting' });
    await d.transfers.uploadDue();
    expect(d.server.stored.has('p1')).toBe(true);
    expect(d.states()).toEqual({});
  });

  it('after the upload but before it was noted: uploading again is harmless', async () => {
    await d.take('p1');
    d.server.stored.set('p1', image(100)); // the server has it; the device never heard
    d.start();
    await d.transfers.reconcile();
    await d.transfers.uploadDue();
    expect(d.server.uploads).toEqual(['p1']);
    expect(await d.meta('p1')).toMatchObject({ state: 'cached' });
  });

  it('removes images of photos that were deleted on another device', async () => {
    await d.take('p1');
    await d.transfers.uploadDue();
    d.live.delete('p1'); // the delete arrived with a sync
    await d.transfers.reconcile();
    expect(await d.store.get('p1')).toBeUndefined();
  });
});

describe('viewing on another device', () => {
  it('fetches the full image on first view and keeps it', async () => {
    const camera = new Device();
    await camera.take('p1', 300);
    await camera.transfers.uploadDue();

    const viewer = new Device(camera.server);
    viewer.live.add('p1');
    const first = await viewer.transfers.open('p1');
    expect('blob' in first && first.blob.size).toBe(300);
    expect(viewer.server.downloads).toEqual(['p1']);
    expect(await viewer.meta('p1')).toMatchObject({ state: 'cached', size: 300 });
    expect(viewer.states()).toEqual({}); // nothing to upload from here

    // Second view, and offline: from the device.
    viewer.online = false;
    const again = await viewer.transfers.open('p1');
    expect('blob' in again && again.blob.size).toBe(300);
    expect(viewer.server.downloads).toEqual(['p1']);
  });

  it('says why a full image cannot be shown', async () => {
    d.live.add('p1');
    expect(await d.transfers.open('p1')).toEqual({ unavailable: 'not_uploaded' });
    d.online = false;
    expect(await d.transfers.open('p1')).toEqual({ unavailable: 'offline' });
    d.online = true;
    d.server.unreachable = true;
    expect(await d.transfers.open('p1')).toEqual({ unavailable: 'error' });
    expect(await d.meta('p1')).toBeUndefined();
  });

  it('still shows a photo it cannot keep', async () => {
    d.server.stored.set('p1', image(50));
    d.store.put = async () => {
      throw new DOMException('full', 'QuotaExceededError');
    };
    const res = await d.transfers.open('p1');
    expect('blob' in res && res.blob.size).toBe(50);
  });
});

describe('making room', () => {
  // Budget 1000 bytes in these tests.
  async function cache(id: string, bytes: number) {
    d.server.stored.set(id, image(bytes));
    d.live.add(id);
    await d.transfers.open(id);
    d.clock += SECOND;
  }

  it('drops the cached copies that were used longest ago', async () => {
    await cache('a', 400);
    await cache('b', 400);
    await d.transfers.open('a'); // a is the most recently used now
    d.clock += SECOND;
    await cache('c', 400); // 1200 > 1000: one has to go, and it is b

    expect((await d.store.list()).map((m) => m.id).sort()).toEqual(['a', 'c']);
  });

  it('never removes an image that has not been uploaded', async () => {
    d.online = false;
    await d.take('mine-1', 600);
    await d.take('mine-2', 600); // 1200 bytes pending: over budget, and that is fine
    d.online = true;
    d.server.unreachable = true;
    await d.transfers.uploadDue();

    d.server.unreachable = false;
    d.server.answers = ['retry'];
    await cache('a', 400);
    await cache('b', 400);
    await cache('c', 400);
    expect(await d.transfers.evict(0)).toBe(true);

    const left = await d.store.list();
    expect(left.map((m) => m.id).sort()).toEqual(['mine-1', 'mine-2']);
    expect(left.every((m) => m.state === 'pending')).toBe(true);
    expect((await d.store.get('mine-1'))?.size).toBe(600);
    // Nothing cached is left: there is nothing more to remove.
    expect(await d.transfers.evict(0)).toBe(false);
  });

  it('an uploaded image becomes a cached copy like any other', async () => {
    await d.take('mine', 600);
    await d.transfers.uploadDue();
    d.clock += SECOND;
    await cache('a', 300);
    await cache('b', 300); // 1200: the oldest cached copy is the uploaded one
    expect((await d.store.list()).map((m) => m.id).sort()).toEqual(['a', 'b']);
  });

  it('frees cached copies when the device is full, to keep a new photo', async () => {
    await cache('a', 400);
    const put = d.store.put.bind(d.store);
    let full = true;
    d.store.put = async (meta, data) => {
      if (full) {
        full = false;
        throw new DOMException('full', 'QuotaExceededError');
      }
      await put(meta, data);
    };
    await d.take('mine', 100);
    expect(await d.meta('a')).toBeUndefined();
    expect(await d.meta('mine')).toMatchObject({ state: 'pending' });
  });

  it('reports a full device when nothing can be freed', async () => {
    d.store.put = async () => {
      throw new DOMException('full', 'QuotaExceededError');
    };
    await expect(d.transfers.keep('mine', image(100))).rejects.toThrow('full');
    expect(await d.store.list()).toEqual([]);
  });
});
