import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  deletePasskey,
  enrollPasskey,
  isPasskeyCancelled,
  listPasskeys,
  passkeyLogin,
  PasskeyError,
  passkeysSupported
} from './webauthn';

const json = (status: number, body: unknown) => ({
  ok: status < 400,
  status,
  json: async () => body
});

function stubCredentials(get: () => Promise<unknown>, create: () => Promise<unknown> = get) {
  Object.defineProperty(globalThis.navigator, 'credentials', {
    value: { get, create },
    configurable: true
  });
}

const bytes = (...values: number[]) => new Uint8Array(values).buffer;

// Records every request and answers from a queue.
function stubFetch(...answers: ReturnType<typeof json>[]) {
  const calls: { url: string; init: RequestInit }[] = [];
  vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    return answers.shift() ?? json(500, {});
  });
  return calls;
}

const domError = (name: string) => Object.assign(new Error(`${name} from the browser`), { name });

describe('passkeyLogin', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', async () => json(200, { publicKey: { challenge: 'AAAA' } }));
  });

  it.each(['NotAllowedError', 'AbortError'])(
    'reports a dismissed prompt (%s) as cancelled',
    async (name) => {
      stubCredentials(async () => {
        throw domError(name);
      });
      const err = await passkeyLogin('').catch((e: unknown) => e);
      expect(isPasskeyCancelled(err)).toBe(true);
    }
  );

  it('reports a prompt that returned nothing as cancelled', async () => {
    stubCredentials(async () => null);
    expect(isPasskeyCancelled(await passkeyLogin('').catch((e: unknown) => e))).toBe(true);
  });

  it('passes other browser errors through', async () => {
    stubCredentials(async () => {
      throw domError('SecurityError');
    });
    const err = await passkeyLogin('').catch((e: unknown) => e);
    expect(isPasskeyCancelled(err)).toBe(false);
    expect((err as Error).name).toBe('SecurityError');
  });

  it('carries the server error code and status', async () => {
    vi.stubGlobal('fetch', async () =>
      json(429, { error: 'too many attempts', code: 'rate_limited' })
    );
    const err = await passkeyLogin('').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(PasskeyError);
    expect((err as PasskeyError).code).toBe('rate_limited');
    expect((err as PasskeyError).status).toBe(429);
    expect(isPasskeyCancelled(err)).toBe(false);
  });
});

describe('passkey ceremonies', () => {
  it('signs in: decodes the challenge and sends the assertion as base64url', async () => {
    const calls = stubFetch(
      json(200, {
        publicKey: { challenge: '-_8', allowCredentials: [{ type: 'public-key', id: 'AQI' }] }
      }),
      json(200, { token: 't', user_id: 'u1', active_org: 'o1', email: 'a@example.org' })
    );
    let options: PublicKeyCredentialRequestOptions | undefined;
    stubCredentials(async (...args: unknown[]) => {
      options = (args[0] as CredentialRequestOptions).publicKey;
      return {
        id: 'cred',
        rawId: bytes(1, 2),
        type: 'public-key',
        response: {
          authenticatorData: bytes(3),
          clientDataJSON: bytes(4),
          signature: bytes(251, 255),
          userHandle: null
        }
      };
    });

    const session = await passkeyLogin('https://api.example');

    expect(session.token).toBe('t');
    expect([...new Uint8Array(options!.challenge as ArrayBuffer)]).toEqual([251, 255]);
    expect([...new Uint8Array(options!.allowCredentials![0]!.id as ArrayBuffer)]).toEqual([1, 2]);
    expect(calls.map((c) => c.url)).toEqual([
      'https://api.example/auth/webauthn/login/begin',
      'https://api.example/auth/webauthn/login/finish'
    ]);
    expect(JSON.parse(calls[1]!.init.body as string)).toEqual({
      id: 'cred',
      rawId: 'AQI',
      type: 'public-key',
      response: {
        authenticatorData: 'Aw',
        clientDataJSON: 'BA',
        signature: '-_8',
        userHandle: null
      }
    });
  });

  it('enrolls: sends the label, the session header and the attestation', async () => {
    const calls = stubFetch(
      json(200, { publicKey: { challenge: 'AQ', user: { id: 'Ag', name: 'a' } } }),
      json(200, { status: 'ok' })
    );
    stubCredentials(
      async () => null,
      async () => ({
        id: 'new',
        rawId: bytes(9),
        type: 'public-key',
        response: { attestationObject: bytes(5), clientDataJSON: bytes(6) }
      })
    );

    await enrollPasskey('', 'My phone', { Authorization: 'Bearer s' });

    expect(calls[0]!.url).toBe('/auth/webauthn/enroll/begin?name=My%20phone');
    expect(calls[0]!.init.headers).toEqual({ Authorization: 'Bearer s' });
    expect(JSON.parse(calls[1]!.init.body as string).response).toEqual({
      attestationObject: 'BQ',
      clientDataJSON: 'Bg',
      transports: []
    });
  });

  it('reports a rejected verification with the server code', async () => {
    stubFetch(
      json(200, { publicKey: { challenge: 'AQ' } }),
      json(401, { error: 'passkey sign-in failed', code: 'passkey_failed' })
    );
    stubCredentials(async () => ({
      id: 'cred',
      rawId: bytes(1),
      type: 'public-key',
      response: {
        authenticatorData: bytes(1),
        clientDataJSON: bytes(1),
        signature: bytes(1),
        userHandle: bytes(7)
      }
    }));
    const err = (await passkeyLogin('').catch((e: unknown) => e)) as PasskeyError;
    expect(err.code).toBe('passkey_failed');
    expect(err.status).toBe(401);
  });
});

describe('passkey management', () => {
  it('lists passkeys, and nothing when the request fails', async () => {
    stubFetch(json(200, { passkeys: [{ id: '1', name: 'Phone' }] }), json(401, {}));
    expect(await listPasskeys('', {})).toEqual([{ id: '1', name: 'Phone' }]);
    expect(await listPasskeys('', {})).toEqual([]);
  });

  it('deletes a passkey by id', async () => {
    const calls = stubFetch(json(200, {}));
    expect(await deletePasskey('', 'pk1', {})).toBe(true);
    expect(calls[0]!.url).toBe('/auth/webauthn/credentials/delete');
    expect(JSON.parse(calls[0]!.init.body as string)).toEqual({ id: 'pk1' });
  });

  it('knows passkeys are unavailable outside a browser', () => {
    expect(passkeysSupported()).toBe(false);
  });
});
