<script lang="ts">
  // API keys for scripts and devices, bound to the account and the active
  // workspace. A new key is shown once, with a request that uses it.
  import { onMount } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { Settings } from '@lucide/svelte';
  import { apiOrigin, authHeaders } from '#lib/account.js';
  import { formatDate } from '#lib/dates.js';
  import {
    API_DOCS_URL,
    apiBaseUrl,
    curlExample,
    listApiKeys,
    createApiKey,
    deleteApiKey,
    type ApiKeyInfo,
    type ApiKeyScope
  } from '#lib/apikeys.js';
  import CopyButton from './CopyButton.svelte';
  import './settings.css';

  // Workspace admins also see and remove the other members' keys.
  let { isOwner }: { isOwner: boolean } = $props();

  let mine = $state<ApiKeyInfo[]>([]);
  let others = $state<ApiKeyInfo[]>([]);
  let name = $state('');
  let scope = $state<ApiKeyScope>('write');
  let expiry = $state(0); // days; 0 = never
  let token = $state('');
  let failed = $state(false);
  let busy = $state(false);

  const baseUrl = apiBaseUrl(apiOrigin(), location.origin);
  const example = $derived(curlExample(baseUrl, token));

  async function load() {
    mine = await listApiKeys(apiOrigin(), authHeaders());
    others = isOwner
      ? (await listApiKeys(apiOrigin(), authHeaders(), true)).filter((k) => !k.mine)
      : [];
  }
  onMount(load);

  async function add(e: Event) {
    e.preventDefault();
    if (busy) return;
    busy = true;
    failed = false;
    token = '';
    try {
      token = (await createApiKey(apiOrigin(), name.trim(), scope, expiry, authHeaders())).token;
      name = '';
      scope = 'write';
      expiry = 0;
      await load();
    } catch {
      failed = true;
    } finally {
      busy = false;
    }
  }

  const isExpired = (k: ApiKeyInfo) => !!k.expires_at && new Date(k.expires_at) <= new Date();

  async function remove(id: string, theirs: boolean) {
    if (!confirm($_(theirs ? 'apikeys.delete_theirs_confirm' : 'apikeys.delete_confirm'))) return;
    if (await deleteApiKey(apiOrigin(), id, authHeaders())) await load();
  }
</script>

{#snippet keyList(keys: ApiKeyInfo[], theirs: boolean)}
  <ul class="st-list">
    {#each keys as k (k.id)}
      <li>
        <div class="st-meta">
          <strong>
            <Settings size={16} aria-hidden="true" />
            {k.name || $_('apikeys.unnamed')}
          </strong>
          <code class="st-badge st-mono">{k.prefix}...</code>
          <span class="st-badge">
            {k.scope === 'read' ? $_('apikeys.scope_read') : $_('apikeys.scope_write')}
          </span>
          {#if theirs}
            <span class="st-date">{k.user_email}</span>
          {:else}
            <span class="st-date">{$_('apikeys.created')} {formatDate(k.created_at, $locale)}</span>
          {/if}
          {#if k.expires_at}
            <span class={isExpired(k) ? 'st-badge bad' : 'st-date'}>
              {isExpired(k) ? $_('apikeys.expired') : $_('apikeys.expires')}
              {formatDate(k.expires_at, $locale)}
            </span>
          {/if}
          {#if k.last_used_at}
            <span class="st-date">
              {$_('apikeys.last_used')}
              {formatDate(k.last_used_at, $locale)}
            </span>
          {/if}
        </div>
        <div class="st-actions">
          <button type="button" class="st-btn danger" onclick={() => remove(k.id, theirs)}>
            {$_('apikeys.delete')}
          </button>
        </div>
      </li>
    {/each}
  </ul>
{/snippet}

<section class="st-card">
  <h2>{$_('apikeys.title')}</h2>
  <p class="st-help">{$_('apikeys.help')}</p>
  {#if mine.length}
    {@render keyList(mine, false)}
  {:else}
    <p class="st-help">{$_('apikeys.none')}</p>
  {/if}

  <form class="st-row" onsubmit={add}>
    <label class="st-field">
      <span>{$_('apikeys.name_label')}</span>
      <input bind:value={name} placeholder={$_('apikeys.name_ph')} disabled={busy} />
    </label>
    <label class="st-field narrow">
      <span>{$_('apikeys.scope_label')}</span>
      <select bind:value={scope} disabled={busy}>
        <option value="write">{$_('apikeys.scope_write')}</option>
        <option value="read">{$_('apikeys.scope_read')}</option>
      </select>
    </label>
    <label class="st-field narrow">
      <span>{$_('apikeys.expiry_label')}</span>
      <select bind:value={expiry} disabled={busy}>
        <option value={0}>{$_('apikeys.expiry_never')}</option>
        <option value={30}>{$_('apikeys.expiry_30')}</option>
        <option value={90}>{$_('apikeys.expiry_90')}</option>
        <option value={365}>{$_('apikeys.expiry_365')}</option>
      </select>
    </label>
    <button type="submit" class="st-btn" disabled={busy}>+ {$_('apikeys.add')}</button>
  </form>
  {#if failed}<p class="st-msg err" role="alert">{$_('apikeys.create_failed')}</p>{/if}

  {#if token}
    <div class="st-note" role="status">
      <p class="st-help">{$_('apikeys.new_help')}</p>
      <div class="st-row">
        <label class="st-field">
          <span>{$_('apikeys.new_label')}</span>
          <input class="st-mono" readonly value={token} onfocus={(e) => e.currentTarget.select()} />
        </label>
        <CopyButton text={token} label={$_('apikeys.copy')} />
      </div>
      <h3 class="st-sub">{$_('apikeys.usage_title')}</h3>
      <p class="st-help">
        {$_('apikeys.usage_base')} <code class="st-mono">{baseUrl}</code>
      </p>
      <p class="st-help">{$_('apikeys.usage_help')}</p>
      <code class="st-code">{example}</code>
      <div class="st-row">
        <CopyButton text={example} label={$_('apikeys.usage_copy')} />
        <a class="st-btn" href={API_DOCS_URL} target="_blank" rel="noopener noreferrer">
          {$_('apikeys.usage_docs')} ›
        </a>
      </div>
    </div>
  {/if}

  {#if isOwner && others.length}
    <h3 class="st-sub">{$_('apikeys.tenant_title')}</h3>
    <p class="st-help">{$_('apikeys.tenant_help')}</p>
    {@render keyList(others, true)}
  {/if}
</section>
