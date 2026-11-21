<script lang="ts">
  // Head of the hive page: picture, name, the two everyday actions and a
  // labelled menu for the rest (Delete last, set apart as dangerous).
  import { focusOnOpen } from '#lib/focus.js';
  import { goto } from '$app/navigation';
  import { _ } from 'svelte-i18n';
  import {
    ArrowLeftRight,
    ChartLine,
    ClipboardPlus,
    Ellipsis,
    Pencil,
    QrCode,
    Table,
    Trash2
  } from '@lucide/svelte';
  import { hives } from '#lib/local/repo.js';
  import { moveHive, updateHive } from '#lib/local/history.js';
  import type { ApiaryRow, HiveRow } from '../../../routes/(app)/rows.js';
  import QrLabel from '#lib/components/QrLabel.svelte';
  import { createSaver } from './saver.js';
  import { changedFrom, dotted } from './forms.js';
  import FormFooter from './FormFooter.svelte';
  import Photos from './Photos.svelte';
  import './hive.css';

  let {
    hive,
    apiaries,
    reload,
    onrecord
  }: {
    hive: HiveRow;
    apiaries: ApiaryRow[];
    reload: () => Promise<void> | void;
    /** "Record visit" was chosen. */
    onrecord: () => void;
  } = $props();

  const HIVE_TYPES = [1, 2, 3, 4, 5, 6, 99];
  const HIVE_STATUSES = [1, 2, 3, 4, 5];

  let panel = $state<'' | 'move' | 'edit' | 'qr'>('');
  let menuOpen = $state(false);
  let menu = $state<HTMLElement>();
  let moveTarget = $state('');
  let name = $state('');
  let type = $state(0);
  let status = $state(1);
  // What the edit panel showed when it was opened; Save writes only what differs.
  let opened = { name: '', type: 0, status: 1 };

  const saver = createSaver();
  const { busy, error } = saver;
  const others = $derived(apiaries.filter((a) => a.id !== hive.apiary_id));

  function toggle(next: 'move' | 'edit' | 'qr') {
    menuOpen = false;
    if (panel === next) {
      panel = '';
      return;
    }
    if (next === 'move') moveTarget = others[0]?.id ?? '';
    if (next === 'edit') {
      name = hive.name;
      type = hive.type ?? 0;
      status = hive.status ?? 1;
      opened = { name, type, status };
    }
    panel = next;
  }

  async function doMove() {
    if (!moveTarget || moveTarget === hive.apiary_id) {
      panel = '';
      return;
    }
    const ok = await saver.run(async () => {
      await moveHive(hive.id, hive.apiary_id, moveTarget);
      await reload();
    });
    if (ok) panel = '';
  }

  async function saveHive() {
    if (!name.trim()) return;
    const ok = await saver.run(async () => {
      const changes = changedFrom(opened, { name: name.trim(), type, status });
      await updateHive(hive.id, hive.apiary_id, changes);
      await reload();
    });
    if (ok) panel = '';
  }

  let deleting = false;
  async function delHive() {
    menuOpen = false;
    if (deleting || !confirm($_('hives.confirm_delete', { values: { name: hive.name } }))) return;
    deleting = true;
    const back = hive.apiary_id;
    try {
      await hives.remove(hive.id, hive.apiary_id);
      await goto(back ? `/apiaries/${back}` : '/hives', { replaceState: true });
    } finally {
      deleting = false;
    }
  }

  // The menu closes on Escape and when focus or a tap lands outside it.
  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') menuOpen = false;
  }
  function onOutside(e: Event) {
    if (menuOpen && menu && !menu.contains(e.target as Node)) menuOpen = false;
  }
</script>

<svelte:window onkeydown={onKey} onclick={onOutside} onfocusin={onOutside} />

<header>
  <div class="title">
    <Photos kind="hive" {hive} onchange={reload} />
    <div class="names">
      <h1>{hive.name}</h1>
      <p class="hv-muted">
        {dotted(
          $_('hivestatus.' + (hive.status ?? 0)),
          hive.type ? $_('hivetype.' + hive.type) : ''
        )}
      </p>
    </div>
  </div>

  <div class="acts">
    <button type="button" class="hv-btn" onclick={onrecord}>
      <ClipboardPlus size={18} aria-hidden="true" />
      {$_('insp.record')}
    </button>
    <button
      type="button"
      class="hv-btn ghost"
      aria-expanded={panel === 'move'}
      onclick={() => toggle('move')}
    >
      <ArrowLeftRight size={18} aria-hidden="true" />
      {$_('hives.move')}
    </button>
    <div class="more" bind:this={menu}>
      <button
        type="button"
        class="hv-btn ghost"
        aria-expanded={menuOpen}
        aria-controls="hive-more"
        onclick={() => (menuOpen = !menuOpen)}
      >
        <Ellipsis size={18} aria-hidden="true" />
        {$_('hives.more')}
      </button>
      {#if menuOpen}
        <ul id="hive-more" class="menu hv-card">
          <li>
            <a href={`/hives/${hive.id}/stockkarte`}>
              <Table size={18} aria-hidden="true" />
              {$_('insp.stockkarte')}
            </a>
          </li>
          <li>
            <a href={`/hives/${hive.id}/stats`}>
              <ChartLine size={18} aria-hidden="true" />
              {$_('stats.title')}
            </a>
          </li>
          <li>
            <button type="button" onclick={() => toggle('edit')}>
              <Pencil size={18} aria-hidden="true" />
              {$_('hives.edit')}
            </button>
          </li>
          <li>
            <button type="button" onclick={() => toggle('qr')}>
              <QrCode size={18} aria-hidden="true" />
              {$_('hives.qr')}
            </button>
          </li>
          <li class="danger">
            <button type="button" onclick={delHive}>
              <Trash2 size={18} aria-hidden="true" />
              {$_('hives.delete')}
            </button>
          </li>
        </ul>
      {/if}
    </div>
  </div>
</header>

{#if panel === 'move'}
  <form
    class="hv-card hv-form"
    use:focusOnOpen
    onsubmit={(e) => {
      e.preventDefault();
      doMove();
    }}
  >
    {#if others.length}
      <label class="hv-field">
        <span>{$_('hives.move_to')}</span>
        <select bind:value={moveTarget}>
          {#each others as a}
            <option value={a.id}>{a.name}</option>
          {/each}
        </select>
      </label>
    {:else}
      <p class="hv-muted">{$_('hives.move_none')}</p>
    {/if}
    <FormFooter busy={$busy} error={$error} invalid={!moveTarget} oncancel={() => (panel = '')} />
  </form>
{:else if panel === 'edit'}
  <form
    class="hv-card hv-form"
    use:focusOnOpen
    onsubmit={(e) => {
      e.preventDefault();
      saveHive();
    }}
  >
    <label class="hv-field">
      <span>{$_('hives.name')}</span>
      <input bind:value={name} placeholder={$_('hives.name_ph')} required />
    </label>
    <div class="hv-grid2">
      <label class="hv-field">
        <span>{$_('hives.type')}</span>
        <select bind:value={type}>
          {#if !HIVE_TYPES.includes(type)}<option value={type}>{$_('hivetype.0')}</option>{/if}
          {#each HIVE_TYPES as t}<option value={t}>{$_('hivetype.' + t)}</option>{/each}
        </select>
      </label>
      <label class="hv-field">
        <span>{$_('hives.status')}</span>
        <select bind:value={status}>
          {#each HIVE_STATUSES as s}<option value={s}>{$_('hivestatus.' + s)}</option>{/each}
        </select>
      </label>
    </div>
    <FormFooter busy={$busy} error={$error} invalid={!name.trim()} oncancel={() => (panel = '')} />
  </form>
{:else if panel === 'qr'}
  <div class="qrwrap hv-card">
    <QrLabel hiveId={hive.id} name={hive.name} />
  </div>
{/if}

<style>
  /* Wraps on narrow phones: the actions drop below the name. */
  header {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: flex-start;
    gap: 14px;
    margin: 6px 0 18px;
  }
  .title {
    display: flex;
    gap: 14px;
    align-items: flex-start;
    min-width: 0;
    flex: 1 1 220px;
  }
  .names {
    min-width: 0;
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .more {
    position: relative;
  }
  .menu {
    position: absolute;
    right: 0;
    top: calc(100% + 6px);
    z-index: 20;
    min-width: 230px;
    max-width: calc(100vw - 36px);
    list-style: none;
    padding: 6px;
    display: grid;
    gap: 2px;
  }
  .menu a,
  .menu button {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    min-height: 46px;
    padding: 0 12px;
    border: none;
    border-radius: 10px;
    background: transparent;
    color: var(--text);
    font: inherit;
    font-weight: 600;
    text-align: left;
    text-decoration: none;
    cursor: pointer;
  }
  .menu a:hover,
  .menu button:hover {
    background: var(--bg);
  }
  .menu a:focus-visible,
  .menu button:focus-visible {
    outline: 2px solid var(--accent-text);
    outline-offset: -2px;
  }
  .menu .danger {
    margin-top: 4px;
    padding-top: 4px;
    border-top: 1px solid var(--border);
  }
  .menu .danger button {
    color: var(--danger);
  }
  .qrwrap {
    padding: 16px;
    display: flex;
    justify-content: center;
    margin-bottom: 12px;
  }
  /* On phones the menu would open off-screen to the left of a wrapped row. */
  @media (max-width: 480px) {
    .more {
      position: static;
    }
    .acts {
      position: relative;
      width: 100%;
    }
    .menu {
      left: 0;
      right: auto;
    }
  }
</style>
