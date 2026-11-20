// Service worker: makes the installed app open without a network.
//
// - Install: cache the app shell (the page every route falls back to) and
//   every file of this build, including the SQLite WASM files the local
//   database needs. Nothing else is ever cached: API, sign-in and map requests
//   always go to the network.
// - Navigations: network first (so a deploy and fresh response headers take
//   effect), the cached shell when the server cannot be reached in time.
// - Updates: a new version installs next to the running one and waits. The
//   page shows "New version available"; only when the user reloads (or all
//   tabs are closed) does the new worker take over. The old cache stays until
//   no open page runs the old code, so its lazily loaded chunks keep working.
//
// Data sync is separate: it runs in the page through the sync engine and the
// local SQLite database, not through this cache.
import { version } from '$app/env';
import { assets, immutable } from '$app/manifest';
import { self } from '$app/service-worker';
import { files as emitted } from 'virtual:obh-build-files';
import { cacheName, isServerPath, MSG, precacheList, staleCaches } from '../lib/pwa';

const CACHE = cacheName(version);
// The app's base path with a trailing slash; also the URL of the app shell.
const ROOT = new URL(self.registration.scope).pathname;
const PRECACHE = precacheList(ROOT, {
  immutable: immutable.map((f) => f.path),
  assets: assets.map((f) => f.path),
  emitted
});
const PRECACHED = new Set(PRECACHE);
const IMMUTABLE = `${ROOT}_app/immutable/`;
// On a connection that is technically up but going nowhere, open the cached
// app instead of a blank page.
const NAVIGATION_TIMEOUT_MS = 4000;
const VERSION_REPLY_TIMEOUT_MS = 2000;

async function precache(): Promise<void> {
  const cache = await caches.open(CACHE);
  await Promise.all(
    PRECACHE.map(async (url) => {
      // Hashed files never change; the shell and static files must come from
      // the server, not from the browser's HTTP cache.
      const res = await fetch(url, { cache: url.startsWith(IMMUTABLE) ? 'default' : 'reload' });
      if (!res.ok) throw new Error(`precache ${url}: ${res.status}`);
      // A redirected response may not answer a navigation; store a plain copy.
      const copy = res.redirected
        ? new Response(res.body, {
            status: res.status,
            statusText: res.statusText,
            headers: res.headers
          })
        : res;
      await cache.put(url, copy);
    })
  );
}

const fromCache = async (url: string): Promise<Response | undefined> =>
  (await caches.open(CACHE)).match(url, { ignoreSearch: true, ignoreVary: true });

async function cacheFirst(req: Request): Promise<Response> {
  return (await fromCache(new URL(req.url).pathname)) ?? fetch(req);
}

async function navigate(req: Request): Promise<Response> {
  try {
    // Raced against a timer rather than aborted: the navigation request is
    // passed to fetch() untouched, so redirects behave as without a worker.
    const timeout = new Promise<never>((_, reject) =>
      setTimeout(() => reject(new Error('navigation timed out')), NAVIGATION_TIMEOUT_MS)
    );
    const res = await Promise.race([fetch(req), timeout]);
    // A server that is restarting behind its proxy answers 502/503/504.
    if (res.status >= 500) return (await fromCache(ROOT)) ?? res;
    return res;
  } catch {
    return (await fromCache(ROOT)) ?? Response.error();
  }
}

/** Asks a page which build it runs; null when it does not answer. */
function askVersion(client: Client): Promise<string | null> {
  return new Promise((resolve) => {
    const channel = new MessageChannel();
    const timer = setTimeout(() => resolve(null), VERSION_REPLY_TIMEOUT_MS);
    channel.port1.onmessage = (event) => {
      clearTimeout(timer);
      resolve(typeof event.data === 'string' ? event.data : null);
    };
    client.postMessage({ type: MSG.version }, [channel.port2]);
  });
}

/** Deletes the caches of other builds once every open page runs this one. */
async function prune(): Promise<void> {
  // A newer worker may already have filled its cache; that one must survive.
  if (self.registration.installing || self.registration.waiting) return;
  const stale = staleCaches(await caches.keys(), CACHE);
  if (!stale.length) return;
  const pages = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
  const versions = await Promise.all(pages.map(askVersion));
  if (versions.every((v) => v === version)) await Promise.all(stale.map((k) => caches.delete(k)));
}

// No skipWaiting here: with an older version still open, this worker waits
// until the page asks for it (see the message handler below).
self.addEventListener('install', (event) => {
  event.waitUntil(precache());
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      // Repair a cache that was cleared while this worker was waiting.
      if (!(await fromCache(ROOT))) {
        await precache().catch((err) =>
          console.warn('service worker: could not restore the offline cache', err)
        );
      }
      // On first install this lets the already open page work offline at once.
      await self.clients.claim();
      await prune();
    })()
  );
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  // Map tiles, the geocoder and any other origin: not ours to cache.
  if (url.origin !== self.location.origin || !url.pathname.startsWith(ROOT)) return;
  // API and sign-in endpoints always hit the network and are never stored.
  if (isServerPath('/' + url.pathname.slice(ROOT.length))) return;

  if (req.mode === 'navigate') {
    event.respondWith(navigate(req));
  } else if (PRECACHED.has(url.pathname)) {
    event.respondWith(cacheFirst(req));
  } else if (url.pathname.startsWith(IMMUTABLE)) {
    // A chunk of another build: a page still running the previous version
    // finds it in that version's cache.
    event.respondWith(caches.match(req, { ignoreVary: true }).then((hit) => hit ?? fetch(req)));
  }
  // Everything else is left to the browser.
});

self.addEventListener('message', (event) => {
  const type = (event.data as { type?: string } | null)?.type;
  if (type === MSG.skipWaiting) void self.skipWaiting();
  else if (type === MSG.version) event.ports[0]?.postMessage(version);
  else if (type === MSG.hello) event.waitUntil(prune());
});
