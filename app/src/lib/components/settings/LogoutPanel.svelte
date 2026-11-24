<script lang="ts">
  // Signing out. Changes that have not reached the server are counted first,
  // and a shared device can be left without the workspace's records on it.
  import { _ } from 'svelte-i18n';
  import { apiOrigin, authHeaders, clearSession, logoutWarning } from '#lib/account.js';
  import { removeDeviceData } from '#lib/device-data.js';
  import { CodedError, errorKey } from '#lib/errors.js';
  import { unsentCount } from '#lib/local/sync.js';
  import './settings.css';

  let { orgId }: { orgId: string } = $props();

  let open = $state(false);
  let unsent = $state(0);
  let removeData = $state(false);
  let busy = $state(false);
  // Translation key of why the data could not be removed, or ''.
  let failed = $state('');
  const warning = $derived(logoutWarning(unsent, removeData));

  async function ask() {
    failed = '';
    removeData = false;
    unsent = 0;
    open = true;
    unsent = await unsentCount().catch(() => 0);
  }

  async function signOut() {
    if (busy) return;
    busy = true;
    failed = '';
    if (removeData) {
      try {
        await removeDeviceData(orgId);
      } catch (e) {
        // Stay signed in: the user asked for the data to be gone first.
        failed = e instanceof CodedError ? errorKey(e) : 'account.logout_remove_failed';
        busy = false;
        return;
      }
    }
    try {
      await fetch(`${apiOrigin()}/auth/logout`, {
        method: 'POST',
        credentials: 'include',
        headers: authHeaders()
      });
    } catch {
      // Offline: the session is forgotten on this device all the same.
    }
    clearSession();
    location.href = '/login';
  }
</script>

{#if !open}
  <button type="button" class="st-btn" onclick={ask}>{$_('settings.logout')}</button>
{:else}
  <div class="st-note" role="group" aria-label={$_('settings.logout')}>
    <p class="st-help">{$_('account.logout_help')}</p>
    {#if warning}
      <p class="st-warn" role="alert">{$_(warning, { values: { n: unsent } })}</p>
    {/if}
    <label class="st-check">
      <input type="checkbox" bind:checked={removeData} disabled={busy} />
      <span>
        {$_('account.logout_remove_data')}
        <small>{$_('account.logout_remove_data_help')}</small>
      </span>
    </label>
    {#if failed}<p class="st-msg err" role="alert">{$_(failed)}</p>{/if}
    <div class="st-row">
      <button
        type="button"
        class="st-btn"
        class:danger-solid={removeData}
        class:primary={!removeData}
        disabled={busy}
        onclick={signOut}
      >
        {removeData ? $_('account.logout_and_remove') : $_('settings.logout')}
      </button>
      <button type="button" class="st-btn" disabled={busy} onclick={() => (open = false)}>
        {$_('common.cancel')}
      </button>
    </div>
  </div>
{/if}
