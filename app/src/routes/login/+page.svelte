<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { _ } from 'svelte-i18n';
  import { passkeyLogin, passkeysSupported } from '$lib/webauthn';

  // Auth endpoints are on the API origin. Empty = same origin (single-binary prod).
  const API = (import.meta.env.BEEHIVE_API_URL ?? '').replace(/\/$/, '');

  type Mode = 'loading' | 'signin' | 'signup' | 'verify' | 'single';
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
  let error = $state('');

  const justVerified = $derived($page.url.searchParams.get('verified') === '1');
  // Invite links land here as /login?invite=<token> (see server tenant.go).
  const inviteToken = $derived($page.url.searchParams.get('invite') ?? '');
  const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

  onMount(async () => {
    try {
      const r = await fetch(`${API}/auth/instance`, { credentials: 'include' });
      if (!r.ok) throw new Error('no auth');
      const j = await r.json();
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
      if (inviteToken && hasPassword)
        mode = 'signup'; // invited: most likely a fresh account
      else mode = 'signin';
    } catch {
      mode = 'single';
      goto('/');
    }
  });

  function persist(j: any, fallbackEmail = '') {
    if (j.token) localStorage.setItem('session', j.token);
    if (j.user_id) localStorage.setItem('obh.userId', j.user_id);
    if (j.active_org) localStorage.setItem('obh.orgId', j.active_org);
    localStorage.setItem('obh.email', email.trim() || fallbackEmail);
    location.href = '/'; // full reload: opens the tenant store, starts sync
  }

  // Joins the invited tenant right after sign-in/up, so the invite link works in
  // one pass. Returns the (possibly updated) auth payload to persist.
  async function acceptInvite(j: any) {
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

  async function submit(path: 'signup' | 'signin') {
    if (busy) return;
    busy = true;
    error = '';
    try {
      const r = await fetch(`${API}/auth/${path}`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          email: email.trim(),
          password,
          name: name.trim(),
          invite: inviteToken
        })
      });
      let j = await r.json().catch(() => ({}));
      if (!r.ok) {
        error = j.error || $_('auth.err_generic');
        return;
      }
      if (j.status === 'verify') {
        mode = 'verify';
        return;
      }
      if (inviteToken) j = await acceptInvite(j);
      persist(j);
    } catch {
      error = $_('auth.err_generic');
    } finally {
      busy = false;
    }
  }

  async function passkeySignin() {
    if (busy) return;
    busy = true;
    error = '';
    try {
      let j = await passkeyLogin(API);
      if (inviteToken) j = await acceptInvite(j);
      persist(j, j.email ?? '');
    } catch (e) {
      error = String((e as Error)?.message ?? e) || $_('auth.err_generic');
    } finally {
      busy = false;
    }
  }

  async function demoLogin() {
    if (busy) return;
    busy = true;
    error = '';
    try {
      const r = await fetch(`${API}/auth/demo-login`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: '{}'
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) {
        error = j.error || $_('auth.err_generic');
        return;
      }
      persist(j, 'demo');
    } catch {
      error = $_('auth.err_generic');
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

<div class="wrap">
  <div class="card">
    <span class="mark">⬡</span>

    {#if mode === 'loading' || mode === 'single'}
      <p class="muted">{$_('auth.entering')}</p>
    {:else if mode === 'verify'}
      <h1>{$_('auth.verify_title')}</h1>
      <p class="muted">{$_('auth.verify_sent')}</p>
      <button class="link" onclick={() => (mode = 'signin')}>{$_('auth.back_to_signin')}</button>
    {:else}
      <h1>
        {#if mode === 'signup'}{$_('auth.create_title')}
        {:else}{$_('auth.signin_title')}{/if}
      </h1>
      <p class="muted">{$_('app.tagline')}</p>

      {#if inviteToken}
        <p class="notice">✉️ {$_('auth.invited_banner')}</p>
      {:else if !registration}
        <p class="notice">{$_('auth.invite_only_banner')}</p>
      {/if}

      {#if justVerified}<p class="ok">{$_('auth.verified_ok')}</p>{/if}
      {#if error}<p class="err">{error}</p>{/if}

      {#if demoEnabled}
        <button class="primary demo" onclick={demoLogin} disabled={busy}
          >🐝 {$_('auth.try_demo')}</button
        >
      {/if}

      {#if webauthn && mode === 'signin' && passkeysSupported()}
        <button class="provider pk" onclick={passkeySignin} disabled={busy}
          >🔑 {$_('auth.passkey_signin')}</button
        >
      {/if}

      {#if hasPassword}
        <form onsubmit={onSubmit}>
          {#if isSignup}
            <label
              ><span>{$_('auth.name')}</span>
              <input bind:value={name} autocomplete="name" /></label
            >
          {/if}
          <label
            ><span>{$_('auth.email')}</span>
            <input type="email" bind:value={email} autocomplete="email" required /></label
          >
          <label
            ><span>{$_('auth.password')}</span>
            <input
              type="password"
              bind:value={password}
              minlength="8"
              autocomplete={isSignup ? 'new-password' : 'current-password'}
              required
            /></label
          >
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
        <button
          class="link"
          onclick={() => {
            mode = 'signup';
            error = '';
          }}>{$_('auth.no_account')}</button
        >
      {:else if hasPassword && mode === 'signup'}
        <button
          class="link"
          onclick={() => {
            mode = 'signin';
            error = '';
          }}>{$_('auth.have_account')}</button
        >
      {/if}
    {/if}
  </div>
</div>

<style>
  .wrap {
    min-height: 100vh;
    display: grid;
    place-items: center;
    padding: 24px;
  }
  .card {
    width: 100%;
    max-width: 360px;
    background: var(--cream2, #fffdf7);
    border: 1px solid var(--line, #e5dcc6);
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
    color: var(--honey, #c77f22);
    line-height: 1;
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.5rem;
    margin: 6px 0 0;
  }
  .muted {
    color: var(--ink-soft, #6b5e48);
    font-size: 0.9rem;
    margin: 0 0 8px;
  }
  .ok {
    color: var(--moss, #5c6b4a);
    font-weight: 600;
    font-size: 0.88rem;
  }
  .notice {
    background: var(--cream, #fbf6ea);
    border: 1px solid var(--line, #e5dcc6);
    border-left: 3px solid var(--honey, #c77f22);
    border-radius: 10px;
    padding: 10px 12px;
    color: var(--ink-soft, #6b5e48);
    font-size: 0.85rem;
    margin: 4px 0;
    text-align: left;
    align-self: stretch;
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
    font-size: 0.8rem;
    font-weight: 600;
    color: var(--ink-soft, #6b5e48);
  }
  input {
    font: inherit;
    padding: 11px 13px;
    border: 1px solid var(--line, #e5dcc6);
    border-radius: 11px;
    background: #fff;
    color: var(--ink, #2c2316);
  }
  input:focus {
    outline: none;
    border-color: var(--honey, #c77f22);
  }
  .err {
    color: #b5402f;
    font-size: 0.85rem;
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
    background: var(--honey, #c77f22);
    color: #fff;
    margin-top: 4px;
    width: 100%;
  }
  .primary:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .primary.demo {
    background: linear-gradient(90deg, var(--honey-l, #e9a744), var(--honey, #c77f22));
  }
  .or {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 10px;
    color: var(--ink-soft, #6b5e48);
    font-size: 0.78rem;
    margin: 14px 0 2px;
  }
  .or::before,
  .or::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--line, #e5dcc6);
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
    border: 1px solid var(--line, #e5dcc6);
    border-radius: 12px;
    text-decoration: none;
    color: var(--ink, #2c2316);
    font-weight: 600;
    background: #fff;
  }
  .provider:hover {
    border-color: var(--honey, #c77f22);
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
    color: var(--honey-d, #a4641a);
    font: inherit;
    font-weight: 600;
    font-size: 0.88rem;
    margin-top: 12px;
  }
</style>
