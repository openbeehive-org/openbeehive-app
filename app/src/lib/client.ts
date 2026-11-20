// Central Connect-Web transport. An interceptor attaches the bearer token (app
// session) to every RPC.
import { createClient, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { browser } from '$app/env';

// Generated via `make proto` into src/lib/proto (protoc-gen-es v2: services
// live in the *_pb modules):
import { SyncService } from './proto/openbeehive/v1/sync_pb';
import { version as appVersion } from '../../package.json';

// Default to the current origin in the browser: the Go binary always serves the
// SPA and API on one origin (cloud behind a reverse proxy, or the single
// self-host binary), so same-origin is correct without a build-time override.
// Falls back to localhost only for non-browser/dev contexts.
const API_URL =
  import.meta.env.BEEHIVE_API_URL ?? (browser ? window.location.origin : 'http://localhost:8080');

const authInterceptor: Interceptor = (next) => async (req) => {
  if (browser) {
    const token = localStorage.getItem('session');
    if (token) req.header.set('Authorization', `Bearer ${token}`);
  }
  return next(req);
};

// Version of the sync protocol this build speaks (see sync.proto). The
// server serves a client that does not say so as an app of release 0.4.x:
// without the columns and tables added since, and without snapshots.
export const SYNC_PROTOCOL = 1;

const versionInterceptor: Interceptor = (next) => async (req) => {
  req.header.set('Openbeehive-Sync-Protocol', String(SYNC_PROTOCOL));
  req.header.set('Openbeehive-App-Version', appVersion);
  return next(req);
};

const transport = createConnectTransport({
  baseUrl: API_URL,
  interceptors: [authInterceptor, versionInterceptor]
});

export const syncClient = createClient(SyncService, transport);
