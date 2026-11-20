import { describe, it, expect } from 'vitest';
import { cacheName, isServerPath, precacheList, staleCaches } from './pwa';

describe('precacheList', () => {
  const files = {
    immutable: ['_app/immutable/entry/start.abc.js', '_app/immutable/workers/db.worker-1.js'],
    assets: ['favicon.svg', '/fonts/a.woff2'],
    emitted: [
      '_app/immutable/workers/db.worker-1.js',
      '_app/immutable/workers/sqlite3-worker1-2.js',
      '_app/immutable/workers/assets/sqlite3-3.wasm',
      '_app/immutable/workers/assets/sqlite3-opfs-async-proxy-4.js'
    ]
  };

  it('starts with the app shell', () => {
    expect(precacheList('/', files)[0]).toBe('/');
  });

  it('includes the SQLite files that only exist on disk', () => {
    const list = precacheList('/', files);
    expect(list).toContain('/_app/immutable/workers/assets/sqlite3-3.wasm');
    expect(list).toContain('/_app/immutable/workers/assets/sqlite3-opfs-async-proxy-4.js');
    expect(list).toContain('/_app/immutable/workers/sqlite3-worker1-2.js');
  });

  it('lists each file once, as a path under the app root', () => {
    const list = precacheList('/', files);
    expect(new Set(list).size).toBe(list.length);
    expect(list).toContain('/favicon.svg');
    expect(list).toContain('/fonts/a.woff2');
    expect(list.filter((u) => u.endsWith('db.worker-1.js'))).toHaveLength(1);
  });

  it('honours a base path', () => {
    expect(precacheList('/bees/', files)).toContain('/bees/_app/immutable/entry/start.abc.js');
    expect(precacheList('/bees/', files)[0]).toBe('/bees/');
  });
});

describe('isServerPath', () => {
  it('matches the API, auth, workspace, photo and health endpoints', () => {
    for (const p of [
      '/openbeehive.v1.SyncService/Pull',
      '/auth/instance',
      '/auth/callback',
      '/tenants/invite',
      '/photos/0b7c6d5e-1111-4222-8333-444455556666',
      '/healthz'
    ]) {
      expect(isServerPath(p), p).toBe(true);
    }
  });

  it('leaves app routes and assets to the worker', () => {
    for (const p of [
      '/',
      '/login',
      '/hives/123',
      '/h/abc',
      '/authors',
      '/photos',
      '/_app/immutable/x.js'
    ]) {
      expect(isServerPath(p), p).toBe(false);
    }
  });
});

describe('staleCaches', () => {
  it('returns the caches of other builds and nothing else', () => {
    const names = [cacheName('1'), cacheName('2'), 'someone-elses-cache'];
    expect(staleCaches(names, cacheName('2'))).toEqual([cacheName('1')]);
    expect(staleCaches([cacheName('2')], cacheName('2'))).toEqual([]);
  });
});
