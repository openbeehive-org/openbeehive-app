<script lang="ts">
  // For the instance admin: create a password reset link for an account and
  // hand it to its owner. The only way to reset a password on a server that
  // sends no email.
  import { _, locale } from 'svelte-i18n';
  import { call } from '#lib/account.js';
  import { formatDate } from '#lib/dates.js';
  import CopyButton from './CopyButton.svelte';
  import './settings.css';

  let email = $state('');
  let link = $state('');
  let expires = $state('');
  let forEmail = $state('');
  let busy = $state(false);
  let errorKey = $state('');

  async function create(e: Event) {
    e.preventDefault();
    const address = email.trim();
    if (!address || busy) return;
    busy = true;
    errorKey = '';
    link = '';
    const r = await call<{ link?: string; expires_at?: string }>('/auth/admin/reset-link', {
      email: address
    });
    busy = false;
    if (!r.ok) {
      errorKey = r.errorKey;
      return;
    }
    link = r.body.link ?? '';
    expires = r.body.expires_at ?? '';
    forEmail = address;
    email = '';
  }
</script>

<section class="st-card">
  <h2>{$_('account.admin_reset_title')}</h2>
  <p class="st-help">{$_('account.admin_reset_help')}</p>
  <form class="st-row" onsubmit={create}>
    <label class="st-field">
      <span>{$_('account.admin_reset_email')}</span>
      <input
        type="email"
        inputmode="email"
        autocapitalize="none"
        spellcheck="false"
        bind:value={email}
        disabled={busy}
      />
    </label>
    <button type="submit" class="st-btn" disabled={busy || !email.trim()}>
      {$_('account.admin_reset_create')}
    </button>
  </form>
  {#if errorKey}<p class="st-msg err" role="alert">{$_(errorKey)}</p>{/if}
  {#if link}
    <div class="st-note" role="status">
      <p class="st-help">
        {$_('account.admin_reset_done', {
          values: {
            email: forEmail,
            time: formatDate(expires, $locale, { dateStyle: 'medium', timeStyle: 'short' })
          }
        })}
      </p>
      <code class="st-code">{link}</code>
      <CopyButton text={link} label={$_('tenant.copy_link')} />
    </div>
  {/if}
</section>
