<script lang="ts">
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { _ } from 'svelte-i18n';
  import { MapPin, Pencil, Trash2, Tag } from '@lucide/svelte';
  import {
    apiaries,
    hives,
    queens,
    inspections,
    MARKING_COLORS,
    MARKING_NAMES
  } from '#lib/local/repo.js';
  import { createHive } from '#lib/local/history.js';
  import type { ApiaryRow, HiveRow, QueenRow } from '../../rows.js';
  import { dataVersion } from '#lib/local/live.js';
  import { qrSvg, hiveUrl, shortCode } from '#lib/qr.js';
  import LocationPicker from '#lib/components/LocationPicker.svelte';

  const id = page.params.id ?? '';
  type HiveCard = HiveRow & { queen: QueenRow | undefined; lastInspection: string | undefined };
  let apiary = $state<ApiaryRow | null>(null);
  let hiveRows = $state<HiveCard[]>([]);
  let loaded = $state(false);

  let editing = $state(false);
  let eName = $state('');
  let eAddress = $state('');
  let eNote = $state('');
  let eLat = $state(0);
  let eLng = $state(0);
  let locating = $state(false);

  let addingHive = $state(false);
  let hiveName = $state('');

  function daysAgo(iso?: string) {
    if (!iso) return -1;
    return Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000);
  }

  async function load() {
    apiary = await apiaries.get(id);
    if (apiary) {
      const list = (await hives.list(id)) as HiveRow[];
      hiveRows = await Promise.all(
        list.map(async (h) => {
          const q = await queens.current(h.id);
          const insp = await inspections.listByHive(h.id);
          return { ...h, queen: q, lastInspection: insp[0]?.date };
        })
      );
    }
    loaded = true;
  }

  function startEdit() {
    if (!apiary) return;
    eName = apiary.name;
    eAddress = apiary.address ?? '';
    eNote = apiary.note ?? '';
    eLat = apiary.lat ?? 0;
    eLng = apiary.lng ?? 0;
    editing = true;
  }
  function useMyLocation() {
    locating = true;
    navigator.geolocation.getCurrentPosition(
      (p) => {
        eLat = +p.coords.latitude.toFixed(6);
        eLng = +p.coords.longitude.toFixed(6);
        locating = false;
      },
      () => {
        locating = false;
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
    if (!confirm($_('common.confirm_delete'))) return;
    await apiaries.remove(id);
    await goto('/apiaries', { replaceState: true });
  }
  async function addHive() {
    if (!hiveName.trim()) return;
    await createHive({ name: hiveName.trim(), apiary_id: id });
    hiveName = '';
    addingHive = false;
    await load();
  }

  // Printable A4 sheet of QR labels for every hive at this apiary.
  async function printQrSheet() {
    if (!apiary || !hiveRows.length) return;
    const cells = await Promise.all(
      hiveRows.map(async (h) => {
        const svg = await qrSvg(hiveUrl(h.id), 220);
        return `<div class="cell"><div class="qr">${svg}</div>
        <div class="n">${h.name}</div><div class="c">${shortCode(h.id)}</div></div>`;
      })
    );
    const w = window.open('', '_blank', 'width=820,height=1000');
    if (!w) return;
    w.document.write(`<!doctype html><html><head><title>${apiary.name} - QR</title><style>
      @page { size: A4; margin: 12mm; }
      body { font-family: system-ui, sans-serif; margin: 0; }
      h1 { font-size: 16px; margin: 0 0 10px; }
      .grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; }
      .cell { border: 1px solid #ddd; border-radius: 10px; padding: 10px; text-align: center;
        break-inside: avoid; }
      .qr svg { width: 100%; height: auto; max-width: 150px; }
      .n { font-weight: 700; font-size: 13px; margin-top: 6px; }
      .c { color: #777; letter-spacing: .15em; font-size: 11px; }
    </style></head><body><h1>${apiary.name}</h1><div class="grid">${cells.join('')}</div></body></html>`);
    w.document.close();
    w.focus();
    setTimeout(() => w.print(), 200);
  }

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<div class="page">
  <a class="back" href="/apiaries">‹ {$_('apiaries.title')}</a>

  {#if !loaded}
    <p class="muted">...</p>
  {:else if !apiary}
    <p class="muted">{$_('apiaries.not_found')}</p>
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
        <button class="icon" onclick={startEdit} title={$_('apiaries.edit')}
          ><Pencil size={18} aria-hidden="true" /></button
        >
        <button class="icon danger" onclick={del} title={$_('common.delete')}
          ><Trash2 size={18} aria-hidden="true" /></button
        >
      </div>
    </header>

    {#if editing}
      <form
        class="card form"
        onsubmit={(e) => {
          e.preventDefault();
          saveEdit();
        }}
      >
        <input bind:value={eName} placeholder={$_('apiaries.name')} />
        <input bind:value={eAddress} placeholder={$_('apiaries.address')} />
        <textarea bind:value={eNote} placeholder={$_('apiaries.note')} rows="2"></textarea>
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
            {locating ? '...' : $_('apiaries.use_location')}
          </button>
        </div>
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
          <button class="ghost sm" onclick={printQrSheet}
            ><Tag size={16} aria-hidden="true" /> {$_('apiaries.qr_sheet')}</button
          >
        {/if}

        <button class="primary sm" onclick={() => (addingHive = !addingHive)}
          >+ {$_('apiaries.add_hive')}</button
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
        <input bind:value={hiveName} placeholder={$_('hives.name_ph')} />
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
      <ul class="hives">
        {#each hiveRows as h}
          <li class="card">
            <a href={`/hives/${h.id}`}>
              <span class="thumb">
                {#if h.photo}<img src={h.photo} alt={h.name} />{:else}<img
                    class="ph"
                    src="/favicon.svg"
                    alt=""
                  />{/if}
              </span>
              <div class="body">
                <strong>{h.name}</strong>
                <div class="meta">
                  <span class="badge status-{h.status}">{$_('hivestatus.' + (h.status ?? 0))}</span>
                  {#if h.queen}
                    <span class="queen">
                      <span
                        class="dot"
                        style={`background:${MARKING_COLORS[h.queen.marking] ?? '#ccc'}`}
                        title={MARKING_NAMES[h.queen.marking] ?? ''}
                      ></span>
                      {$_('queens.title')}
                      {h.queen.year}
                    </span>
                  {/if}
                  <span class="insp">
                    {$_('hives.last_inspection')}:
                    {daysAgo(h.lastInspection) < 0
                      ? $_('hives.never')
                      : $_('dashboard.days_ago', { values: { days: daysAgo(h.lastInspection) } })}
                  </span>
                </div>
              </div>
              <span class="chev">›</span>
            </a>
          </li>
        {/each}
      </ul>
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
    color: var(--ink-soft);
    text-decoration: none;
    font-weight: 600;
    font-size: 0.9rem;
  }
  .muted {
    color: var(--ink-soft);
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
    margin: 14px 0 18px;
  }
  .title {
    display: flex;
    gap: 12px;
    align-items: flex-start;
  }
  .title .pin {
    display: flex;
    align-items: center;
    height: 1.9rem;
    color: var(--honey-d);
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.9rem;
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
    width: 38px;
    height: 38px;
    border: 1px solid var(--line);
    border-radius: 10px;
    background: var(--cream2);
    cursor: pointer;
    font-size: 1rem;
    color: var(--ink);
  }
  .icon.danger:hover {
    border-color: #d9a59b;
    color: #b5402f;
  }

  .card {
    background: var(--cream2);
    border: 1px solid var(--line);
    border-radius: 16px;
  }
  .note {
    padding: 14px 16px;
    margin-bottom: 18px;
    color: var(--ink-soft);
    white-space: pre-wrap;
  }
  .form {
    padding: 16px;
    display: grid;
    gap: 10px;
    margin-bottom: 18px;
  }
  .form input,
  .form textarea {
    font: inherit;
    padding: 11px 13px;
    border: 1px solid var(--line);
    border-radius: 10px;
    background: #fff;
    color: var(--ink);
    resize: vertical;
  }
  .form .actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
  }
  .coords {
    display: flex;
    gap: 10px;
    flex-wrap: wrap;
    align-items: flex-end;
  }
  .coords .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 0.8rem;
    color: var(--ink-soft);
    font-weight: 600;
    flex: 1;
    min-width: 120px;
  }
  .coords .field input {
    width: 100%;
  }
  .coords .loc {
    white-space: nowrap;
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
  .primary.sm {
    padding: 8px 13px;
    font-size: 0.85rem;
    box-shadow: none;
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
  .ghost.sm {
    padding: 8px 12px;
    font-size: 0.85rem;
    border-radius: 10px;
  }
  .hives {
    list-style: none;
    display: grid;
    gap: 12px;
  }
  .hives li a {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 12px 14px;
    text-decoration: none;
    color: var(--ink);
  }
  .thumb {
    width: 52px;
    height: 52px;
    border-radius: 12px;
    overflow: hidden;
    flex-shrink: 0;
    background: linear-gradient(150deg, #fff5e3, #ffe9c6);
    display: grid;
    place-items: center;
    border: 1px solid var(--line);
  }
  .thumb img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .thumb .ph {
    width: 24px;
    height: 24px;
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .body strong {
    display: block;
    font-weight: 700;
    margin-bottom: 5px;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    font-size: 0.8rem;
    color: var(--ink-soft);
  }
  .badge {
    font-weight: 700;
    font-size: 0.72rem;
    padding: 3px 9px;
    border-radius: 999px;
    background: rgba(92, 107, 74, 0.15);
    color: #41502f;
  }
  .badge.status-3,
  .badge.status-4 {
    background: rgba(181, 64, 47, 0.13);
    color: #b5402f;
  }
  .queen {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-weight: 600;
  }
  .queen .dot {
    width: 11px;
    height: 11px;
    border-radius: 50%;
    border: 1px solid rgba(0, 0, 0, 0.18);
  }
  .chev {
    color: var(--ink-soft);
    font-size: 1.4rem;
  }
</style>
