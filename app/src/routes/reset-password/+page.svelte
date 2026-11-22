<script lang="ts">
  // Opened from a reset link (/reset-password?token=...): choose a new
  // password. The link works once and for an hour.
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { page } from '$app/state';
  import {
    afterResetComplete,
    call,
    clearSession,
    newPasswordProblem,
    type ResetCompleteState
  } from '#lib/account.js';
  import UpdatePrompt from '#lib/components/UpdatePrompt.svelte';
  import AppFooter from '#lib/components/AppFooter.svelte';
  import { hideUrlSecret } from '#lib/url-secret.js';
  import '#lib/components/auth/auth.css';

  const token = $derived(page.url.searchParams.get('token') ?? '');
  onMount(() => hideUrlSecret('token'));
  let view = $state<ResetCompleteState>('form');
  let password = $state('');
  let repeat = $state('');
  let busy = $state(false);
  let errorKey = $state('');

  async function submit(e: Event) {
    e.preventDefault();
    if (busy) return;
    errorKey = newPasswordProblem(password, repeat);
    if (errorKey) return;
    busy = true;
    const next = afterResetComplete(
      await call('/auth/password/reset', { token, new_password: password })
    );
    busy = false;
    errorKey = next.errorKey;
    view = next.state;
    if (next.state !== 'done') return;
    // The reset ended every session of the account, this device's included.
    clearSession();
    location.href = '/login?reset=1';
  }
</script>

<div class="auth-top"><UpdatePrompt /></div>
<div class="auth-page">
  <main class="card">
    <span class="mark" aria-hidden="true">⬡</span>
    <h1>{$_('auth.reset_title')}</h1>

    {#if !token || view === 'invalid'}
      <p class="text" role="alert">{$_('auth.reset_invalid')}</p>
      <a class="primary" href="/forgot-password">{$_('auth.reset_again')}</a>
    {:else if view === 'done'}
      <p class="text" role="status">{$_('auth.reset_done')}</p>
    {:else}
      <p class="muted">{$_('auth.reset_help')}</p>
      {#if errorKey}
        <p class="err" role="alert">{$_(errorKey)}</p>
      {/if}
      <form onsubmit={submit}>
        <label>
          <span>{$_('account.password_new')}</span>
          <input
            type="password"
            bind:value={password}
            autocomplete="new-password"
            minlength="8"
            required
          />
        </label>
        <label>
          <span>{$_('account.password_repeat')}</span>
          <input type="password" bind:value={repeat} autocomplete="new-password" required />
        </label>
        <button class="primary" type="submit" disabled={busy}>{$_('auth.reset_save')}</button>
      </form>
    {/if}

    <a class="link" href="/login">{$_('auth.back_to_signin')}</a>
  </main>
  <AppFooter />
</div>
