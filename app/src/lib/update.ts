// Page side of app updates. The service worker installs a new version in the
// background and waits; this module notices, lets the UI offer a reload, and
// hands over to the new worker when the user accepts. See
// src/service-worker/index.ts for the worker side.
import { writable } from 'svelte/store';
import { version } from '$app/env';
import { MSG } from './pwa';

/** True while a newer version is installed and waiting for a reload. */
export const updateReady = writable(false);

const CHECK_INTERVAL_MS = 60 * 60 * 1000;
const REPLY_TIMEOUT_MS = 2000;
const RELOAD_FALLBACK_MS = 3000;

let registration: ServiceWorkerRegistration | undefined;
let reloading = false;

/** Asks a worker which build it belongs to; null when it does not answer. */
function askVersion(worker: ServiceWorker): Promise<string | null> {
  return new Promise((resolve) => {
    const channel = new MessageChannel();
    const timer = setTimeout(() => resolve(null), REPLY_TIMEOUT_MS);
    channel.port1.onmessage = (event) => {
      clearTimeout(timer);
      resolve(typeof event.data === 'string' ? event.data : null);
    };
    worker.postMessage({ type: MSG.version }, [channel.port2]);
  });
}

async function onWaiting(reg: ServiceWorkerRegistration) {
  const waiting = reg.waiting;
  // Without a controller this is the very first install: nothing to update.
  if (!waiting || !navigator.serviceWorker.controller) return;
  // A page loaded right after a deploy already runs the new code while the
  // old worker is still in charge. Then the new worker can simply take over.
  if ((await askVersion(waiting)) === version) waiting.postMessage({ type: MSG.skipWaiting });
  else updateReady.set(true);
}

/**
 * Starts watching for new versions. Call once from the root layout; returns a
 * cleanup function.
 */
export function watchForUpdates(): () => void {
  if (!('serviceWorker' in navigator)) return () => {};
  const sw = navigator.serviceWorker;
  let lastCheck = Date.now();

  const hello = () => sw.controller?.postMessage({ type: MSG.hello });
  // The worker asks every open page for its version before it deletes the
  // cache of an older build.
  const onMessage = (event: MessageEvent) => {
    if ((event.data as { type?: string } | null)?.type === MSG.version) {
      event.ports[0]?.postMessage(version);
    }
  };
  const onControllerChange = () => {
    if (reloading) location.reload();
    else hello();
  };
  // An installed app can stay open for days without a navigation, which is
  // when browsers look for a new worker. Check when it comes back into view.
  const onVisible = () => {
    if (document.visibilityState !== 'visible' || !navigator.onLine) return;
    if (Date.now() - lastCheck < CHECK_INTERVAL_MS) return;
    lastCheck = Date.now();
    // Failing here only means the check is repeated the next time.
    registration?.update().catch(() => undefined);
  };

  sw.addEventListener('message', onMessage);
  sw.addEventListener('controllerchange', onControllerChange);
  document.addEventListener('visibilitychange', onVisible);

  void sw.ready.then((reg) => {
    registration = reg;
    void onWaiting(reg);
    reg.addEventListener('updatefound', () => {
      const worker = reg.installing;
      worker?.addEventListener('statechange', () => {
        if (worker.state === 'installed') void onWaiting(reg);
      });
    });
    hello();
  });

  return () => {
    sw.removeEventListener('message', onMessage);
    sw.removeEventListener('controllerchange', onControllerChange);
    document.removeEventListener('visibilitychange', onVisible);
  };
}

/** Switches to the waiting version and reloads the page. */
export function applyUpdate() {
  const waiting = registration?.waiting;
  if (!waiting) {
    // Another tab already switched; this page only needs the new code.
    location.reload();
    return;
  }
  reloading = true;
  waiting.postMessage({ type: MSG.skipWaiting });
  // controllerchange triggers the reload; do it anyway if that never arrives.
  setTimeout(() => location.reload(), RELOAD_FALLBACK_MS);
}
