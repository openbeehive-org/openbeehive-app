<script lang="ts">
  // The signed-in account: who it is, signing out here or everywhere, and
  // its password.
  import { _ } from 'svelte-i18n';
  import { call, saveSession, type AccountInfo, type SessionPayload } from '#lib/account.js';
  import LogoutPanel from './LogoutPanel.svelte';
  import ChangePassword from './ChangePassword.svelte';
  import './settings.css';

  let {
    email,
    orgId,
    signedIn,
    account,
    readOnly = false
  }: {
    email: string;
    orgId: string;
    signedIn: boolean;
    account: AccountInfo | null;
    readOnly?: boolean;
  } = $props();

  let busy = $state(false);
  let everywhereKey = $state('');
  let everywhereErr = $state(false);

  async function logoutEverywhere() {
    if (busy || !confirm($_('account.logout_all_confirm'))) return;
    busy = true;
    const r = await call<SessionPayload>('/auth/logout-all', {});
    busy = false;
    everywhereErr = !r.ok;
    everywhereKey = r.ok ? 'account.logout_all_done' : r.errorKey;
    if (r.ok) saveSession(r.body);
  }
</script>

<section class="st-card">
  <h2>{$_('settings.account')}</h2>
  <p class="st-help">{$_('settings.user')}</p>
  <div class="user">
    <span class="ava" aria-hidden="true">⬡</span>
    <strong>{email}</strong>
  </div>

  {#if signedIn}
    <div class="st-row">
      <LogoutPanel {orgId} />
    </div>

    {#if !readOnly}
      <h3 class="st-sub">{$_('account.logout_all_title')}</h3>
      <p class="st-help">{$_('account.logout_all_help')}</p>
      <button type="button" class="st-btn" disabled={busy} onclick={logoutEverywhere}>
        {$_('account.logout_all')}
      </button>
      {#if everywhereKey}
        <p class="st-msg" class:err={everywhereErr} role="status">{$_(everywhereKey)}</p>
      {/if}

      {#if account && account.password !== 'off'}
        <h3 class="st-sub">{$_('account.password_title')}</h3>
        {#if account.password === 'set'}
          <p class="st-help">{$_('account.password_help')}</p>
          <ChangePassword email={account.email} />
        {:else if account.password === 'managed'}
          <p class="st-help">{$_('account.err_password_managed')}</p>
        {:else}
          <p class="st-help">{$_('account.err_no_password')}</p>
        {/if}
      {/if}
    {/if}
  {/if}
</section>

<style>
  .user {
    display: flex;
    align-items: center;
    gap: 12px;
    overflow-wrap: anywhere;
  }
  .ava {
    font-size: 1.6rem;
    color: var(--accent);
  }
</style>
