// The invite link (/login?invite=<token>): what it is for, and joining the
// workspace right after signing in or up. Both report failures as a
// translation key, so the sign-in page can say why joining did not work
// instead of continuing silently into the account's own workspace.
import { authErrorKey, GENERIC_AUTH_ERROR, NETWORK_AUTH_ERROR } from './auth-errors';

/** What the sign-in page shows about an invite before anyone signs in. */
export type InviteInfo = { workspace: string; email: string };

export type InviteFailure = { ok: false; code: string; errorKey: string };

/** Session fields of the auth endpoints that the app keeps. */
export type JoinedSession = { token?: string; user_id?: string; active_org?: string };

async function send(
  url: string,
  init: RequestInit
): Promise<{ ok: true; body: Record<string, unknown> } | InviteFailure> {
  let r: Response;
  try {
    r = await fetch(url, { credentials: 'include', ...init });
  } catch {
    return { ok: false, code: 'network', errorKey: NETWORK_AUTH_ERROR };
  }
  const body = (await r.json().catch(() => ({}))) as Record<string, unknown>;
  if (r.ok) return { ok: true, body };
  const code = typeof body.code === 'string' ? body.code : '';
  return { ok: false, code, errorKey: authErrorKey(body, r.status) };
}

/** The workspace and masked address an invite is for (GET /auth/invite). */
export async function lookUpInvite(
  api: string,
  token: string
): Promise<({ ok: true } & InviteInfo) | InviteFailure> {
  const r = await send(`${api}/auth/invite?token=${encodeURIComponent(token)}`, {});
  if (!r.ok) return r;
  return { ok: true, workspace: String(r.body.workspace ?? ''), email: String(r.body.email ?? '') };
}

/**
 * Joins the invited workspace with the session just obtained. On success the
 * returned session points at the joined workspace.
 */
export async function acceptInvite(
  api: string,
  token: string,
  session: JoinedSession
): Promise<({ ok: true } & JoinedSession) | InviteFailure> {
  const r = await send(`${api}/auth/accept-invite`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(session.token ? { Authorization: `Bearer ${session.token}` } : {})
    },
    body: JSON.stringify({ token })
  });
  if (!r.ok) return r;
  return { ok: true, ...session, ...(r.body as JoinedSession) };
}

/**
 * The message for a failed invite. A mismatch names the address the invite
 * is for when the page knows it (masked by the server).
 */
export function inviteFailureKey(f: InviteFailure, info: InviteInfo | null): string {
  if (f.code === 'invite_email_mismatch' && info?.email) return 'auth.err_invite_mismatch_to';
  return f.errorKey || GENERIC_AUTH_ERROR;
}
