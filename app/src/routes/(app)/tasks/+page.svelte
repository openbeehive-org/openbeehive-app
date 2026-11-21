<script lang="ts">
  import { onDestroy, onMount, tick } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { page } from '$app/state';
  import { Check, Pencil, Trash2 } from '@lucide/svelte';
  import { tasks } from '#lib/local/repo.js';
  import type { TaskRow } from '../rows.js';
  import { dataVersion, initialSyncPending } from '#lib/local/live.js';
  import { formatDate, fromDateInput, todayLocal } from '#lib/dates.js';
  import { dueDay, isOverdue, splitTasks } from '#lib/tasklist.js';
  import Skeleton from '#lib/components/Skeleton.svelte';
  import Syncing from '#lib/components/Syncing.svelte';
  import DemoNote from '#lib/components/DemoNote.svelte';
  import { demoSession } from '#lib/demo.js';

  let rows = $state<TaskRow[]>([]);
  let loaded = $state(false);
  let title = $state('');
  let due = $state('');
  let titleInput = $state<HTMLInputElement>();
  let showCompleted = $state(false);

  // inline edit
  let editId = $state<string | null>(null);
  let eTitle = $state('');
  let eDue = $state('');

  // A deleted task disappears at once but is only removed for good when the
  // undo window has passed (or the page is left), so "Undo" has nothing to
  // repair and nothing half-deleted ever syncs.
  const UNDO_MS = 7000;
  let pendingDelete = $state<TaskRow | null>(null);
  let undoTimer: ReturnType<typeof setTimeout> | undefined;

  // The task a dashboard link pointed at ("/tasks#task-<id>").
  let linkedId = $state('');
  let linkHandled = false;

  const today = todayLocal();
  const lists = $derived(splitTasks(rows.filter((t) => t.id !== pendingDelete?.id)));

  async function load() {
    rows = await tasks.list();
    loaded = true;
    await showLinkedTask();
  }

  async function showLinkedTask() {
    if (linkHandled) return;
    const hash = page.url.hash;
    if (!hash.startsWith('#task-')) {
      linkHandled = true;
      return;
    }
    const target = rows.find((t) => t.id === hash.slice('#task-'.length));
    if (!target) return; // may still arrive with the next sync batch
    linkHandled = true;
    if (target.done) showCompleted = true;
    linkedId = target.id;
    await tick();
    document.getElementById(`task-${target.id}`)?.scrollIntoView({ block: 'center' });
  }

  async function add() {
    if (!title.trim()) return;
    const dueAt = fromDateInput(due);
    await tasks.create({ title: title.trim(), ...(dueAt ? { due_at: dueAt } : {}) });
    title = '';
    due = '';
    await load();
  }

  function startEdit(t: TaskRow) {
    editId = t.id;
    eTitle = t.title;
    eDue = dueDay(t.due_at);
  }
  async function saveEdit(t: TaskRow) {
    if (!eTitle.trim()) return;
    // Only fields that changed are written, so an untouched date keeps its value.
    const fields: Record<string, unknown> = {};
    if (eTitle.trim() !== t.title) fields.title = eTitle.trim();
    if (eDue !== dueDay(t.due_at)) fields.due_at = fromDateInput(eDue);
    if (Object.keys(fields).length) await tasks.update(t, fields);
    editId = null;
    await load();
  }

  async function toggle(t: TaskRow) {
    await tasks.toggle(t);
    await load();
  }

  async function commitDelete() {
    const t = pendingDelete;
    clearTimeout(undoTimer);
    if (!t) return;
    pendingDelete = null;
    await tasks.remove(t);
  }
  async function del(t: TaskRow) {
    await commitDelete(); // an earlier delete becomes final
    pendingDelete = t;
    undoTimer = setTimeout(() => void commitDelete().then(load), UNDO_MS);
  }
  function undoDelete() {
    clearTimeout(undoTimer);
    pendingDelete = null;
  }

  onMount(() => {
    // "/tasks?new=1" (dashboard) puts the cursor into the new-task field.
    if (page.url.searchParams.has('new')) titleInput?.focus();
    const flush = () => void commitDelete();
    window.addEventListener('pagehide', flush);
    return () => window.removeEventListener('pagehide', flush);
  });
  onDestroy(() => void commitDelete());

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<svelte:head><title>{$_('tasks.title')}</title></svelte:head>

{#snippet item(t: TaskRow)}
  <li class="card" class:done={t.done} class:linked={t.id === linkedId} id={`task-${t.id}`}>
    {#if editId === t.id}
      <form
        class="edit"
        onsubmit={(e) => {
          e.preventDefault();
          saveEdit(t);
        }}
      >
        <label class="field grow">
          <span>{$_('tasks.title_label')}</span>
          <input bind:value={eTitle} />
        </label>
        <label class="field">
          <span>{$_('tasks.due')}</span>
          <input type="date" bind:value={eDue} />
        </label>
        <div class="edit-actions">
          <button class="ghost" type="button" onclick={() => (editId = null)}
            >{$_('common.cancel')}</button
          >
          <button class="primary" type="submit" disabled={!eTitle.trim() || $demoSession}
            >{$_('common.save')}</button
          >
        </div>
      </form>
    {:else}
      <button
        class="check"
        class:on={t.done}
        onclick={() => toggle(t)}
        aria-pressed={!!t.done}
        aria-label={$_(t.done ? 'tasks.mark_open' : 'tasks.mark_done', {
          values: { title: t.title }
        })}
      >
        <span class="box"
          >{#if t.done}<Check size={16} aria-hidden="true" />{/if}</span
        >
      </button>
      <div class="body">
        <strong>{t.title}</strong>
        {#if t.due_at}
          <small class:overdue={!t.done && isOverdue(t.due_at, today)}>
            {$_('tasks.due')}: {formatDate(dueDay(t.due_at), $locale)}
            {#if !t.done && isOverdue(t.due_at, today)}· {$_('tasks.overdue')}{/if}
          </small>
        {/if}
      </div>
      <button
        class="iconbtn"
        onclick={() => startEdit(t)}
        title={$_('tasks.edit')}
        aria-label={$_('tasks.edit_named', { values: { title: t.title } })}
        ><Pencil size={18} aria-hidden="true" /></button
      >
      <button
        class="iconbtn danger"
        onclick={() => del(t)}
        title={$_('common.delete')}
        aria-label={$_('tasks.delete_named', { values: { title: t.title } })}
        ><Trash2 size={18} aria-hidden="true" /></button
      >
    {/if}
  </li>
{/snippet}

<div class="page">
  <header><h1>{$_('tasks.title')}</h1></header>

  <DemoNote />
  <form
    class="adder"
    onsubmit={(e) => {
      e.preventDefault();
      add();
    }}
  >
    <label class="field grow">
      <span>{$_('tasks.new_label')}</span>
      <input bind:value={title} bind:this={titleInput} placeholder={$_('tasks.title_ph')} />
    </label>
    <label class="field">
      <span>{$_('tasks.due')}</span>
      <input type="date" bind:value={due} />
    </label>
    <button class="primary" type="submit" disabled={!title.trim() || $demoSession}
      >{$_('tasks.add')}</button
    >
  </form>

  {#if !loaded}
    <Skeleton n={3} height={54} />
  {:else if rows.length === 0 && $initialSyncPending}
    <Syncing />
  {:else}
    {#if lists.open.length === 0}
      <p class="muted empty">{$_('tasks.empty')}</p>
    {:else}
      <ul class="list">
        {#each lists.open as t (t.id)}
          {@render item(t)}
        {/each}
      </ul>
    {/if}

    {#if lists.completed.length > 0}
      <button
        class="toggle"
        aria-expanded={showCompleted}
        aria-controls="completed-tasks"
        onclick={() => (showCompleted = !showCompleted)}
      >
        <span class="caret" class:open={showCompleted} aria-hidden="true">›</span>
        {$_('tasks.completed', { values: { n: lists.completed.length } })}
      </button>
      {#if showCompleted}
        <ul class="list" id="completed-tasks">
          {#each lists.completed as t (t.id)}
            {@render item(t)}
          {/each}
        </ul>
      {/if}
    {/if}
  {/if}
</div>

{#if pendingDelete}
  <div class="undo" role="status">
    <span>{$_('tasks.deleted', { values: { title: pendingDelete.title } })}</span>
    <button onclick={undoDelete}>{$_('tasks.undo')}</button>
  </div>
{/if}

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
    color: var(--text-soft);
  }
  .empty {
    text-align: center;
    padding: 40px 0;
  }
  .adder,
  .edit {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 8px;
  }
  .adder {
    margin-bottom: 20px;
  }
  .edit {
    flex: 1;
  }
  .field {
    display: grid;
    gap: 4px;
    min-width: 0;
  }
  .field.grow {
    flex: 1 1 200px;
  }
  .field span {
    font-size: 0.82rem;
    font-weight: 600;
    color: var(--text-soft);
  }
  .field input {
    font: inherit;
    width: 100%;
    min-height: 44px;
    padding: 10px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    background: var(--surface);
    color: var(--text);
  }
  .edit-actions {
    display: flex;
    gap: 8px;
  }
  .primary,
  .ghost {
    min-height: 44px;
    border-radius: 11px;
    padding: 10px 16px;
    font: inherit;
    font-weight: 700;
    cursor: pointer;
    white-space: nowrap;
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
  .list {
    list-style: none;
    display: grid;
    gap: 10px;
  }
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 14px;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
  }
  .card.linked {
    border-color: var(--accent);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 30%, transparent);
  }
  .card.done .body strong {
    text-decoration: line-through;
    color: var(--text-soft);
  }
  /* 44 px touch target around a smaller visible box. */
  .check {
    display: grid;
    place-items: center;
    width: 44px;
    height: 44px;
    flex-shrink: 0;
    background: none;
    border: none;
    cursor: pointer;
  }
  .check .box {
    display: grid;
    place-items: center;
    width: 26px;
    height: 26px;
    border-radius: 8px;
    border: 2px solid var(--accent);
    background: var(--surface);
    color: var(--on-accent);
  }
  .check.on .box {
    background: var(--accent);
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .body strong {
    display: block;
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .body small {
    color: var(--text-soft);
    font-size: 0.82rem;
  }
  .body small.overdue {
    color: var(--danger);
    font-weight: 600;
  }
  .iconbtn {
    display: grid;
    place-items: center;
    width: 44px;
    height: 44px;
    flex-shrink: 0;
    background: none;
    border: none;
    border-radius: 10px;
    cursor: pointer;
    color: var(--text-soft);
  }
  .iconbtn:hover {
    color: var(--accent-text);
  }
  /* Kept apart from the edit button, so a slip of the thumb does not delete. */
  .iconbtn.danger {
    margin-left: 8px;
  }
  .iconbtn.danger:hover {
    color: var(--danger);
  }
  .toggle {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 44px;
    margin: 14px 0 6px;
    padding: 0 6px;
    background: none;
    border: none;
    font: inherit;
    font-weight: 700;
    color: var(--text-soft);
    cursor: pointer;
  }
  .caret {
    display: inline-block;
    transition: transform 0.12s;
  }
  .caret.open {
    transform: rotate(90deg);
  }
  .undo {
    position: fixed;
    left: 50%;
    transform: translateX(-50%);
    bottom: calc(84px + env(safe-area-inset-bottom));
    z-index: 900;
    width: min(92vw, 480px);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    background: var(--text);
    color: var(--bg);
    border-radius: 12px;
    padding: 4px 6px 4px 16px;
    font-size: 0.9rem;
  }
  .undo span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .undo button {
    min-height: 44px;
    padding: 0 14px;
    background: none;
    border: none;
    font: inherit;
    font-weight: 700;
    color: inherit;
    text-decoration: underline;
    cursor: pointer;
    flex-shrink: 0;
  }
</style>
