<script lang="ts">
  // Deleting the signed-in account: says exactly what goes and what stays,
  // names the workspaces that stand in the way, and asks for the password
  // (or a passkey, for accounts without one) before doing it.
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import {
    afterDeleteRequest,
    apiOrigin,
    authHeaders,
    call,
    clearSession,
    DELETE_ACCOUNT_HASH,
    deletionState,
    signInAgain,
    type AccountInfo,
    type WorkspaceRef
  } from '#lib/account.js';
  import { removeDeviceData } from '#lib/device-data.js';
  import { isPasskeyCancelled, passkeyReauth } from '#lib/webauthn.js';
  import './settings.css';

  let { account, orgId }: { account: AccountInfo; orgId: string } = $props();

  // Workspaces that turned out to block the deletion when it was attempted
  // (someone joined since the page loaded).
  let joined = $state<WorkspaceRef[]>([]);
  const plan = $derived(
    joined.length ? { ...account.deletion, blocked: joined } : account.deletion
  );
  const status = $derived(deletionState(plan));

  let open = $state(false);
  let password = $state('');
  let busy = $state(false);
  let errorKey = $state('');
  // The server wants a sign-in from the last five minutes first.
  let needsSignIn = $state(false);
  let section = $state<HTMLElement>();

  // Back from the sign-in this dialog asked for: open it again.
  onMount(() => {
    if (location.hash !== DELETE_ACCOUNT_HASH) return;
    open = true;
    section?.scrollIntoView();
  });

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
      const r = await call<{ workspaces?: WorkspaceRef[] }>('/auth/account/delete', {
        password,
        reauth
      });
      const outcome = afterDeleteRequest(r);
      if (outcome === 'sign_in_again') {
        needsSignIn = true;
        return;
      }
      if (!r.ok) {
        if (outcome === 'blocked') joined = (r.body.workspaces as WorkspaceRef[]) ?? [];
        else errorKey = r.errorKey;
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

<section class="st-card" id="delete-account" bind:this={section}>
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
      {:else if needsSignIn}
        <p class="st-warn" role="alert">{$_('account.delete_login_required')}</p>
        <div class="st-row">
          <button
            type="button"
            class="st-btn primary"
            onclick={() => signInAgain(`/settings${DELETE_ACCOUNT_HASH}`)}
          >
            {$_('account.delete_sign_in_again')}
          </button>
          <button type="button" class="st-btn" onclick={() => (needsSignIn = false)}>
            {$_('common.cancel')}
          </button>
        </div>
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
          {:else if plan.confirm === 'passkey'}
            <p class="st-help">{$_('account.delete_passkey')}</p>
          {:else}
            <p class="st-help">{$_('account.delete_login')}</p>
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
