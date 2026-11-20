<script lang="ts">
  // "Forgot password?": ask for a reset link by email. A server that sends
  // no email says who can help instead.
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import {
    afterResetRequest,
    call,
    resetMode,
    type ResetMode,
    type ResetRequestState
  } from '#lib/account.js';
  import { fetchInstance } from '#lib/instance.js';
  import UpdatePrompt from '#lib/components/UpdatePrompt.svelte';
  import '#lib/components/auth/auth.css';

  let mode = $state<ResetMode | 'loading'>('loading');
  let view = $state<ResetRequestState>('form');
  let email = $state('');
  let busy = $state(false);
  let errorKey = $state('');

  onMount(async () => {
    const instance = await fetchInstance();
    mode = instance ? resetMode(instance) : 'email';
    if (mode === 'admin') view = 'needs_admin';
  });

  async function submit(e: Event) {
    e.preventDefault();
    if (busy) return;
    busy = true;
    const next = afterResetRequest(
      await call('/auth/password/reset-request', { email: email.trim() })
    );
    busy = false;
    view = next.state;
    errorKey = next.errorKey;
  }
</script>

<div class="auth-top"><UpdatePrompt /></div>
<div class="auth-page">
  <main class="card">
    <span class="mark" aria-hidden="true">⬡</span>
    <h1>{$_('auth.forgot_title')}</h1>

    {#if mode === 'loading'}
      <p class="muted" role="status">{$_('auth.entering')}</p>
    {:else if mode === 'off'}
      <p class="text">{$_('auth.forgot_off')}</p>
    {:else if view === 'needs_admin'}
      <p class="text" role="status">{$_('auth.forgot_needs_admin')}</p>
    {:else if view === 'sent'}
      <p class="text" role="status">{$_('auth.forgot_sent')}</p>
    {:else}
      <p class="muted">{$_('auth.forgot_help')}</p>
      {#if errorKey}
        <p class="err" role="alert">{$_(errorKey)}</p>
      {/if}
      <form onsubmit={submit}>
        <label>
          <span>{$_('auth.email')}</span>
          <input
            type="email"
            inputmode="email"
            bind:value={email}
            autocomplete="username"
            autocapitalize="none"
            spellcheck="false"
            required
          />
        </label>
        <button class="primary" type="submit" disabled={busy}>{$_('auth.forgot_send')}</button>
      </form>
    {/if}

    <a class="link" href="/login">{$_('auth.back_to_signin')}</a>
  </main>
</div>
