import { afterEach, describe, expect, it, vi } from 'vitest';
import { acceptInvite, inviteFailureKey, lookUpInvite } from './invite';
import { authErrorKey } from './auth-errors';

function answer(status: number, body: unknown) {
  return vi.fn(
    async (_url: string, _init?: RequestInit) => new Response(JSON.stringify(body), { status })
  );
}

afterEach(() => vi.unstubAllGlobals());

describe('accepting an invite', () => {
  it('returns the session of the joined workspace', async () => {
    const fetch = answer(200, { token: 'joined', active_org: 'w2' });
    vi.stubGlobal('fetch', fetch);
    const r = await acceptInvite('', 'tok', { token: 'own', user_id: 'u1', active_org: 'w1' });
    expect(r).toEqual({ ok: true, token: 'joined', user_id: 'u1', active_org: 'w2' });
    const init = fetch.mock.calls[0]?.[1];
    expect((init?.headers as Record<string, string>).Authorization).toBe('Bearer own');
  });

  // A refused invite must be reported, not swallowed: otherwise the user
  // lands in their own empty workspace without knowing why.
  it.each([
    ['invite_email_mismatch', 403, 'auth.err_invite_mismatch'],
    ['invite_expired', 400, 'auth.err_invite_expired'],
    ['invite_invalid', 400, 'auth.err_invite_invalid']
  ])('reports %s', async (code, status, key) => {
    vi.stubGlobal('fetch', answer(status, { error: 'x', code }));
    const r = await acceptInvite('', 'tok', { token: 'own' });
    expect(r).toEqual({ ok: false, code, errorKey: key });
  });

  it('reports a network failure', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('offline');
      })
    );
    expect(await acceptInvite('', 'tok', {})).toMatchObject({ ok: false, code: 'network' });
  });

  it('names the invited address on a mismatch when it is known', () => {
    const f = {
      ok: false as const,
      code: 'invite_email_mismatch',
      errorKey: 'auth.err_invite_mismatch'
    };
    expect(inviteFailureKey(f, { workspace: 'Bees', email: 'f…@b….org' })).toBe(
      'auth.err_invite_mismatch_to'
    );
    expect(inviteFailureKey(f, null)).toBe('auth.err_invite_mismatch');
  });
});

describe('looking up an invite', () => {
  it('reads the workspace and masked address', async () => {
    const fetch = answer(200, { workspace: 'Bees', email: 'f…@b….org' });
    vi.stubGlobal('fetch', fetch);
    expect(await lookUpInvite('https://x', 'a b')).toEqual({
      ok: true,
      workspace: 'Bees',
      email: 'f…@b….org'
    });
    expect(fetch.mock.calls[0]?.[0]).toBe('https://x/auth/invite?token=a%20b');
  });

  it('maps an expired invite to its own message', async () => {
    vi.stubGlobal('fetch', answer(400, { error: 'x', code: 'invite_expired' }));
    expect(await lookUpInvite('', 't')).toMatchObject({
      ok: false,
      errorKey: 'auth.err_invite_expired'
    });
  });
});

describe('invite creation errors', () => {
  it('maps inviting an existing member', () => {
    expect(authErrorKey({ code: 'already_member' }, 409)).toBe('members.err_already_member');
  });
});
