<script lang="ts">
  // Invite someone to the workspace by email; open invites with their expiry.
  import { _, locale } from 'svelte-i18n';
  import { call } from '#lib/account.js';
  import { formatDate } from '#lib/dates.js';
  import { inviteExpired, type OpenInvite } from './members.js';
  import CopyButton from './CopyButton.svelte';
  import './settings.css';

  let { orgId }: { orgId: string } = $props();

  let email = $state('');
  let invites = $state<OpenInvite[]>([]);
  let newLink = $state('');
  let busy = $state(false);
  let errorKey = $state('');

  async function load() {
    const r = await call<{ invites?: OpenInvite[] }>(
      `/tenants/invites?org_id=${encodeURIComponent(orgId)}`
    );
    invites = r.ok ? (r.body.invites ?? []) : [];
  }
  $effect(() => {
    orgId;
    void load();
  });

  async function send(e: Event) {
    e.preventDefault();
    const address = email.trim();
    if (!address || busy) return;
    busy = true;
    errorKey = '';
    newLink = '';
    const r = await call<{ link?: string }>('/tenants/invite', { email: address, org_id: orgId });
    busy = false;
    if (!r.ok) {
      errorKey = r.code === 'invalid_email' ? 'tenant.invite_invalid_email' : r.errorKey;
      return;
    }
    newLink = r.body.link ?? '';
    email = '';
    await load();
  }

  async function revoke(invite: OpenInvite) {
    if (!confirm($_('tenant.invite_revoke_confirm', { values: { email: invite.email } }))) return;
    const r = await call('/tenants/invite/revoke', { id: invite.id, org_id: orgId });
    if (!r.ok) errorKey = r.errorKey;
    await load();
  }
</script>

<h3 class="st-sub">{$_('tenant.invite')}</h3>
<p class="st-help">{$_('tenant.invite_help')}</p>
<form class="st-row" onsubmit={send}>
  <label class="st-field">
    <span>{$_('tenant.invite_label')}</span>
    <input
      type="email"
      inputmode="email"
      autocapitalize="none"
      spellcheck="false"
      bind:value={email}
      placeholder={$_('tenant.invite_ph')}
      disabled={busy}
    />
  </label>
  <button type="submit" class="st-btn" disabled={busy || !email.trim()}>
    {$_('tenant.invite_send')}
  </button>
</form>
{#if errorKey}<p class="st-msg err" role="alert">{$_(errorKey)}</p>{/if}
{#if newLink}
  <div class="st-note" role="status">
    <p class="st-help">{$_('tenant.invite_sent')}</p>
    <code class="st-code">{newLink}</code>
    <CopyButton text={newLink} label={$_('tenant.copy_link')} />
  </div>
{/if}

{#if invites.length}
  <h3 class="st-sub">{$_('tenant.open_invites')}</h3>
  <ul class="st-list">
    {#each invites as i (i.id)}
      {@const expired = inviteExpired(i)}
      <li>
        <div class="st-meta">
          <strong>{i.email}</strong>
          <span class="st-badge"
            >{i.role === 'owner' ? $_('tenant.role_owner') : $_('tenant.role_member')}</span
          >
          {#if expired}
            <span class="st-badge bad">{$_('tenant.invite_expired')}</span>
          {:else if i.expires_at}
            <span class="st-date"
              >{$_('tenant.invite_expires', {
                values: { date: formatDate(i.expires_at, $locale) }
              })}</span
            >
          {/if}
        </div>
        <div class="st-actions">
          {#if !expired}<CopyButton text={i.link} label={$_('tenant.copy_link')} />{/if}
          <button type="button" class="st-btn danger" onclick={() => revoke(i)}>
            {$_('tenant.invite_revoke')}
          </button>
        </div>
      </li>
    {/each}
  </ul>
{/if}
