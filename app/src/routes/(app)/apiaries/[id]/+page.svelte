<script lang="ts">
  import { mount, onDestroy, unmount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { _ } from 'svelte-i18n';
  import { MapPin, Pencil, Trash2, Tag } from '@lucide/svelte';
  import { apiaries, hives, ApiaryHasHivesError } from '#lib/local/repo.js';
  import { createHive } from '#lib/local/history.js';
  import type { ApiaryRow } from '../../rows.js';
  import { dataVersion } from '#lib/local/live.js';
  import { qrSvg, hiveUrl, shortCode } from '#lib/qr.js';
  import type { HiveOverview } from '#lib/hivelist.js';
  import HiveCard from '#lib/components/HiveCard.svelte';
  import LocationPicker from '#lib/components/LocationPicker.svelte';
  import QrSheet from '#lib/components/QrSheet.svelte';
  import Skeleton from '#lib/components/Skeleton.svelte';

  const id = $derived(page.params.id ?? '');
  let apiary = $state<ApiaryRow | null>(null);
  let hiveRows = $state<HiveOverview[]>([]);
  let loaded = $state(false);

  let editing = $state(false);
  let eName = $state('');
  let eAddress = $state('');
  let eNote = $state('');
  let eLat = $state(0);
  let eLng = $state(0);
  let locating = $state(false);
  let locationFailed = $state(false);

  let addingHive = $state(false);
  let hiveName = $state('');
  // Set when a delete was refused because hives still stand here.
  let deleteBlocked = $state(false);
  // The label sheet while it is being printed.
  let sheet: ReturnType<typeof mount> | null = null;

  // One query for the apiary's hives, also when the apiary itself is deleted:
  // its hives are then still listed so they can be moved elsewhere.
  async function load() {
    apiary = (await apiaries.get(id)) ?? null;
    hiveRows = await hives.overview(id);
    if (hiveRows.length === 0) deleteBlocked = false;
    loaded = true;
  }

  function startEdit() {
    if (!apiary) return;
    eName = apiary.name;
    eAddress = apiary.address ?? '';
    eNote = apiary.note ?? '';
    eLat = apiary.lat ?? 0;
    eLng = apiary.lng ?? 0;
    locationFailed = false;
    editing = true;
  }
  function useMyLocation() {
    locationFailed = false;
    if (!navigator.geolocation) {
      locationFailed = true;
      return;
    }
    locating = true;
    navigator.geolocation.getCurrentPosition(
      (p) => {
        eLat = +p.coords.latitude.toFixed(6);
        eLng = +p.coords.longitude.toFixed(6);
        locating = false;
      },
      () => {
        // Permission denied, no fix, or timed out: say so instead of doing nothing.
        locating = false;
        locationFailed = true;
      },
      { enableHighAccuracy: true, timeout: 10_000 }
    );
  }
  async function saveEdit() {
    if (!eName.trim()) return;
    await apiaries.update(id, {
      name: eName.trim(),
      address: eAddress.trim(),
      note: eNote.trim(),
      lat: eLat || 0,
      lng: eLng || 0
    });
    editing = false;
    await load();
  }
  async function del() {
    if (!apiary) return;
    if (hiveRows.length > 0) {
      deleteBlocked = true;
      return;
    }
    if (!confirm($_('apiaries.confirm_delete', { values: { name: apiary.name } }))) return;
    try {
      await apiaries.remove(id);
    } catch (e) {
      // A hive arrived through sync between the check above and the delete.
      if (!(e instanceof ApiaryHasHivesError)) throw e;
      await load();
      deleteBlocked = true;
      return;
    }
    await goto('/apiaries', { replaceState: true });
  }
  async function addHive() {
    if (!hiveName.trim()) return;
    await createHive({ name: hiveName.trim(), apiary_id: id });
    hiveName = '';
    addingHive = false;
    await load();
  }

  // Printable sheet of QR labels for every hive at this apiary. It is mounted
  // on <body> for the duration of the print and removed afterwards.
  async function printQrSheet() {
    if (!apiary || !hiveRows.length || sheet) return;
    const title = apiary.name;
    const labels = await Promise.all(
      hiveRows.map(async (h) => ({
        id: h.id,
        name: h.name,
        code: shortCode(h.id),
        svg: await qrSvg(hiveUrl(h.id), 220)
      }))
    );
    closeSheet();
    sheet = mount(QrSheet, {
      target: document.body,
      props: { title, labels, onclose: closeSheet }
    });
  }
  function closeSheet() {
    if (sheet) void unmount(sheet);
    sheet = null;
  }
  onDestroy(closeSheet);

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<svelte:head><title>{apiary?.name ?? $_('apiaries.title')}</title></svelte:head>

{#snippet hiveList()}
  <ul class="hives">
    {#each hiveRows as h (h.id)}
      <li><HiveCard hive={h} /></li>
    {/each}
  </ul>
{/snippet}

<div class="page">
  <a class="back" href="/apiaries"><span aria-hidden="true">‹</span> {$_('apiaries.title')}</a>

  {#if !loaded}
    <Skeleton n={3} height={80} />
  {:else if !apiary && hiveRows.length > 0}
    <div class="notice" role="status">
      <h1>{$_('apiaries.deleted_title')}</h1>
      <p>{$_('apiaries.deleted_text', { values: { n: hiveRows.length } })}</p>
    </div>
    {@render hiveList()}
  {:else if !apiary}
    <p class="muted empty">{$_('apiaries.not_found')}</p>
  {:else}
    <header>
      <div class="title">
        <span class="pin"><MapPin size={24} aria-hidden="true" /></span>
        <div>
          <h1>{apiary.name}</h1>
          {#if apiary.address}<p class="muted">{apiary.address}</p>{/if}
        </div>
      </div>
      <div class="acts">
        <button
          class="icon"
          onclick={startEdit}
          title={$_('apiaries.edit')}
          aria-label={$_('apiaries.edit')}><Pencil size={18} aria-hidden="true" /></button
        >
        <button
          class="icon danger"
          onclick={del}
          title={$_('apiaries.delete')}
          aria-label={$_('apiaries.delete')}><Trash2 size={18} aria-hidden="true" /></button
        >
      </div>
    </header>

    {#if deleteBlocked}
      <p class="notice" role="alert">
        {$_('apiaries.delete_blocked', { values: { name: apiary.name, n: hiveRows.length } })}
      </p>
    {/if}

    {#if editing}
      <form
        class="card form"
        onsubmit={(e) => {
          e.preventDefault();
          saveEdit();
        }}
      >
        <label class="field">
          <span>{$_('apiaries.name')}</span>
          <input bind:value={eName} />
        </label>
        <label class="field">
          <span>{$_('apiaries.address')}</span>
          <input bind:value={eAddress} autocomplete="street-address" />
        </label>
        <label class="field">
          <span>{$_('apiaries.note')}</span>
          <textarea bind:value={eNote} rows="2"></textarea>
        </label>
        <div class="coords">
          <label class="field"
            ><span>{$_('apiaries.lat')}</span>
            <input type="number" step="0.000001" bind:value={eLat} placeholder="0" /></label
          >
          <label class="field"
            ><span>{$_('apiaries.lng')}</span>
            <input type="number" step="0.000001" bind:value={eLng} placeholder="0" /></label
          >
          <button type="button" class="ghost loc" onclick={useMyLocation} disabled={locating}>
            <MapPin size={16} aria-hidden="true" />
            {locating ? $_('apiaries.locating') : $_('apiaries.use_location')}
          </button>
        </div>
        {#if locationFailed}
          <p class="error" role="alert">{$_('apiaries.location_unavailable')}</p>
        {/if}
        <LocationPicker bind:lat={eLat} bind:lng={eLng} bind:address={eAddress} />
        <div class="actions">
          <button type="button" class="ghost" onclick={() => (editing = false)}
            >{$_('common.cancel')}</button
          >

          <button type="submit" class="primary" disabled={!eName.trim()}>{$_('common.save')}</button
          >
        </div>
      </form>
    {:else if apiary.note}
      <p class="note card">{apiary.note}</p>
    {/if}

    <div class="sec-head">
      <h2>{$_('hives.title')}</h2>
      <div class="hactions">
        {#if hiveRows.length}
          <button class="ghost" onclick={printQrSheet}
            ><Tag size={16} aria-hidden="true" /> {$_('apiaries.qr_sheet')}</button
          >
        {/if}

        <button
          class="primary"
          aria-expanded={addingHive}
          onclick={() => (addingHive = !addingHive)}>+ {$_('apiaries.add_hive')}</button
        >
      </div>
    </div>

    {#if addingHive}
      <form
        class="card form"
        onsubmit={(e) => {
          e.preventDefault();
          addHive();
        }}
      >
        <label class="field">
          <span>{$_('hives.name')}</span>
          <input bind:value={hiveName} placeholder={$_('hives.name_ph')} />
        </label>
        <div class="actions">
          <button type="button" class="ghost" onclick={() => (addingHive = false)}
            >{$_('common.cancel')}</button
          >

          <button type="submit" class="primary" disabled={!hiveName.trim()}
            >{$_('common.save')}</button
          >
        </div>
      </form>
    {/if}

    {#if hiveRows.length === 0}
      <p class="muted empty">{$_('apiaries.no_hives')}</p>
    {:else}
      {@render hiveList()}
    {/if}
  {/if}
</div>

<style>
  .page {
    max-width: 760px;
    margin: 0 auto;
    padding: 22px 18px 96px;
  }
  .back {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    min-height: 44px;
    color: var(--text-soft);
    text-decoration: none;
    font-weight: 600;
    font-size: 0.9rem;
  }
  .muted {
    color: var(--text-soft);
  }
  .empty {
    text-align: center;
    padding: 36px 0;
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
    margin: 6px 0 18px;
  }
  .title {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    min-width: 0;
  }
  .title .pin {
    display: flex;
    align-items: center;
    height: 1.9rem;
    color: var(--accent-text);
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.9rem;
    overflow-wrap: anywhere;
  }
  h2 {
    font-family: 'Fraunces', serif;
    font-size: 1.3rem;
  }
  .acts {
    display: flex;
    gap: 8px;
    flex-shrink: 0;
  }
  .icon {
    display: inline-grid;
    place-items: center;
    width: 44px;
    height: 44px;
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    background: var(--surface);
    cursor: pointer;
    color: var(--text);
  }
  .icon.danger:hover {
    border-color: var(--danger);
    color: var(--danger);
  }

  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 16px;
  }
  .note {
    padding: 14px 16px;
    margin-bottom: 18px;
    color: var(--text-soft);
    white-space: pre-wrap;
  }
  .notice {
    background: var(--warn-bg);
    color: var(--warn-text);
    border-radius: 12px;
    padding: 12px 16px;
    margin-bottom: 18px;
  }
  .notice h1 {
    font-size: 1.3rem;
    margin-bottom: 4px;
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
    min-width: 0;
  }
  .field span {
    font-size: 0.82rem;
    font-weight: 600;
    color: var(--text-soft);
  }
  .field input,
  .field textarea {
    font: inherit;
    width: 100%;
    min-height: 44px;
    padding: 10px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    background: var(--surface);
    color: var(--text);
    resize: vertical;
  }
  .form .actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
  }
  .form .error {
    color: var(--danger);
    background: color-mix(in srgb, var(--danger) 8%, transparent);
    border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
    border-radius: 10px;
    padding: 9px 12px;
    font-size: 0.85rem;
  }
  .coords {
    display: flex;
    gap: 10px;
    flex-wrap: wrap;
    align-items: flex-end;
  }
  .coords .field {
    flex: 1;
    min-width: 120px;
  }
  .coords .loc {
    white-space: nowrap;
  }
  .primary,
  .ghost {
    display: inline-flex;
    align-items: center;
    gap: 6px;
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
  .primary:disabled,
  .ghost:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .ghost {
    background: transparent;
    border: 1px solid var(--border-strong);
    color: var(--text);
  }

  .sec-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin: 6px 0 14px;
    gap: 10px;
  }
  .hactions {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    justify-content: flex-end;
  }
  .hives {
    list-style: none;
    display: grid;
    gap: 12px;
  }
</style>
