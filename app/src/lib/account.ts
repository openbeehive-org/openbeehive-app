// The signed-in account and its workspaces: calls to the /auth and /tenants
// endpoints, the session kept on this device, and the decisions the Settings
// and sign-in pages derive from the server's answers. The decisions are plain
// functions so they can be tested without a browser.
import { authErrorKey, NETWORK_AUTH_ERROR } from './auth-errors.js';
import type { InstanceInfo } from './instance.js';

const API = (import.meta.env?.BEEHIVE_API_URL ?? '').replace(/\/$/, '');

/** Origin of the API; empty when the app is served by the server itself. */
export const apiOrigin = (): string => API;

// --- session on this device ---

const SESSION_KEYS = ['session', 'obh.userId', 'obh.orgId', 'obh.email'];
const NOTICE_KEY = 'obh.notice';

export type SessionPayload = { token?: string; user_id?: string; active_org?: string };

/** Headers for a request made as the signed-in account. */
export function authHeaders(): Record<string, string> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const token = localStorage.getItem('session');
  if (token) headers.Authorization = `Bearer ${token}`;
  return headers;
}

/** Stores the session and active workspace an auth endpoint handed out. */
export function saveSession(j: SessionPayload | null | undefined): void {
  if (j?.token) localStorage.setItem('session', j.token);
  if (j?.user_id) localStorage.setItem('obh.userId', j.user_id);
  if (j?.active_org) localStorage.setItem('obh.orgId', j.active_org);
}

/** Forgets the session. Records stored on the device are not touched. */
export function clearSession(): void {
  for (const key of SESSION_KEYS) localStorage.removeItem(key);
}

// --- requests ---

export type Failure = {
  ok: false;
  status: number;
  /** The server's error code; 'network' when no answer came. */
  code: string;
  /** Translation key of the message to show. */
  errorKey: string;
  body: Record<string, unknown>;
};
export type Result<T> = { ok: true; body: T } | Failure;

/**
 * Calls an auth or workspace endpoint: GET without a body, POST with one.
 * Never throws; a failure carries the translation key of its message.
 */
export async function call<T = Record<string, unknown>>(
  path: string,
  body?: unknown
): Promise<Result<T>> {
  let r: Response;
  try {
    const init: RequestInit = { credentials: 'include', headers: authHeaders() };
    if (body !== undefined) {
      init.method = 'POST';
      init.body = JSON.stringify(body);
    }
    r = await fetch(`${API}${path}`, init);
  } catch {
    return { ok: false, status: 0, code: 'network', errorKey: NETWORK_AUTH_ERROR, body: {} };
  }
  const j = (await r.json().catch(() => ({}))) as Record<string, unknown>;
  if (r.ok) return { ok: true, body: j as T };
  return {
    ok: false,
    status: r.status,
    code: typeof j.code === 'string' ? j.code : '',
    // A 401 here means the session ended, whatever the endpoint.
    errorKey: authErrorKey(j, r.status, 'account.err_signed_out'),
    body: j
  };
}

/**
 * Continues in the workspace an endpoint switched the session to: the page
 * reloads so that workspace's local database opens and sync starts for it.
 */
export function openWorkspace(j: SessionPayload): void {
  saveSession(j);
  location.reload();
}

// --- losing the active workspace ---

export type Recovery = 'recovered' | 'ok' | 'signed_out' | 'unreachable';

/**
 * Asks the server for a session in another workspace after the active one
 * was taken away (removed from it, or it was deleted). 'recovered' means a
 * new session is stored and the page must reload to open that workspace.
 */
export async function recoverSession(): Promise<Recovery> {
  if (!localStorage.getItem('session')) return 'signed_out';
  const r = await call<SessionPayload & { switched?: boolean }>('/auth/session/recover', {});
  if (!r.ok) return r.code === 'network' || r.status >= 500 ? 'unreachable' : 'signed_out';
  if (!r.body.switched || !r.body.token) return 'ok';
  saveSession(r.body);
  try {
    sessionStorage.setItem(NOTICE_KEY, 'account.workspace_lost');
  } catch {
    // no session storage: the switch still happens, only unannounced
  }
  return 'recovered';
}

/** The message left for the next page load, if any; reading removes it. */
export function takeNotice(): string {
  try {
    const key = sessionStorage.getItem(NOTICE_KEY) ?? '';
    sessionStorage.removeItem(NOTICE_KEY);
    return key;
  } catch {
    return '';
  }
}

// --- account ---

export type WorkspaceRef = { id: string; name: string; members: number };

export type DeletionPlan = {
  /** Why the account cannot be deleted in the app at all, or ''. */
  protected: '' | 'instance_admin' | 'demo';
  /** Workspaces deleted with the account: it is their only member. */
  deletes: WorkspaceRef[];
  /** Workspaces that block the deletion: other members, no other admin. */
  blocked: WorkspaceRef[];
  /** Workspaces the account leaves; its records there stay. */
  leaves: WorkspaceRef[];
  confirm: 'password' | 'passkey' | 'unavailable';
};

export type AccountInfo = {
  user_id: string;
  email: string;
  name: string;
  /** set: changeable. none: not set yet. managed: server configuration. off: no password sign-in. */
  password: 'set' | 'none' | 'managed' | 'off';
  deletion: DeletionPlan;
};

export type DeletionState = 'instance_admin' | 'demo' | 'blocked' | 'unconfirmable' | 'ready';

/**
 * What the "Delete account" section offers. A protected account comes
 * first (nothing else matters for it), then the workspaces that have to be
 * handed over or deleted, then a missing way to confirm.
 */
export function deletionState(plan: DeletionPlan): DeletionState {
  if (plan.protected === 'instance_admin' || plan.protected === 'demo') return plan.protected;
  if (plan.blocked.length > 0) return 'blocked';
  if (plan.confirm === 'unavailable') return 'unconfirmable';
  return 'ready';
}

// --- passwords ---

const MIN_PASSWORD = 8;
const MAX_PASSWORD_BYTES = 72;

/**
 * Translation key of what is wrong with a new password, or '' when it can be
 * sent. Mirrors the server's limits; `repeat` is the second entry.
 */
export function newPasswordProblem(password: string, repeat: string): string {
  if (password.length < MIN_PASSWORD) return 'account.err_password_short';
  if (new TextEncoder().encode(password).length > MAX_PASSWORD_BYTES) {
    return 'auth.err_password_too_long';
  }
  if (password !== repeat) return 'account.err_password_mismatch';
  return '';
}

/** How this server recovers a forgotten password. */
export type ResetMode = 'email' | 'admin' | 'off';

/** Servers before 0.5.0 have no reset; they do not send the field. */
export function resetMode(instance: InstanceInfo | null | undefined): ResetMode {
  const mode = instance?.password_reset;
  return mode === 'email' || mode === 'admin' ? mode : 'off';
}

export type ResetRequestState = 'form' | 'sent' | 'needs_admin';

/**
 * Where the "Forgot password?" page goes after asking for a link. A sent
 * request always reads "check your mail": the server does not say whether
 * the address has an account.
 */
export function afterResetRequest(r: Result<unknown>): {
  state: ResetRequestState;
  errorKey: string;
} {
  if (r.ok) return { state: 'sent', errorKey: '' };
  if (r.code === 'reset_needs_admin') return { state: 'needs_admin', errorKey: '' };
  return { state: 'form', errorKey: r.errorKey };
}

export type ResetCompleteState = 'form' | 'done' | 'invalid';

/** Where the "Choose a new password" page goes after submitting. */
export function afterResetComplete(r: Result<unknown>): {
  state: ResetCompleteState;
  errorKey: string;
} {
  if (r.ok) return { state: 'done', errorKey: '' };
  // A used or expired link cannot be retried: offer a new one instead.
  if (r.code === 'reset_invalid') return { state: 'invalid', errorKey: '' };
  return { state: 'form', errorKey: r.errorKey };
}

// --- signing out ---

/**
 * Translation key of the warning to show before signing out, or '' when all
 * changes are on the server. Removing the data from the device turns "they
 * wait here" into "they are lost".
 */
export function logoutWarning(unsent: number, removeData: boolean): string {
  if (unsent <= 0) return '';
  return removeData ? 'account.logout_unsent_lost' : 'account.logout_unsent_kept';
}
