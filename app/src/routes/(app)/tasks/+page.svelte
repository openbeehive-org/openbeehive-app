<script lang="ts">
  import { _ } from 'svelte-i18n';
  import { Check, Pencil } from '@lucide/svelte';
  import { tasks } from '#lib/local/repo.js';
  import type { TaskRow } from '../rows.js';
  import { dataVersion, initialSyncPending } from '#lib/local/live.js';
  import Skeleton from '#lib/components/Skeleton.svelte';
  import Syncing from '#lib/components/Syncing.svelte';

  let rows = $state<TaskRow[]>([]);
  let loaded = $state(false);
  let title = $state('');
  let due = $state('');

  // inline edit
  let editId = $state<string | null>(null);
  let eTitle = $state('');
  let eDue = $state('');

  async function load() {
    rows = await tasks.list();
    loaded = true;
  }

  async function add() {
    if (!title.trim()) return;
    await tasks.create({ title: title.trim(), ...(due ? { due_at: due } : {}) });
    title = '';
    due = '';
    await load();
  }

  function startEdit(t: TaskRow) {
    editId = t.id;
    eTitle = t.title;
    eDue = t.due_at ? t.due_at.slice(0, 10) : '';
  }
  async function saveEdit(t: TaskRow) {
    if (!eTitle.trim()) return;
    await tasks.update(t, { title: eTitle.trim(), due_at: eDue || '' });
    editId = null;
    await load();
  }

  async function toggle(t: TaskRow) {
    await tasks.toggle(t);
    await load();
  }

  async function del(t: TaskRow) {
    await tasks.remove(t);
    await load();
  }

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<svelte:head><title>{$_('tasks.title')}</title></svelte:head>

<div class="page">
  <header><h1>{$_('tasks.title')}</h1></header>

  <form
    class="adder"
    onsubmit={(e) => {
      e.preventDefault();
      add();
    }}
  >
    <input bind:value={title} placeholder={$_('tasks.title_ph')} />
    <input type="date" bind:value={due} aria-label={$_('tasks.due')} />
    <button class="primary" type="submit" disabled={!title.trim()}>{$_('tasks.add')}</button>
  </form>

  {#if !loaded}
    <Skeleton n={3} height={54} />
  {:else if rows.length === 0 && $initialSyncPending}
    <Syncing />
  {:else if rows.length === 0}
    <p class="muted empty">{$_('tasks.empty')}</p>
  {:else}
    <ul class="list">
      {#each rows as t (t.id)}
        <li class="card" class:done={t.done}>
          {#if editId === t.id}
            <form
              class="edit"
              onsubmit={(e) => {
                e.preventDefault();
                saveEdit(t);
              }}
            >
              <input bind:value={eTitle} placeholder={$_('tasks.title_ph')} />
              <input type="date" bind:value={eDue} aria-label={$_('tasks.due')} />
              <button class="primary sm" type="submit" disabled={!eTitle.trim()}
                >{$_('common.save')}</button
              >
              <button class="ghost sm" type="button" onclick={() => (editId = null)}
                >{$_('common.cancel')}</button
              >
            </form>
          {:else}
            <button class="check" class:on={t.done} onclick={() => toggle(t)} aria-label="toggle">
              {#if t.done}<Check size={16} aria-hidden="true" />{/if}
            </button>
            <div class="body">
              <strong>{t.title}</strong>
              {#if t.due_at}<small
                  >{$_('tasks.due')}: {new Date(t.due_at).toLocaleDateString()}</small
                >{/if}
            </div>
            <button class="iconbtn" onclick={() => startEdit(t)} aria-label={$_('tasks.edit')}
              ><Pencil size={16} aria-hidden="true" /></button
            >
            <button class="del" onclick={() => del(t)} aria-label={$_('common.delete')}>×</button>
          {/if}
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
  .adder {
    display: flex;
    gap: 8px;
    margin-bottom: 20px;
  }
  .adder input:not([type='date']) {
    flex: 1;
  }
  .adder input {
    font: inherit;
    padding: 11px 13px;
    border: 1px solid var(--line);
    border-radius: 10px;
    background: #fff;
    color: var(--ink);
    min-width: 0;
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
    white-space: nowrap;
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .list {
    list-style: none;
    display: grid;
    gap: 10px;
  }
  .card {
    background: var(--cream2);
    border: 1px solid var(--line);
    border-radius: 14px;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 14px;
  }
  .card.done .body strong {
    text-decoration: line-through;
    color: var(--ink-soft);
  }
  .check {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    border-radius: 8px;
    border: 2px solid var(--honey);
    background: #fff;
    cursor: pointer;
    color: #fff;
    font-weight: 800;
    flex-shrink: 0;
    line-height: 1;
  }
  .check.on {
    background: var(--honey);
  }
  .body {
    flex: 1;
  }
  .body strong {
    display: block;
    font-weight: 600;
  }
  .body small {
    color: var(--ink-soft);
    font-size: 0.82rem;
  }
  .del {
    background: none;
    border: none;
    font-size: 1.5rem;
    color: var(--ink-soft);
    cursor: pointer;
    line-height: 1;
    padding: 0 4px;
  }
  .iconbtn {
    display: inline-flex;
    background: none;
    border: none;
    cursor: pointer;
    color: var(--ink-soft);
    font-size: 1rem;
    padding: 4px 6px;
  }
  .iconbtn:hover {
    color: var(--honey-d);
  }
  .edit {
    display: flex;
    gap: 8px;
    flex: 1;
    flex-wrap: wrap;
    align-items: center;
  }
  .edit input:first-child {
    flex: 1;
    min-width: 120px;
  }
  .edit input {
    font: inherit;
    padding: 9px 11px;
    border: 1px solid var(--line);
    border-radius: 9px;
    background: #fff;
    color: var(--ink);
    min-width: 0;
  }
  .sm {
    padding: 8px 12px;
    font-size: 0.85rem;
    border-radius: 9px;
  }
  .ghost {
    background: transparent;
    border: 1px solid var(--line);
    font-family: inherit;
    font-weight: 600;
    color: var(--ink);
    cursor: pointer;
  }
</style>
