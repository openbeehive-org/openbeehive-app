<script lang="ts">
  import { errorKey } from '#lib/errors.js';
  import { _ } from 'svelte-i18n';
  import { page } from '$app/state';
  import { MapPin } from '@lucide/svelte';
  import { apiaries, hives } from '#lib/local/repo.js';
  import type { ApiaryRow } from '../rows.js';
  import { dataVersion, initialSyncPending } from '#lib/local/live.js';
  import ApiaryMap from '#lib/components/ApiaryMap.svelte';
  import LocationPicker from '#lib/components/LocationPicker.svelte';
  import Skeleton from '#lib/components/Skeleton.svelte';
  import Syncing from '#lib/components/Syncing.svelte';

  let rows = $state<ApiaryRow[]>([]);
  let counts = $state<Record<string, number>>({});
  let loaded = $state(false);
  // "/apiaries?new=1" (dashboard, first-run checklist) opens the form directly.
  let showForm = $state(page.url.searchParams.has('new'));
  let name = $state('');
  let address = $state('');
  let note = $state('');
  let lat = $state(0);
  let lng = $state(0);
  let saving = $state(false);
  let saveError = $state('');

  async function load() {
    rows = (await apiaries.list()) as ApiaryRow[];
    counts = await hives.countByApiary();
    loaded = true;
  }

  async function create() {
    if (!name.trim() || saving) return;
    saving = true;
    saveError = '';
    try {
      await apiaries.create({
        organization_id: localStorage.getItem('obh.orgId') ?? 'local',
        name: name.trim(),
        address: address.trim(),
        note: note.trim(),
        lat: lat || 0,
        lng: lng || 0
      });
      // Success: clear and close. On failure the input stays so nothing is lost.
      name = address = note = '';
      lat = lng = 0;
      showForm = false;
      await load();
    } catch (e) {
      saveError = errorKey(e);
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<svelte:head><title>{$_('apiaries.title')}</title></svelte:head>

<div class="page">
  <header>
    <h1>{$_('apiaries.title')}</h1>
    <button class="primary" aria-expanded={showForm} onclick={() => (showForm = !showForm)}
      >+ {$_('common.new')}</button
    >
  </header>

  {#if showForm}
    <form
      class="card form"
      onsubmit={(e) => {
        e.preventDefault();
        create();
      }}
    >
      <label class="field">
        <span>{$_('apiaries.name')}</span>
        <input bind:value={name} placeholder={$_('apiaries.name_ph')} />
      </label>
      <label class="field">
        <span>{$_('apiaries.address')}</span>
        <input bind:value={address} autocomplete="street-address" />
      </label>
      <label class="field">
        <span>{$_('apiaries.note')}</span>
        <input bind:value={note} />
      </label>
      <LocationPicker bind:lat bind:lng bind:address />
      {#if saveError}
        <p class="error" role="alert">
          {$_('common.save_failed', { values: { msg: $_(saveError) } })}
        </p>
      {/if}
      <div class="actions">
        <button type="button" class="ghost" onclick={() => (showForm = false)}
          >{$_('common.cancel')}</button
        >
        <button type="submit" class="primary" disabled={!name.trim() || saving}>
          {saving ? $_('common.saving') : $_('common.save')}
        </button>
      </div>
    </form>
  {/if}

  {#if loaded && rows.length > 0}
    <div class="mapwrap"><ApiaryMap apiaries={rows} /></div>
  {/if}

  {#if !loaded}
    <Skeleton n={3} height={74} />
  {:else if rows.length === 0 && $initialSyncPending}
    <Syncing />
  {:else if rows.length === 0}
    <p class="muted empty">{$_('apiaries.empty')}</p>
  {:else}
    <ul class="list">
      {#each rows as a}
        <li class="card">
          <a href={`/apiaries/${a.id}`}>
            <div class="lead"><span class="pin"><MapPin size={22} aria-hidden="true" /></span></div>
            <div class="body">
              <strong>{a.name}</strong>
              {#if a.address}<small>{a.address}</small>{/if}
            </div>
            <span class="badge"
              >{$_('apiaries.hives_count', { values: { n: counts[a.id] ?? 0 } })}</span
            >
            <span class="chev" aria-hidden="true">›</span>
          </a>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .page {
    max-width: 720px;
    margin: 0 auto;
    padding: 26px 18px 96px;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 18px;
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.9rem;
  }
  .muted {
    color: var(--text-soft);
  }
  .empty {
    text-align: center;
    padding: 40px 0;
  }
  .mapwrap {
    margin-bottom: 18px;
  }
  .primary,
  .ghost {
    min-height: 44px;
    border-radius: 11px;
    padding: 10px 16px;
    font: inherit;
    font-weight: 700;
    cursor: pointer;
  }
  .primary {
    background: var(--accent);
    color: var(--on-accent);
    border: none;
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .ghost {
    background: transparent;
    border: 1px solid var(--border-strong);
    color: var(--text);
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 16px;
  }
  .form {
    padding: 16px;
    display: grid;
    gap: 12px;
    margin-bottom: 18px;
  }
  .field {
    display: grid;
    gap: 4px;
  }
  .field span {
    font-size: 0.82rem;
    font-weight: 600;
    color: var(--text-soft);
  }
  .field input {
    font: inherit;
    min-height: 44px;
    padding: 10px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    background: var(--surface);
    color: var(--text);
  }
  .form .actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
    margin-top: 4px;
  }
  .form .error {
    color: var(--danger);
    background: color-mix(in srgb, var(--danger) 8%, transparent);
    border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
    border-radius: 10px;
    padding: 9px 12px;
    font-size: 0.85rem;
  }
  .list {
    list-style: none;
    display: grid;
    gap: 12px;
  }
  .list li a {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 14px 16px;
    min-height: 44px;
    text-decoration: none;
    color: var(--text);
  }
  .lead .pin {
    display: flex;
    color: var(--accent-text);
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .body strong {
    display: block;
    font-weight: 700;
    overflow-wrap: anywhere;
  }
  .body small {
    color: var(--text-soft);
    font-size: 0.85rem;
  }
  .badge {
    background: color-mix(in srgb, var(--ok) 16%, transparent);
    color: var(--text);
    font-weight: 700;
    font-size: 0.78rem;
    padding: 5px 11px;
    border-radius: 999px;
    white-space: nowrap;
  }
  .chev {
    color: var(--text-soft);
    font-size: 1.4rem;
  }
</style>
