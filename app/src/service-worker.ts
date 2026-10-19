/// <reference lib="webworker" />
// SvelteKit service worker. Precaches the app shell and serves navigations
// offline from cache. Data sync runs separately via the sync engine + local
// SQLite, not through this cache.

import { build, files, version } from '$service-worker';

const CACHE = `obh-shell-${version}`;
const ASSETS = [...build, ...files];

self.addEventListener('install', (e: any) => {
  e.waitUntil(
    caches
      .open(CACHE)
      .then((c) => c.addAll(ASSETS))
      .then(() => (self as any).skipWaiting())
  );
});

self.addEventListener('activate', (e: any) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => (self as any).clients.claim())
  );
});

self.addEventListener('fetch', (e: any) => {
  const req = e.request as Request;
  if (req.method !== 'GET') return;

  // Never cache API calls - they go through the sync engine.
  const url = new URL(req.url);
  if (url.pathname.startsWith('/openbeehive.v1.')) return;

  // Cache-first for assets, network fallback, offline -> app shell.
  e.respondWith(
    caches
      .match(req)
      .then((hit) => hit ?? fetch(req).catch(() => caches.match('/') as Promise<Response>))
  );
});
