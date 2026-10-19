// Central Connect-Web transport. An interceptor attaches the bearer token (app
// session) to every RPC.
import { createClient, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { browser } from '$app/environment';

// Generated via `make proto` into src/lib/proto (protoc-gen-es v2: services
// live in the *_pb modules):
import { SyncService } from './proto/openbeehive/v1/sync_pb';

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

const transport = createConnectTransport({
  baseUrl: API_URL,
  interceptors: [authInterceptor]
});

export const syncClient = createClient(SyncService, transport);
