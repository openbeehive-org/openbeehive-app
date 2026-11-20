// Error responses of /auth/* are JSON `{ "error": "<English>", "code":
// "<snake_case>" }`. The English text is for logs and API users; the app shows
// a translation chosen by `code` and a generic message for anything unknown.

// Server error code -> translation key.
const BY_CODE: Record<string, string> = {
  invalid_credentials: 'auth.err_invalid_credentials',
  email_unverified: 'auth.err_unverified',
  invite_only: 'auth.err_invite_only',
  email_taken: 'auth.err_email_taken',
  invalid_signup: 'auth.err_invalid_input',
  password_too_long: 'auth.err_password_too_long',
  email_reserved: 'auth.err_email_reserved',
  invite_email_mismatch: 'auth.err_invite_mismatch',
  invite_invalid: 'auth.err_invite_invalid',
  invite_expired: 'auth.err_invite_invalid',
  rate_limited: 'auth.err_rate_limited',
  demo_unavailable: 'auth.err_demo_unavailable',
  passkey_failed: 'auth.err_passkey_failed',
  passkey_no_ceremony: 'auth.err_passkey_failed',
  // Links the browser opened; the server redirects to /login?error=<code>.
  verification_invalid: 'auth.err_verification_invalid',
  oidc_state_missing: 'auth.err_provider_expired',
  oidc_state_invalid: 'auth.err_provider_expired',
  oidc_failed: 'auth.err_provider_failed',
  oidc_no_email: 'auth.err_provider_no_email',
  unknown_provider: 'auth.err_provider_failed',
  account_exists: 'auth.err_account_exists',
  // Account, password and member management.
  wrong_password: 'account.err_wrong_password',
  invalid_password: 'account.err_password_short',
  no_password: 'account.err_no_password',
  password_managed: 'account.err_password_managed',
  reset_invalid: 'auth.reset_invalid',
  reset_needs_admin: 'auth.forgot_needs_admin',
  admin_only: 'account.err_admin_only',
  account_not_found: 'account.err_account_not_found',
  account_protected: 'account.delete_protected_admin',
  sole_owner: 'account.err_sole_owner',
  reauth_required: 'account.err_reauth_required',
  reauth_unavailable: 'account.delete_unconfirmable',
  passkey_none: 'account.delete_unconfirmable',
  demo_read_only: 'account.err_demo',
  last_owner: 'members.err_last_owner',
  owner_only: 'members.err_owner_only',
  member_not_found: 'members.err_not_found',
  // Value of the `status` field that older responses carried instead of a code.
  unverified: 'auth.err_unverified'
};

export const GENERIC_AUTH_ERROR = 'auth.err_generic';
/** For a request that never got an answer (offline, server down). */
export const NETWORK_AUTH_ERROR = 'auth.err_network';

export type AuthErrorBody =
  { error?: unknown; code?: unknown; status?: unknown } | null | undefined;

/**
 * Translation key for a failed auth request.
 *
 * `body` is the parsed JSON response (`status` is read as well: responses
 * used it for "unverified" and "invite_only" before `code` existed).
 * `on401` names what a 401 without a known code means for this request.
 */
export function authErrorKey(
  body: AuthErrorBody,
  httpStatus = 0,
  on401: string = GENERIC_AUTH_ERROR
): string {
  for (const value of [body?.code, body?.status]) {
    const key = typeof value === 'string' ? BY_CODE[value] : undefined;
    if (key) return key;
  }
  if (httpStatus === 429) return 'auth.err_rate_limited';
  if (httpStatus === 401) return on401;
  return GENERIC_AUTH_ERROR;
}
