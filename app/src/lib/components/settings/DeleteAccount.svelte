<script lang="ts">
  // Deleting the signed-in account: says exactly what goes and what stays,
  // names the workspaces that stand in the way, and asks for the password
  // (or a passkey, for accounts without one) before doing it.
  import { _ } from 'svelte-i18n';
  import {
    apiOrigin,
    authHeaders,
    call,
    clearSession,
    deletionState,
    type AccountInfo
  } from '#lib/account.js';
  import { removeDeviceData } from '#lib/device-data.js';
  import { isPasskeyCancelled, passkeyReauth } from '#lib/webauthn.js';
  import './settings.css';

  let { account, orgId }: { account: AccountInfo; orgId: string } = $props();

  const plan = $derived(account.deletion);
  const status = $derived(deletionState(plan));

  let open = $state(false);
  let password = $state('');
  let busy = $state(false);
  let errorKey = $state('');

  const names = (list: { name: string }[]) => list.map((w) => `“${w.name}”`).join(', ');

  async function remove(e: Event) {
    e.preventDefault();
    if (busy) return;
    busy = true;
    errorKey = '';
    try {
      let reauth = '';
      if (plan.confirm === 'passkey') {
        try {
          reauth = await passkeyReauth(apiOrigin(), authHeaders());
        } catch (err) {
          // Closing the passkey prompt cancels the deletion without a word.
          if (!isPasskeyCancelled(err)) errorKey = 'account.err_reauth_required';
          return;
        }
      }
      const r = await call('/auth/account/delete', { password, reauth });
      if (!r.ok) {
        errorKey = r.errorKey;
        return;
      }
      // The account is gone; do not leave its records on the device either.
      await removeDeviceData(orgId).catch(() => undefined);
      clearSession();
      location.href = '/login?deleted=1';
    } finally {
      busy = false;
    }
  }
</script>

<section class="st-card">
  <div class="st-danger-zone flush">
    <h2 class="st-sub">{$_('account.delete_title')}</h2>

    {#if status === 'instance_admin'}
      <p class="st-help">{$_('account.delete_protected_admin')}</p>
    {:else if status === 'demo'}
      <p class="st-help">{$_('account.err_demo')}</p>
    {:else}
      <p class="st-help">{$_('account.delete_intro')}</p>
      <ul class="st-bullets">
        <li>{$_('account.delete_personal')}</li>
        {#if plan.deletes.length}
          <li>{$_('account.delete_workspaces', { values: { names: names(plan.deletes) } })}</li>
        {/if}
        {#if plan.leaves.length}
          <li>{$_('account.delete_leaves', { values: { names: names(plan.leaves) } })}</li>
        {/if}
      </ul>

      {#if status === 'blocked'}
        <p class="st-warn" role="status">{$_('account.delete_blocked')}</p>
        <ul class="st-bullets">
          {#each plan.blocked as w (w.id)}
            <li>
              {$_('account.delete_blocked_item', { values: { name: w.name, n: w.members - 1 } })}
            </li>
          {/each}
        </ul>
      {:else if status === 'unconfirmable'}
        <p class="st-warn" role="status">{$_('account.delete_unconfirmable')}</p>
      {:else if !open}
        <button type="button" class="st-btn danger" onclick={() => (open = true)}>
          {$_('account.delete_start')}
        </button>
      {:else}
        <form class="st-stack" onsubmit={remove}>
          {#if plan.confirm === 'password'}
            <label class="st-field">
              <span>{$_('account.delete_password')}</span>
              <input
                type="password"
                bind:value={password}
                autocomplete="current-password"
                required
              />
            </label>
          {:else}
            <p class="st-help">{$_('account.delete_passkey')}</p>
          {/if}
          <div class="st-row">
            <button type="submit" class="st-btn danger-solid" disabled={busy}>
              {$_('account.delete_confirm')}
            </button>
            <button type="button" class="st-btn" disabled={busy} onclick={() => (open = false)}>
              {$_('common.cancel')}
            </button>
          </div>
        </form>
      {/if}
      {#if errorKey}<p class="st-msg err" role="alert">{$_(errorKey)}</p>{/if}
    {/if}
  </div>
</section>

<style>
  .flush {
    margin-top: 0;
  }
</style>
