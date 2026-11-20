// Passkey ceremonies against the server's /auth/webauthn endpoints.
//
// The server (go-webauthn) speaks the standard WebAuthn JSON wire format:
// binary fields travel as base64url strings. The conversions are done by hand
// instead of PublicKeyCredential.parseCreationOptionsFromJSON() because the
// native parsers are still missing on a chunk of the PWA's install base.

const b64uToBuf = (s: string): ArrayBuffer =>
  Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0)).buffer;

const bufToB64u = (b: ArrayBuffer): string =>
  btoa(String.fromCharCode(...new Uint8Array(b)))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '');

export const passkeysSupported = (): boolean =>
  typeof window !== 'undefined' && !!window.PublicKeyCredential;

type Fetchish = { headers?: Record<string, string> };

/**
 * A failed passkey ceremony. `code` is 'cancelled' when the user dismissed the
 * browser's prompt (or let it time out), otherwise the `code` of the server's
 * error response, or '' when there was none. `status` is the HTTP status.
 */
export class PasskeyError extends Error {
  readonly code: string;
  readonly status: number;
  constructor(message: string, code: string, status = 0) {
    super(message);
    this.name = 'PasskeyError';
    this.code = code;
    this.status = status;
  }
}

/** True when the user backed out of the passkey prompt: nothing to report. */
export const isPasskeyCancelled = (e: unknown): boolean =>
  e instanceof PasskeyError && e.code === 'cancelled';

const cancelled = () => new PasskeyError('passkey: cancelled', 'cancelled');
const serverCode = (body: { code?: unknown }): string =>
  typeof body.code === 'string' ? body.code : '';

// Wire shapes from go-webauthn: the DOM option types with binary fields as base64url.
type DescriptorJSON = Omit<PublicKeyCredentialDescriptor, 'id'> & { id: string };
type CreationJSON = Omit<
  PublicKeyCredentialCreationOptions,
  'challenge' | 'user' | 'excludeCredentials'
> & {
  challenge: string;
  user: Omit<PublicKeyCredentialUserEntity, 'id'> & { id: string };
  excludeCredentials?: DescriptorJSON[];
};
type RequestJSON = Omit<PublicKeyCredentialRequestOptions, 'challenge' | 'allowCredentials'> & {
  challenge: string;
  allowCredentials?: DescriptorJSON[];
};

// Finish response: the session payload on login, a status object on enroll.
export type PasskeyFinish = {
  token?: string;
  user_id?: string;
  active_org?: string;
  email?: string;
  [k: string]: unknown;
};

async function beginFinish<O>(
  api: string,
  beginPath: string,
  finishPath: string,
  run: (options: O) => Promise<Credential | null>,
  opts: Fetchish
): Promise<PasskeyFinish> {
  const common: RequestInit = { method: 'POST', credentials: 'include' };
  if (opts.headers) common.headers = opts.headers;
  const b = await fetch(`${api}${beginPath}`, common);
  const bj = await b.json().catch(() => ({}));
  if (!b.ok) throw new PasskeyError(bj.error || 'passkey: begin failed', serverCode(bj), b.status);
  let cred: Credential | null;
  try {
    cred = await run(bj);
  } catch (e) {
    // Browsers reject with NotAllowedError when the prompt is closed or times
    // out, and with AbortError when it is aborted.
    const name = (e as { name?: string } | null)?.name;
    if (name === 'NotAllowedError' || name === 'AbortError') throw cancelled();
    throw e;
  }
  if (!cred) throw cancelled();
  const f = await fetch(`${api}${finishPath}`, { ...common, body: JSON.stringify(cred) });
  const fj = await f.json().catch(() => ({}));
  if (!f.ok) {
    throw new PasskeyError(fj.error || 'passkey: verification failed', serverCode(fj), f.status);
  }
  return fj;
}

/** Enroll a new passkey for the signed-in account (label is user-facing). */
export async function enrollPasskey(api: string, label: string, headers: Record<string, string>) {
  return beginFinish(
    api,
    `/auth/webauthn/enroll/begin?name=${encodeURIComponent(label)}`,
    '/auth/webauthn/enroll/finish',
    async (options: { publicKey: CreationJSON }) => {
      const j = options.publicKey;
      const pk: PublicKeyCredentialCreationOptions = {
        ...j,
        challenge: b64uToBuf(j.challenge),
        user: { ...j.user, id: b64uToBuf(j.user.id) },
        excludeCredentials: (j.excludeCredentials ?? []).map((c) => ({
          ...c,
          id: b64uToBuf(c.id)
        }))
      };
      const cred = (await navigator.credentials.create({
        publicKey: pk
      })) as PublicKeyCredential | null;
      if (!cred) return null;
      const r = cred.response as AuthenticatorAttestationResponse;
      return {
        id: cred.id,
        rawId: bufToB64u(cred.rawId),
        type: cred.type,
        response: {
          attestationObject: bufToB64u(r.attestationObject),
          clientDataJSON: bufToB64u(r.clientDataJSON),
          transports: r.getTransports?.() ?? []
        }
      } as unknown as Credential;
    },
    { headers }
  );
}

// Asks the browser for an assertion with one of the user's passkeys and
// returns it in the wire format the server reads.
async function assertion(options: { publicKey: RequestJSON }): Promise<Credential | null> {
  const j = options.publicKey;
  const pk: PublicKeyCredentialRequestOptions = {
    ...j,
    challenge: b64uToBuf(j.challenge),
    allowCredentials: (j.allowCredentials ?? []).map((c) => ({
      ...c,
      id: b64uToBuf(c.id)
    }))
  };
  const cred = (await navigator.credentials.get({
    publicKey: pk
  })) as PublicKeyCredential | null;
  if (!cred) return null;
  const r = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id,
    rawId: bufToB64u(cred.rawId),
    type: cred.type,
    response: {
      authenticatorData: bufToB64u(r.authenticatorData),
      clientDataJSON: bufToB64u(r.clientDataJSON),
      signature: bufToB64u(r.signature),
      userHandle: r.userHandle ? bufToB64u(r.userHandle) : null
    }
  } as unknown as Credential;
}

/** Usernameless sign-in with a discoverable passkey; returns the auth payload. */
export async function passkeyLogin(api: string) {
  return beginFinish(
    api,
    '/auth/webauthn/login/begin',
    '/auth/webauthn/login/finish',
    assertion,
    {}
  );
}

/**
 * Confirms that the signed-in account's owner is present, with one of the
 * account's passkeys. Returns the single-use proof that accounts without a
 * password send where a password would be asked for.
 */
export async function passkeyReauth(api: string, headers: Record<string, string>): Promise<string> {
  const done = await beginFinish(
    api,
    '/auth/webauthn/reauth/begin',
    '/auth/webauthn/reauth/finish',
    assertion,
    { headers }
  );
  return typeof done.reauth === 'string' ? done.reauth : '';
}

export type PasskeyInfo = {
  id: string;
  name: string;
  created_at: string;
  last_used_at: string | null;
};

export async function listPasskeys(
  api: string,
  headers: Record<string, string>
): Promise<PasskeyInfo[]> {
  const r = await fetch(`${api}/auth/webauthn/credentials`, { credentials: 'include', headers });
  if (!r.ok) return [];
  const j = await r.json().catch(() => ({}));
  return j.passkeys ?? [];
}

export async function deletePasskey(
  api: string,
  id: string,
  headers: Record<string, string>
): Promise<boolean> {
  const r = await fetch(`${api}/auth/webauthn/credentials/delete`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: JSON.stringify({ id })
  });
  return r.ok;
}
