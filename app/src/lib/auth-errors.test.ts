import { describe, it, expect } from 'vitest';
import { authErrorKey, GENERIC_AUTH_ERROR, NETWORK_AUTH_ERROR } from './auth-errors';
import en from './i18n/locales/en.json';

describe('authErrorKey', () => {
  it('maps a known code to its translation', () => {
    expect(
      authErrorKey({ error: 'wrong email or password', code: 'invalid_credentials' }, 401)
    ).toBe('auth.err_invalid_credentials');
    expect(authErrorKey({ code: 'email_taken' }, 409)).toBe('auth.err_email_taken');
    expect(authErrorKey({ code: 'rate_limited' }, 429)).toBe('auth.err_rate_limited');
  });

  it('never surfaces the raw English text of an unknown error', () => {
    expect(authErrorKey({ error: 'pq: relation "users" does not exist' }, 500)).toBe(
      GENERIC_AUTH_ERROR
    );
    expect(authErrorKey({ code: 'something_new' }, 400)).toBe(GENERIC_AUTH_ERROR);
    expect(authErrorKey(null)).toBe(GENERIC_AUTH_ERROR);
    expect(authErrorKey({ code: 42 }, 400)).toBe(GENERIC_AUTH_ERROR);
  });

  it('gives several codes of one kind the same message', () => {
    expect(authErrorKey({ code: 'invite_expired' }, 403)).toBe(
      authErrorKey({ code: 'invite_invalid' }, 400)
    );
    expect(authErrorKey({ code: 'passkey_no_ceremony' }, 400)).toBe('auth.err_passkey_failed');
  });

  it('understands the older status field', () => {
    expect(authErrorKey({ status: 'unverified' }, 403)).toBe('auth.err_unverified');
    expect(authErrorKey({ status: 'invite_only' }, 403)).toBe('auth.err_invite_only');
  });

  it('falls back on the HTTP status when the code is unknown', () => {
    expect(authErrorKey({}, 429)).toBe('auth.err_rate_limited');
    expect(authErrorKey({}, 401, 'auth.err_invalid_credentials')).toBe(
      'auth.err_invalid_credentials'
    );
    expect(authErrorKey({}, 401)).toBe(GENERIC_AUTH_ERROR);
  });

  it('only returns keys that exist in the locale files', () => {
    const auth = en.auth as Record<string, string>;
    const codes = [
      'invalid_credentials',
      'email_unverified',
      'invite_only',
      'email_taken',
      'invalid_signup',
      'password_too_long',
      'email_reserved',
      'invite_email_mismatch',
      'invite_invalid',
      'rate_limited',
      'demo_unavailable',
      'passkey_failed'
    ];
    const keys = [
      ...codes.map((code) => authErrorKey({ code })),
      GENERIC_AUTH_ERROR,
      NETWORK_AUTH_ERROR
    ];
    for (const key of keys) {
      expect(key.startsWith('auth.')).toBe(true);
      expect(auth[key.slice('auth.'.length)], key).toEqual(expect.any(String));
    }
    expect(new Set(keys).size).toBe(keys.length);
  });

  it('names an invite for someone who is already a member', () => {
    const key = authErrorKey({ code: 'already_member' }, 409);
    expect(key).toBe('members.err_already_member');
    expect((en.members as Record<string, string>).err_already_member).toEqual(expect.any(String));
  });
});
