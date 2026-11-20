<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { _ } from 'svelte-i18n';
  import { KeyRound, Mail } from '@lucide/svelte';
  import { authErrorKey, GENERIC_AUTH_ERROR, NETWORK_AUTH_ERROR } from '#lib/auth-errors.js';
  import UpdatePrompt from '#lib/components/UpdatePrompt.svelte';
  import { fetchInstance } from '#lib/instance.js';
  import { resetMode, type ResetMode } from '#lib/account.js';
  import '#lib/components/auth/auth.css';
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
  // Set by the pages that send the user back here after finishing.
  const noticeKey = $derived(
    page.url.searchParams.get('reset') === '1'
      ? 'auth.reset_done'
      : page.url.searchParams.get('deleted') === '1'
        ? 'account.deleted'
        : ''
  );
  // Sent here to confirm something with a fresh sign-in (see signInAgain).
  const reauth = $derived(page.url.searchParams.get('reauth') === '1');
  let forgot = $state<ResetMode>('off');
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
    forgot = resetMode(j);
    // A link the browser opened (email verification, the return from a
    // sign-in provider) failed; the server sent the reason along.
    const failure = page.url.searchParams.get('error');
    if (failure) errorKey = authErrorKey({ code: failure });
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

<div class="auth-top"><UpdatePrompt /></div>
<div class="auth-page">
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

      {#if reauth}
        <p class="notice" role="status">{$_('auth.reauth_notice')}</p>
      {:else if inviteToken}
        <p class="notice"><Mail size={16} aria-hidden="true" /> {$_('auth.invited_banner')}</p>
      {:else if !registration}
        <p class="notice">{$_('auth.invite_only_banner')}</p>
      {/if}

      {#if justVerified}
        <p class="ok" role="status">{$_('auth.verified_ok')}</p>
      {/if}
      {#if noticeKey}
        <p class="ok" role="status">{$_(noticeKey)}</p>
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
          {#if !isSignup && forgot !== 'off'}
            <a class="link forgot" href="/forgot-password">{$_('auth.forgot_link')}</a>
          {/if}
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
