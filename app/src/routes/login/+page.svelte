<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { _ } from 'svelte-i18n';
  import { KeyRound, Mail } from '@lucide/svelte';
  import { authErrorKey, GENERIC_AUTH_ERROR, NETWORK_AUTH_ERROR } from '#lib/auth-errors.js';
  import UpdatePrompt from '#lib/components/UpdatePrompt.svelte';
  import { fetchInstance } from '#lib/instance.js';
  import {
    isPasskeyCancelled,
    passkeyLogin,
    PasskeyError,
    passkeysSupported
  } from '#lib/webauthn.js';

  // Auth endpoints are on the API origin. Empty = same origin (single-binary prod).
  const API = (import.meta.env.BEEHIVE_API_URL ?? '').replace(/\/$/, '');

  type Mode = 'loading' | 'signin' | 'signup' | 'verify' | 'single';
  // JSON body of the auth endpoints (signin/signup/demo-login/accept-invite).
  type AuthPayload = {
    token?: string;
    user_id?: string;
    active_org?: string;
    status?: string;
    error?: string;
    code?: string;
  };
  let mode = $state<Mode>('loading');
  let hasPassword = $state(true);
  let registration = $state(true);
  let providers = $state<string[]>([]);
  let webauthn = $state(false);
  let demoEnabled = $state(false);
  let email = $state('');
  let password = $state('');
  let name = $state('');
  let busy = $state(false);
  // Translation key of the current error; never the server's raw text.
  let errorKey = $state('');
  let heading = $state<HTMLHeadingElement>();

  const justVerified = $derived(page.url.searchParams.get('verified') === '1');
  // Invite links land here as /login?invite=<token> (see server tenant.go).
  const inviteToken = $derived(page.url.searchParams.get('invite') ?? '');
  const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

  onMount(async () => {
    const j = await fetchInstance();
    // Server unreachable (offline): the app itself still opens from the device.
    if (!j) {
      mode = 'single';
      goto('/');
      return;
    }
    hasPassword = !!j.password_auth;
    registration = j.registration !== false;
    providers = j.oidc_providers ?? [];
    webauthn = !!j.webauthn;
    demoEnabled = !!j.demo;
    // Nothing to log into (single-user self-host): go straight to the app.
    if (!hasPassword && !providers.length && !webauthn && !demoEnabled) {
      mode = 'single';
      goto('/');
      return;
    }
    // Invited: most likely a fresh account.
    mode = inviteToken && hasPassword ? 'signup' : 'signin';
  });

  // Switches the form and moves focus to its heading, so keyboard and screen
  // reader users notice that the page changed.
  async function show(next: Mode) {
    mode = next;
    errorKey = '';
    await tick();
    heading?.focus();
  }

  function persist(j: AuthPayload, fallbackEmail = '') {
    if (j.token) localStorage.setItem('session', j.token);
    if (j.user_id) localStorage.setItem('obh.userId', j.user_id);
    if (j.active_org) localStorage.setItem('obh.orgId', j.active_org);
    localStorage.setItem('obh.email', email.trim() || fallbackEmail);
    location.href = '/'; // full reload: opens the tenant store, starts sync
  }

  // Joins the invited tenant right after sign-in/up, so the invite link works in
  // one pass. Returns the (possibly updated) auth payload to persist.
  async function acceptInvite(j: AuthPayload): Promise<AuthPayload> {
    try {
      const r = await fetch(`${API}/auth/accept-invite`, {
        method: 'POST',
        credentials: 'include',
        headers: {
          'Content-Type': 'application/json',
          ...(j.token ? { Authorization: `Bearer ${j.token}` } : {})
        },
        body: JSON.stringify({ token: inviteToken })
      });
      const a = await r.json().catch(() => ({}));
      if (r.ok) return { ...j, ...a }; // token + active_org now point at the joined tenant
    } catch {
      /* joining failed; continue into the personal tenant */
    }
    return j;
  }

  // POSTs to an auth endpoint. Returns the payload, or null after setting
  // errorKey. `on401` is what a 401 without an error code means here.
  async function post(path: string, body: string, on401?: string): Promise<AuthPayload | null> {
    let r: Response;
    try {
      r = await fetch(`${API}/auth/${path}`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body
      });
    } catch {
      errorKey = NETWORK_AUTH_ERROR;
      return null;
    }
    const j: AuthPayload = await r.json().catch(() => ({}));
    if (!r.ok) {
      errorKey = authErrorKey(j, r.status, on401);
      return null;
    }
    return j;
  }

  async function submit(path: 'signup' | 'signin') {
    if (busy) return;
    busy = true;
    errorKey = '';
    try {
      let j = await post(
        path,
        JSON.stringify({ email: email.trim(), password, name: name.trim(), invite: inviteToken }),
        'auth.err_invalid_credentials'
      );
      if (!j) return;
      if (j.status === 'verify') {
        await show('verify');
        return;
      }
      if (inviteToken) j = await acceptInvite(j);
      persist(j);
    } finally {
      busy = false;
    }
  }

  async function passkeySignin() {
    if (busy) return;
    busy = true;
    errorKey = '';
    try {
      let j = await passkeyLogin(API);
      if (inviteToken) j = await acceptInvite(j);
      persist(j, j.email ?? '');
    } catch (e) {
      // Closing the browser's passkey prompt is a decision, not an error.
      if (isPasskeyCancelled(e)) return;
      errorKey =
        e instanceof PasskeyError
          ? authErrorKey({ code: e.code }, e.status, 'auth.err_passkey_failed')
          : e instanceof TypeError
            ? NETWORK_AUTH_ERROR
            : GENERIC_AUTH_ERROR;
    } finally {
      busy = false;
    }
  }

  async function demoLogin() {
    if (busy) return;
    busy = true;
    errorKey = '';
    try {
      const j = await post('demo-login', '{}');
      if (j) persist(j, 'demo');
    } finally {
      busy = false;
    }
  }

  const isSignup = $derived(mode === 'signup');
  function onSubmit(e: Event) {
    e.preventDefault();
    submit(isSignup ? 'signup' : 'signin');
  }
</script>

<div class="top"><UpdatePrompt /></div>
<div class="wrap">
  <main class="card">
    <span class="mark" aria-hidden="true">⬡</span>

    {#if mode === 'loading' || mode === 'single'}
      <p class="muted" role="status">{$_('auth.entering')}</p>
    {:else if mode === 'verify'}
      <h1 tabindex="-1" bind:this={heading}>{$_('auth.verify_title')}</h1>
      <p class="muted">{$_('auth.verify_sent')}</p>
      <button type="button" class="link" onclick={() => show('signin')}>
        {$_('auth.back_to_signin')}
      </button>
    {:else}
      <h1 tabindex="-1" bind:this={heading}>
        {#if mode === 'signup'}{$_('auth.create_title')}
        {:else}{$_('auth.signin_title')}{/if}
      </h1>
      <p class="muted">{$_('app.tagline')}</p>

      {#if inviteToken}
        <p class="notice"><Mail size={16} aria-hidden="true" /> {$_('auth.invited_banner')}</p>
      {:else if !registration}
        <p class="notice">{$_('auth.invite_only_banner')}</p>
      {/if}

      {#if justVerified}
        <p class="ok" role="status">{$_('auth.verified_ok')}</p>
      {/if}

      {#if errorKey}
        <p class="err" role="alert">{$_(errorKey)}</p>
      {/if}

      {#if demoEnabled}
        <div class="demo">
          <p>{$_('auth.demo_hint')}</p>
          <button type="button" class="primary" onclick={demoLogin} disabled={busy}>
            {$_('auth.try_demo')}
          </button>
        </div>
        {#if hasPassword || providers.length || webauthn}
          <div class="or"><span>{$_('auth.or')}</span></div>
        {/if}
      {/if}

      {#if webauthn && mode === 'signin' && passkeysSupported()}
        <button type="button" class="provider pk" onclick={passkeySignin} disabled={busy}
          ><KeyRound size={16} aria-hidden="true" /> {$_('auth.passkey_signin')}</button
        >
      {/if}

      {#if hasPassword}
        <form onsubmit={onSubmit}>
          {#if isSignup}
            <label>
              <span>{$_('auth.name')}</span>
              <input type="text" bind:value={name} autocomplete="name" autocapitalize="words" />
            </label>
          {/if}

          <label>
            <span>{$_('auth.email')}</span>
            <input
              type="email"
              inputmode="email"
              bind:value={email}
              autocomplete={isSignup ? 'email' : 'username'}
              autocapitalize="none"
              spellcheck="false"
              required
            />
          </label>

          <label>
            <span>{$_('auth.password')}</span>
            <input
              type="password"
              bind:value={password}
              minlength={isSignup ? 8 : undefined}
              autocomplete={isSignup ? 'new-password' : 'current-password'}
              required
            />
          </label>
          <button class="primary" type="submit" disabled={busy}>
            {#if mode === 'signup'}{$_('auth.signup')}
            {:else}{$_('auth.signin')}{/if}
          </button>
        </form>
      {/if}

      {#if providers.length}
        {#if hasPassword}<div class="or"><span>{$_('auth.or')}</span></div>{/if}
        <div class="providers">
          {#each providers as p}
            <a class="provider" href={`${API}/auth/login?provider=${p}`}
              >{$_('auth.continue_with', { values: { provider: cap(p) } })}</a
            >
          {/each}
        </div>
      {/if}

      {#if hasPassword && mode === 'signin' && (registration || inviteToken)}
        <button type="button" class="link" onclick={() => show('signup')}>
          {$_('auth.no_account')}
        </button>
      {:else if hasPassword && mode === 'signup'}
        <button type="button" class="link" onclick={() => show('signin')}>
          {$_('auth.have_account')}
        </button>
      {/if}
    {/if}
  </main>
</div>

<style>
  .top {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    z-index: 10;
  }
  .wrap {
    min-height: 100vh;
    display: grid;
    place-items: center;
    padding: 24px;
  }
  .card {
    width: 100%;
    max-width: 360px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 20px;
    padding: 30px 26px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    text-align: center;
  }
  .mark {
    font-size: 2.6rem;
    color: var(--accent);
    line-height: 1;
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.5rem;
    margin: 6px 0 0;
  }
  h1:focus {
    outline: none;
  }
  .muted {
    color: var(--text-soft);
    font-size: 0.9rem;
    margin: 0 0 8px;
  }
  .ok {
    color: var(--ok);
    font-weight: 600;
    font-size: 0.88rem;
  }
  .wrap :global(.lucide-icon) {
    vertical-align: -0.15em;
  }
  .notice {
    background: var(--bg);
    border: 1px solid var(--border);
    border-left: 3px solid var(--accent);
    border-radius: 10px;
    padding: 10px 12px;
    color: var(--text-soft);
    font-size: 0.85rem;
    margin: 4px 0;
    text-align: left;
    align-self: stretch;
  }
  .demo {
    align-self: stretch;
    background: var(--warn-bg);
    color: var(--warn-text);
    border-radius: 14px;
    padding: 14px;
    margin-top: 4px;
  }
  .demo p {
    font-size: 0.88rem;
    font-weight: 600;
    margin-bottom: 8px;
  }
  form {
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 12px;
    margin-top: 4px;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 5px;
    text-align: left;
  }
  label span {
    font-size: 0.85rem;
    font-weight: 600;
    color: var(--text-soft);
  }
  /* 16px at least: iOS zooms the page when a smaller field gets focus. */
  input {
    font: inherit;
    font-size: 1rem;
    padding: 11px 13px;
    border: 1px solid var(--border-strong);
    border-radius: 11px;
    background: var(--bg);
    color: var(--text);
  }
  input:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .err {
    color: var(--danger);
    font-size: 0.88rem;
    font-weight: 600;
    margin: 0;
    text-align: left;
    align-self: stretch;
  }
  .primary {
    font: inherit;
    font-weight: 700;
    cursor: pointer;
    border: none;
    border-radius: 12px;
    padding: 12px;
    background: var(--accent);
    color: var(--on-accent);
    margin-top: 4px;
    width: 100%;
  }
  .primary:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .or {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 10px;
    color: var(--text-soft);
    font-size: 0.8rem;
    margin: 14px 0 2px;
  }
  .or::before,
  .or::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--border);
  }
  .providers {
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .provider {
    display: block;
    padding: 11px;
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    text-decoration: none;
    color: var(--text);
    font-weight: 600;
    background: var(--bg);
  }
  .provider:hover {
    border-color: var(--accent);
  }
  .provider.pk {
    font: inherit;
    font-weight: 600;
    width: 100%;
    cursor: pointer;
    margin-top: 2px;
  }
  .provider.pk:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .link {
    background: none;
    border: none;
    cursor: pointer;
    color: var(--accent-text);
    font: inherit;
    font-weight: 600;
    font-size: 0.88rem;
    margin-top: 12px;
  }
</style>
