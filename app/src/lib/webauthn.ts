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
  if (!b.ok) throw new Error(bj.error || 'passkey: begin failed');
  const cred = await run(bj);
  if (!cred) throw new Error('passkey: cancelled');
  const f = await fetch(`${api}${finishPath}`, { ...common, body: JSON.stringify(cred) });
  const fj = await f.json().catch(() => ({}));
  if (!f.ok) throw new Error(fj.error || 'passkey: verification failed');
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

/** Usernameless sign-in with a discoverable passkey; returns the auth payload. */
export async function passkeyLogin(api: string) {
  return beginFinish(
    api,
    '/auth/webauthn/login/begin',
    '/auth/webauthn/login/finish',
    async (options: { publicKey: RequestJSON }) => {
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
    },
    {}
  );
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
