// SvelteKit service worker. Precaches the app shell and serves navigations
// offline from cache. Data sync runs separately via the sync engine + local
// SQLite, not through this cache.
import { version } from '$app/env';
import { assets, immutable, prerendered } from '$app/manifest';
import { asset } from '$app/paths';
import { self } from '$app/service-worker';

const CACHE = `obh-shell-${version}`;
const ASSETS = [
  ...immutable.map((f) => f.path),
  ...prerendered.map((p) => p.path),
  ...assets.map((f) => asset(f.path))
];

self.addEventListener('install', (e) => {
  e.waitUntil(
    caches
      .open(CACHE)
      .then((c) => c.addAll(ASSETS))
      .then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (e) => {
  const req = e.request;
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
