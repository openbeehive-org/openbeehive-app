<script lang="ts">
  // Passkeys of the signed-in account. Shown only when the instance has
  // passkeys switched on; otherwise its endpoints do not exist.
  import { onMount } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { KeyRound } from '@lucide/svelte';
  import { apiOrigin, authHeaders } from '#lib/account.js';
  import { authErrorKey, GENERIC_AUTH_ERROR, NETWORK_AUTH_ERROR } from '#lib/auth-errors.js';
  import { formatDate } from '#lib/dates.js';
  import { fetchInstance } from '#lib/instance.js';
  import {
    enrollPasskey,
    listPasskeys,
    deletePasskey,
    isPasskeyCancelled,
    PasskeyError,
    passkeysSupported,
    type PasskeyInfo
  } from '#lib/webauthn.js';
  import './settings.css';

  let passkeys = $state<PasskeyInfo[]>([]);
  let name = $state('');
  let msgKey = $state('');
  let isError = $state(false);
  let busy = $state(false);
  let enabled = $state(false);

  async function load() {
    passkeys = await listPasskeys(apiOrigin(), authHeaders());
  }
  onMount(async () => {
    enabled = !!(await fetchInstance())?.webauthn;
    if (enabled) await load();
  });

  async function add(e: Event) {
    e.preventDefault();
    if (busy) return;
    busy = true;
    msgKey = '';
    isError = false;
    try {
      await enrollPasskey(apiOrigin(), name.trim(), authHeaders());
      name = '';
      msgKey = 'pk.added';
      await load();
    } catch (err) {
      // Closing the browser's passkey prompt is a decision, not an error.
      if (!isPasskeyCancelled(err)) {
        isError = true;
        msgKey =
          err instanceof PasskeyError
            ? authErrorKey({ code: err.code }, err.status, 'account.err_signed_out')
            : err instanceof TypeError
              ? NETWORK_AUTH_ERROR
              : GENERIC_AUTH_ERROR;
        if (msgKey === GENERIC_AUTH_ERROR) msgKey = 'pk.add_failed';
      }
    } finally {
      busy = false;
    }
  }

  async function remove(id: string) {
    if (!confirm($_('pk.delete_confirm'))) return;
    if (await deletePasskey(apiOrigin(), id, authHeaders())) await load();
  }
</script>

{#if enabled}
  <section class="st-card">
    <h2>{$_('pk.title')}</h2>
    <p class="st-help">{$_('pk.help')}</p>
    {#if passkeys.length}
      <ul class="st-list">
        {#each passkeys as k (k.id)}
          <li>
            <div class="st-meta">
              <strong><KeyRound size={16} aria-hidden="true" /> {k.name || $_('pk.unnamed')}</strong
              >
              <span class="st-date">{$_('pk.created')} {formatDate(k.created_at, $locale)}</span>
              {#if k.last_used_at}
                <span class="st-date"
                  >{$_('pk.last_used')} {formatDate(k.last_used_at, $locale)}</span
                >
              {/if}
            </div>
            <div class="st-actions">
              <button type="button" class="st-btn danger" onclick={() => remove(k.id)}>
                {$_('pk.delete')}
              </button>
            </div>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="st-help">{$_('pk.none')}</p>
    {/if}
    {#if passkeysSupported()}
      <form class="st-row" onsubmit={add}>
        <label class="st-field">
          <span>{$_('pk.name_label')}</span>
          <input bind:value={name} placeholder={$_('pk.name_ph')} disabled={busy} />
        </label>
        <button type="submit" class="st-btn" disabled={busy}>+ {$_('pk.add')}</button>
      </form>
    {:else}
      <p class="st-help">{$_('pk.not_supported')}</p>
    {/if}
    {#if msgKey}<p class="st-msg" class:err={isError} role="status">{$_(msgKey)}</p>{/if}
  </section>
{/if}
