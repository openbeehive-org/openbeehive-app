import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { addMessages, init, locale, _ } from 'svelte-i18n';
import { get } from 'svelte/store';
import {
  afterDeleteRequest,
  afterResetComplete,
  afterResetRequest,
  authHeaders,
  call,
  clearSession,
  DELETE_ACCOUNT_HASH,
  deletionState,
  logoutWarning,
  newPasswordProblem,
  recoverSession,
  resetMode,
  saveSession,
  signInAgain,
  takeNotice,
  takeResume,
  type DeletionPlan,
  type Failure
} from './account';
import en from './i18n/locales/en.json';
import de from './i18n/locales/de.json';
import fr from './i18n/locales/fr.json';
import es from './i18n/locales/es.json';
import it_ from './i18n/locales/it.json';

const LOCALES = { en, de, fr, es, it: it_ } as const;
type Lang = keyof typeof LOCALES;

const json = (status: number, body: unknown) => ({
  ok: status < 400,
  status,
  json: async () => body
});

// Records every request and answers from a queue.
function stubFetch(...answers: ReturnType<typeof json>[]) {
  const calls: { url: string; init: RequestInit }[] = [];
  vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    return answers.shift() ?? json(500, {});
  });
  return calls;
}

function stubSessionStorage() {
  const items = new Map<string, string>();
  vi.stubGlobal('sessionStorage', {
    getItem: (k: string) => items.get(k) ?? null,
    setItem: (k: string, v: string) => void items.set(k, v),
    removeItem: (k: string) => void items.delete(k)
  });
}

const failure = (code: string, status = 400): Failure => ({
  ok: false,
  status,
  code,
  errorKey: `test.${code}`,
  body: {}
});

beforeEach(() => {
  localStorage.clear();
  stubSessionStorage();
});

describe('session on the device', () => {
  it('stores what an endpoint hands out and sends it as a bearer token', () => {
    expect(authHeaders().Authorization).toBeUndefined();
    saveSession({ token: 'tok', user_id: 'u1', active_org: 'o1' });
    expect(authHeaders().Authorization).toBe('Bearer tok');
    expect(localStorage.getItem('obh.userId')).toBe('u1');
    expect(localStorage.getItem('obh.orgId')).toBe('o1');
  });

  it('keeps what a partial answer does not mention', () => {
    saveSession({ token: 'tok', user_id: 'u1', active_org: 'o1' });
    saveSession({ token: 'next', active_org: 'o2' });
    saveSession(null);
    expect(localStorage.getItem('session')).toBe('next');
    expect(localStorage.getItem('obh.userId')).toBe('u1');
    expect(localStorage.getItem('obh.orgId')).toBe('o2');
  });

  it('forgets the session but nothing else on sign-out', () => {
    saveSession({ token: 'tok', user_id: 'u1', active_org: 'o1' });
    localStorage.setItem('obh.email', 'a@example.org');
    localStorage.setItem('obh.theme', 'dark');
    clearSession();
    for (const key of ['session', 'obh.userId', 'obh.orgId', 'obh.email']) {
      expect(localStorage.getItem(key), key).toBeNull();
    }
    expect(localStorage.getItem('obh.theme')).toBe('dark');
  });
});

describe('call', () => {
  it('sends a GET without a body and a JSON POST with one', async () => {
    saveSession({ token: 'tok' });
    const calls = stubFetch(json(200, { a: 1 }), json(200, { b: 2 }));
    expect(await call('/auth/me')).toEqual({ ok: true, body: { a: 1 } });
    expect(await call('/tenants/leave', { org_id: 'o1' })).toEqual({ ok: true, body: { b: 2 } });
    expect(calls[0]!.init.method).toBeUndefined();
    expect(calls[1]!.init.method).toBe('POST');
    expect(calls[1]!.init.body).toBe('{"org_id":"o1"}');
    expect((calls[1]!.init.headers as Record<string, string>).Authorization).toBe('Bearer tok');
  });

  it('turns an error code into the key of its translation', async () => {
    stubFetch(json(409, { error: 'a workspace needs an admin', code: 'last_owner' }));
    const r = await call('/tenants/leave', {});
    expect(r).toMatchObject({
      ok: false,
      status: 409,
      code: 'last_owner',
      errorKey: 'members.err_last_owner'
    });
  });

  it('never shows the server text of an unknown error', async () => {
    stubFetch(json(500, { error: 'pq: relation "member" does not exist' }));
    const r = (await call('/tenants/members')) as Failure;
    expect(r.errorKey).toBe('auth.err_generic');
    expect(r.code).toBe('');
  });

  it('reads a 401 as "signed out" and a failed request as "no connection"', async () => {
    stubFetch(json(401, { error: 'not signed in', code: 'not_signed_in' }));
    expect(((await call('/auth/account')) as Failure).errorKey).toBe('account.err_signed_out');
    vi.stubGlobal('fetch', async () => {
      throw new TypeError('offline');
    });
    expect(await call('/auth/account')).toMatchObject({
      ok: false,
      code: 'network',
      errorKey: 'auth.err_network'
    });
  });
});

describe('recoverSession', () => {
  it('does not ask without a session', async () => {
    const calls = stubFetch();
    expect(await recoverSession()).toBe('signed_out');
    expect(calls).toHaveLength(0);
  });

  it('stores the session for the fallback workspace and leaves a notice', async () => {
    saveSession({ token: 'old', user_id: 'u1', active_org: 'gone' });
    const calls = stubFetch(json(200, { switched: true, token: 'new', active_org: 'mine' }));
    expect(await recoverSession()).toBe('recovered');
    expect(calls[0]!.url).toBe('/auth/session/recover');
    expect(calls[0]!.init.method).toBe('POST');
    expect(localStorage.getItem('session')).toBe('new');
    expect(localStorage.getItem('obh.orgId')).toBe('mine');
    // The notice is shown once, on the next page load.
    expect(takeNotice()).toBe('account.workspace_lost');
    expect(takeNotice()).toBe('');
  });

  it('changes nothing while the session is still good', async () => {
    saveSession({ token: 'old', active_org: 'o1' });
    stubFetch(json(200, { switched: false, active_org: 'o1' }));
    expect(await recoverSession()).toBe('ok');
    expect(localStorage.getItem('session')).toBe('old');
    expect(takeNotice()).toBe('');
  });

  it('tells a rejected session from an unreachable server', async () => {
    saveSession({ token: 'old' });
    stubFetch(json(401, { code: 'not_signed_in' }), json(503, { code: 'unavailable' }));
    expect(await recoverSession()).toBe('signed_out');
    expect(await recoverSession()).toBe('unreachable');
    vi.stubGlobal('fetch', async () => {
      throw new TypeError('offline');
    });
    expect(await recoverSession()).toBe('unreachable');
    expect(localStorage.getItem('session')).toBe('old');
  });
});

describe('signInAgain', () => {
  function stubLocation() {
    const location = { href: '/settings' };
    vi.stubGlobal('location', location);
    return location;
  }

  it('gives up the session, remembers the way back and opens the sign-in page', async () => {
    saveSession({ token: 'old', user_id: 'u1', active_org: 'o1' });
    const calls = stubFetch(json(204, {}));
    const location = stubLocation();

    await signInAgain(`/settings${DELETE_ACCOUNT_HASH}`);

    expect(calls[0]!.url).toBe('/auth/logout');
    // The old token must not outrank the session of the new sign-in.
    expect(localStorage.getItem('session')).toBeNull();
    expect(location.href).toBe('/login?reauth=1');
    expect(takeResume()).toBe('/settings#delete-account');
    expect(takeResume()).toBe('');
  });

  it('still leaves when the server cannot be reached', async () => {
    saveSession({ token: 'old' });
    vi.stubGlobal('fetch', async () => {
      throw new TypeError('offline');
    });
    const location = stubLocation();
    await signInAgain('/settings');
    expect(localStorage.getItem('session')).toBeNull();
    expect(location.href).toBe('/login?reauth=1');
  });

  it('only resumes inside the app', () => {
    for (const target of ['https://evil.example/', '//evil.example/', 'settings']) {
      sessionStorage.setItem('obh.resume', target);
      expect(takeResume(), target).toBe('');
    }
    sessionStorage.setItem('obh.resume', '/settings#delete-account');
    expect(takeResume()).toBe('/settings#delete-account');
  });
});

describe('deleting the account', () => {
  const plan = (over: Partial<DeletionPlan> = {}): DeletionPlan => ({
    protected: '',
    deletes: [],
    blocked: [],
    leaves: [],
    confirm: 'password',
    ...over
  });
  const club = { id: 'o1', name: 'Club', members: 3 };

  it('is offered when nothing stands in the way', () => {
    expect(deletionState(plan())).toBe('ready');
    expect(deletionState(plan({ deletes: [club], leaves: [club] }))).toBe('ready');
    expect(deletionState(plan({ confirm: 'passkey' }))).toBe('ready');
    // No password and no passkey: a recent sign-in confirms.
    expect(deletionState(plan({ confirm: 'login' }))).toBe('ready');
  });

  it('is blocked while the account is the only admin of a shared workspace', () => {
    expect(deletionState(plan({ blocked: [club] }))).toBe('blocked');
    expect(deletionState(plan({ blocked: [club], confirm: 'login' }))).toBe('blocked');
  });

  it('sends an account that confirms by signing in through sign-in when the session is old', () => {
    expect(afterDeleteRequest(failure('login_required', 403))).toBe('sign_in_again');
  });

  it('shows the workspace someone joined meanwhile as a blocker', () => {
    expect(afterDeleteRequest(failure('sole_owner', 409))).toBe('blocked');
  });

  it('finishes on success and reports everything else', () => {
    expect(afterDeleteRequest({ ok: true, body: {} })).toBe('deleted');
    expect(afterDeleteRequest(failure('wrong_password', 403))).toBe('error');
    expect(afterDeleteRequest(failure('network', 0))).toBe('error');
  });

  it('is never offered for the instance admin and the demo account', () => {
    expect(deletionState(plan({ protected: 'instance_admin' }))).toBe('instance_admin');
    expect(deletionState(plan({ protected: 'demo', blocked: [club] }))).toBe('demo');
  });
});

describe('new passwords', () => {
  it('must be at least 8 characters', () => {
    expect(newPasswordProblem('1234567', '1234567')).toBe('account.err_password_short');
    expect(newPasswordProblem('12345678', '12345678')).toBe('');
  });

  it('must fit the 72 bytes the server hashes', () => {
    expect(newPasswordProblem('a'.repeat(72), 'a'.repeat(72))).toBe('');
    expect(newPasswordProblem('a'.repeat(73), 'a'.repeat(73))).toBe('auth.err_password_too_long');
    // 25 characters, 75 bytes.
    expect(newPasswordProblem('€'.repeat(25), '€'.repeat(25))).toBe('auth.err_password_too_long');
  });

  it('must be typed the same twice', () => {
    expect(newPasswordProblem('correct horse', 'correct house')).toBe(
      'account.err_password_mismatch'
    );
  });
});

describe('password reset flow', () => {
  it('reads how the server resets passwords', () => {
    expect(resetMode({ password_reset: 'email' })).toBe('email');
    expect(resetMode({ password_reset: 'admin' })).toBe('admin');
    expect(resetMode({ password_reset: 'off' })).toBe('off');
    // Servers before 0.5.0 do not send the field, and have no reset.
    expect(resetMode({ password_auth: true })).toBe('off');
    expect(resetMode(null)).toBe('off');
  });

  it('says "check your mail" after every accepted request', () => {
    expect(afterResetRequest({ ok: true, body: {} })).toEqual({ state: 'sent', errorKey: '' });
  });

  it('points to the administrator when the server sends no mail', () => {
    expect(afterResetRequest(failure('reset_needs_admin', 409))).toEqual({
      state: 'needs_admin',
      errorKey: ''
    });
  });

  it('keeps the form and shows the reason for other failures', () => {
    expect(afterResetRequest(failure('rate_limited', 429))).toEqual({
      state: 'form',
      errorKey: 'test.rate_limited'
    });
  });

  it('finishes when the new password is saved', () => {
    expect(afterResetComplete({ ok: true, body: {} })).toEqual({ state: 'done', errorKey: '' });
  });

  it('offers a new link when this one is used up or expired', () => {
    expect(afterResetComplete(failure('reset_invalid'))).toEqual({
      state: 'invalid',
      errorKey: ''
    });
  });

  it('lets the user correct a refused password with the same link', () => {
    expect(afterResetComplete(failure('invalid_password'))).toEqual({
      state: 'form',
      errorKey: 'test.invalid_password'
    });
    expect(afterResetComplete(failure('network', 0)).state).toBe('form');
  });
});

describe('signing out', () => {
  it('warns only when changes have not reached the server', () => {
    expect(logoutWarning(0, false)).toBe('');
    expect(logoutWarning(0, true)).toBe('');
    expect(logoutWarning(3, false)).toBe('account.logout_unsent_kept');
  });

  it('says the changes are lost when the data is removed too', () => {
    expect(logoutWarning(1, true)).toBe('account.logout_unsent_lost');
  });
});

describe('messages of the account and workspace pages', () => {
  beforeAll(() => {
    for (const [lang, messages] of Object.entries(LOCALES)) addMessages(lang, messages);
    init({ fallbackLocale: 'en', initialLocale: 'en' });
  });

  function t(lang: Lang, key: string, values?: Record<string, string | number>) {
    locale.set(lang);
    return get(_)(key, values ? { values } : undefined);
  }

  const values = {
    n: 2,
    name: 'Club',
    names: '“Club”',
    email: 'a@example.org',
    date: '1 Dec 2026',
    workspace: 'Club',
    time: '1 Dec 2026, 10:00'
  };
  const namespaces = ['settings', 'auth', 'tenant', 'members', 'account', 'pk', 'apikeys'] as const;

  it.each(Object.keys(LOCALES) as Lang[])('format cleanly in %s', (lang) => {
    for (const ns of namespaces) {
      for (const key of Object.keys(LOCALES[lang][ns])) {
        const text = t(lang, `${ns}.${key}`, { ...values, provider: 'Google' });
        expect(text, `${lang} ${ns}.${key}`).not.toMatch(/[{}]|plural|undefined/);
        expect(text.trim(), `${lang} ${ns}.${key}`).not.toBe('');
      }
    }
  });

  it('count unsent changes in the singular and the plural', () => {
    expect(t('en', 'account.logout_unsent_kept', { n: 1 })).toMatch(/^1 change has not/);
    expect(t('en', 'account.logout_unsent_lost', { n: 3 })).toMatch(
      /^3 changes have not been uploaded yet and will be lost\. Go online/
    );
    expect(t('de', 'account.logout_unsent_kept', { n: 1 })).toMatch(/^1 Änderung ist/);
    expect(t('fr', 'account.logout_unsent_lost', { n: 2 })).toMatch(/^2 modifications n’ont/);
    expect(t('es', 'account.logout_unsent_kept', { n: 2 })).toMatch(/^2 cambios aún/);
    expect(t('it', 'account.logout_unsent_lost', { n: 1 })).toMatch(/^1 modifica non/);
  });

  it('say "Workspace" in every language', () => {
    expect(t('en', 'tenant.title')).toBe('Workspaces');
    expect(t('de', 'tenant.create')).toBe('Arbeitsbereich erstellen');
    expect(t('fr', 'tenant.create')).toBe('Créer un espace');
    expect(t('es', 'tenant.create')).toBe('Crear espacio');
    expect(t('it', 'tenant.create')).toBe('Crea spazio');
    const old = /\b(tenants?|mandant\w*|locataires?|inquilin\w*)\b/i;
    for (const lang of Object.keys(LOCALES) as Lang[]) {
      for (const ns of namespaces) {
        for (const [key, text] of Object.entries(LOCALES[lang][ns])) {
          expect(text, `${lang} ${ns}.${key}`).not.toMatch(old);
        }
      }
    }
  });

  it('name the workspace whose deletion is being confirmed', () => {
    expect(t('en', 'tenant.delete_type_name', { name: 'Club' })).toBe('Type “Club” to confirm');
    expect(t('en', 'tenant.delete_help', { name: 'Club' })).toContain('for every member');
    expect(t('en', 'account.delete_blocked_item', { name: 'Club', n: 1 })).toBe(
      '“Club”: 1 other member'
    );
  });
});
