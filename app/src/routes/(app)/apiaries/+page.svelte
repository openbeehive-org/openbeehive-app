<script lang="ts">
  import { _ } from 'svelte-i18n';
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
  let showForm = $state(false);
  let name = $state('');
  let address = $state('');
  let note = $state('');
  let lat = $state(0);
  let lng = $state(0);
  let saving = $state(false);
  let saveError = $state('');

  async function load() {
    rows = (await apiaries.list()) as ApiaryRow[];
    const c: Record<string, number> = {};
    for (const a of rows) c[a.id] = await hives.count(a.id);
    counts = c;
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
      saveError = (e as Error)?.message ?? String(e);
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<div class="page">
  <header>
    <h1>{$_('apiaries.title')}</h1>
    <button class="primary" onclick={() => (showForm = !showForm)}>+ {$_('common.new')}</button>
  </header>

  {#if showForm}
    <form
      class="card form"
      onsubmit={(e) => {
        e.preventDefault();
        create();
      }}
    >
      <input bind:value={name} placeholder={$_('apiaries.name_ph')} />
      <input bind:value={address} placeholder={$_('apiaries.address')} />
      <input bind:value={note} placeholder={$_('apiaries.note')} />
      <LocationPicker bind:lat bind:lng bind:address />
      {#if saveError}
        <p class="error" role="alert">{$_('common.save_failed', { values: { msg: saveError } })}</p>
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
            <span class="chev">›</span>
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
    color: var(--ink-soft);
  }
  .empty {
    text-align: center;
    padding: 40px 0;
  }
  .mapwrap {
    margin-bottom: 18px;
  }
  .primary {
    background: var(--honey);
    color: #fff;
    border: none;
    border-radius: 11px;
    padding: 10px 16px;
    font-weight: 700;
    font-family: inherit;
    cursor: pointer;
    box-shadow: 0 4px 12px rgba(199, 127, 34, 0.28);
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: default;
    box-shadow: none;
  }
  .ghost {
    background: transparent;
    border: 1px solid var(--line);
    border-radius: 11px;
    padding: 10px 16px;
    font-weight: 600;
    font-family: inherit;
    cursor: pointer;
    color: var(--ink);
  }
  .card {
    background: var(--cream2);
    border: 1px solid var(--line);
    border-radius: 16px;
  }
  .form {
    padding: 16px;
    display: grid;
    gap: 10px;
    margin-bottom: 18px;
  }
  .form input {
    font: inherit;
    padding: 11px 13px;
    border: 1px solid var(--line);
    border-radius: 10px;
    background: #fff;
    color: var(--ink);
  }
  .form .actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
    margin-top: 4px;
  }
  .form .error {
    color: #a03d2a;
    background: rgba(160, 61, 42, 0.08);
    border: 1px solid rgba(160, 61, 42, 0.25);
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
    text-decoration: none;
    color: var(--ink);
  }
  .lead .pin {
    font-size: 1.4rem;
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .body strong {
    display: block;
    font-weight: 700;
  }
  .body small {
    color: var(--ink-soft);
    font-size: 0.85rem;
  }
  .badge {
    background: rgba(92, 107, 74, 0.15);
    color: #41502f;
    font-weight: 700;
    font-size: 0.78rem;
    padding: 5px 11px;
    border-radius: 999px;
    white-space: nowrap;
  }
  .chev {
    color: var(--ink-soft);
    font-size: 1.4rem;
  }
</style>
