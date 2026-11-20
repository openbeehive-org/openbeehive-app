// Rules shared by the service worker and the page, kept free of browser
// globals so they can be unit-tested: what goes into the offline cache, which
// requests the worker must leave alone, and how the two sides talk.

export const CACHE_PREFIX = 'obh-shell-';

/** One cache per build, so two versions can coexist while an update is pending. */
export const cacheName = (version: string): string => `${CACHE_PREFIX}${version}`;

/** Message types exchanged between page and worker. */
export const MSG = {
  /** page -> waiting worker: take over now (the page reloads right after). */
  skipWaiting: 'obh:skip-waiting',
  /** either direction, with a MessagePort: reply with your build version. */
  version: 'obh:version?',
  /** page -> worker: a page has started; a good moment to drop unused caches. */
  hello: 'obh:hello'
} as const;

// Paths the Go server answers itself: the Connect API, sign-in and workspace
// endpoints, full-size photos and the health check. They are never cached
// here and never replaced by the offline app shell. (Photos have their own
// store on the device, lib/photos.)
const SERVER_PREFIXES = ['/openbeehive.v1.', '/auth/', '/tenants/', '/photos/'];
const SERVER_PATHS = ['/healthz'];

/** `pathname` is relative to the app root (starts with "/"). */
export function isServerPath(pathname: string): boolean {
  return SERVER_PATHS.includes(pathname) || SERVER_PREFIXES.some((p) => pathname.startsWith(p));
}

export type BuildFiles = {
  /** Content-hashed files Vite knows about ($app/manifest `immutable`). */
  immutable: string[];
  /** Files from static/ ($app/manifest `assets`). */
  assets: string[];
  /**
   * Every file found under the immutable directory after the client build.
   * Vite's manifest omits what a worker emits (the SQLite WASM binary and its
   * helper scripts), and the local database cannot start without those.
   */
  emitted: string[];
};

/**
 * URLs to cache at install time: the app shell (`root`, the page every route
 * falls back to) plus every file of the build. `root` is the app's base path
 * with a trailing slash; file paths are relative to it.
 */
export function precacheList(root: string, files: BuildFiles): string[] {
  const urls = [...files.immutable, ...files.emitted, ...files.assets].map(
    (file) => root + file.replace(/^\//, '')
  );
  return [...new Set([root, ...urls])];
}

/** Caches of other builds, given all cache names of the origin. */
export function staleCaches(names: string[], current: string): string[] {
  return names.filter((name) => name.startsWith(CACHE_PREFIX) && name !== current);
}
