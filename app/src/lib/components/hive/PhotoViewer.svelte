<script lang="ts">
  // Shows photos full size, inside the app. Opens on the thumbnail, which is
  // on the device already, and replaces it with the full image once that is
  // there (from the device, or fetched on first view). Arrow keys step
  // through the photos, Escape closes, and closing hands the focus back to
  // whatever opened the viewer.
  import { tick, untrack } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { ChevronLeft, ChevronRight, CloudUpload, ImageUp, Trash2, X } from '@lucide/svelte';
  import {
    deletePhoto,
    openPhoto,
    uploadStates,
    type PhotoRow,
    type Unavailable
  } from '#lib/photos/index.js';
  import { demoSession } from '#lib/demo.js';
  import './hive.css';

  let {
    photos,
    describe,
    onchange,
    onreplace
  }: {
    photos: PhotoRow[];
    /** Alternative text of the photo at this position. */
    describe: (index: number) => string;
    /** A photo was deleted here. */
    onchange?: (() => void) | undefined;
    /** Offer "Replace photo" (the picture of a hive). */
    onreplace?: (() => void) | undefined;
  } = $props();

  let dialog = $state<HTMLDialogElement>();
  let index = $state<number | null>(null);
  let fullUrl = $state<string | null>(null);
  let loading = $state(false);
  let problem = $state<Unavailable | null>(null);
  let confirming = $state(false);
  let deleting = $state(false);
  let opener: HTMLElement | null = null;
  let closeButton = $state<HTMLButtonElement>();
  let cancelButton = $state<HTMLButtonElement>();
  let deleteButton = $state<HTMLButtonElement>();

  const current = $derived(index === null ? undefined : photos[index]);
  const uploadState = $derived(current ? $uploadStates.get(current.id) : undefined);

  /** Opens the viewer on the photo at this position. */
  export async function open(at: number) {
    opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    confirming = false;
    index = at;
    // The content is rendered first, so that the dialog opens with the focus
    // on its Close button rather than on nothing.
    await tick();
    if (dialog && !dialog.open) dialog.showModal();
    closeButton?.focus();
  }

  function release() {
    if (fullUrl) URL.revokeObjectURL(fullUrl);
    fullUrl = null;
  }

  // Load the full image whenever another photo is shown.
  $effect(() => {
    const id = current?.id;
    untrack(release);
    problem = null;
    loading = false;
    if (!id) return;
    let stale = false;
    loading = true;
    openPhoto(id)
      .then((res) => {
        if (stale) return;
        if ('blob' in res) fullUrl = URL.createObjectURL(res.blob);
        else problem = res.unavailable;
      })
      .catch(() => {
        if (!stale) problem = 'error';
      })
      .finally(() => {
        if (!stale) loading = false;
      });
    return () => {
      stale = true;
    };
  });

  // The list can shrink while the viewer is open (a delete here or on
  // another device): stay on a photo that exists, or close.
  $effect(() => {
    if (index === null) return;
    if (photos.length === 0) dialog?.close();
    else if (index >= photos.length) index = photos.length - 1;
  });

  function step(by: number) {
    if (index === null || photos.length < 2) return;
    confirming = false;
    index = (index + by + photos.length) % photos.length;
  }

  function onkeydown(e: KeyboardEvent) {
    if (confirming) return;
    if (e.key === 'ArrowLeft') step(-1);
    else if (e.key === 'ArrowRight') step(1);
    else return;
    e.preventDefault();
  }

  function closed() {
    index = null;
    confirming = false;
    release();
    opener?.focus();
    opener = null;
  }

  // The question appears where the Delete button was: move the focus to the
  // safe answer, and back when the question goes away.
  async function ask(on: boolean) {
    confirming = on;
    await tick();
    (on ? cancelButton : deleteButton)?.focus();
  }

  async function remove() {
    if (!current || deleting) return;
    deleting = true;
    try {
      await deletePhoto(current.id);
      confirming = false;
      onchange?.();
      closeButton?.focus();
    } finally {
      deleting = false;
    }
  }

  function replace() {
    dialog?.close();
    onreplace?.();
  }
</script>

<!-- A click on the dialog itself is a click on the area around the photo;
     the keyboard closes with Escape. -->
<dialog
  bind:this={dialog}
  class="viewer"
  aria-label={current && index !== null
    ? $_('photo.position', { values: { n: index + 1, total: photos.length } })
    : $_('photo.viewer')}
  onclose={closed}
  {onkeydown}
  onclick={(e) => {
    if (e.target === dialog) dialog?.close();
  }}
>
  {#if current && index !== null}
    <div class="bar">
      <span class="count" aria-hidden="true">{index + 1} / {photos.length}</span>
      <!-- svelte-ignore a11y_autofocus -->
      <button
        type="button"
        class="icon"
        autofocus
        bind:this={closeButton}
        aria-label={$_('photo.close')}
        onclick={() => dialog?.close()}
      >
        <X size={22} aria-hidden="true" />
      </button>
    </div>

    <div class="stage">
      {#if photos.length > 1}
        <button
          type="button"
          class="icon nav prev"
          aria-label={$_('photo.previous')}
          onclick={() => step(-1)}
        >
          <ChevronLeft size={26} aria-hidden="true" />
        </button>
      {/if}
      {#if fullUrl ?? current.thumb}
        <img class:preview={!fullUrl} src={fullUrl ?? current.thumb} alt={describe(index)} />
      {:else}
        <p class="hv-muted" role="img" aria-label={describe(index)}>{$_('photo.no_preview')}</p>
      {/if}
      {#if photos.length > 1}
        <button
          type="button"
          class="icon nav next"
          aria-label={$_('photo.next')}
          onclick={() => step(1)}
        >
          <ChevronRight size={26} aria-hidden="true" />
        </button>
      {/if}
    </div>

    <!-- Announced when it changes: what is (not yet) on the screen. -->
    <p class="status" role="status">
      {#if loading}
        {$_('photo.loading')}
      {:else if problem}
        {$_(`photo.unavailable_${problem}`)}
      {/if}
      {#if uploadState}
        <span class="upload">
          <CloudUpload size={16} aria-hidden="true" />
          {$_(`photo.upload_${uploadState}`)}
        </span>
      {/if}
    </p>

    <div class="acts">
      {#if confirming}
        <p class="ask" id="photo-delete-question">{$_('photo.confirm_delete')}</p>
        <button
          type="button"
          class="hv-btn danger"
          aria-describedby="photo-delete-question"
          disabled={deleting}
          onclick={remove}
        >
          {$_('photo.delete')}
        </button>
        <button
          type="button"
          class="hv-btn ghost"
          bind:this={cancelButton}
          onclick={() => ask(false)}
        >
          {$_('common.cancel')}
        </button>
      {:else}
        {#if onreplace}
          <button type="button" class="hv-btn ghost" disabled={$demoSession} onclick={replace}>
            <ImageUp size={18} aria-hidden="true" />
            {$_('photo.replace')}
          </button>
        {/if}
        <button
          type="button"
          class="hv-btn danger"
          bind:this={deleteButton}
          disabled={$demoSession}
          onclick={() => ask(true)}
        >
          <Trash2 size={18} aria-hidden="true" />
          {$_('photo.delete')}
        </button>
      {/if}
    </div>
  {/if}
</dialog>

<style>
  .viewer {
    width: min(96vw, 1100px);
    max-width: none;
    max-height: 96dvh;
    margin: auto;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: 16px;
    background: var(--surface);
    color: var(--text);
    overflow: hidden;
  }
  .viewer[open] {
    display: grid;
    grid-template-rows: auto minmax(0, 1fr) auto auto;
  }
  .viewer::backdrop {
    background: color-mix(in srgb, var(--bg) 80%, transparent);
  }
  .bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 4px 6px 4px 16px;
  }
  .count {
    color: var(--text-soft);
    font-size: 0.9rem;
    font-variant-numeric: tabular-nums;
  }
  .icon {
    display: grid;
    place-items: center;
    width: 44px;
    height: 44px;
    border: none;
    border-radius: 50%;
    background: transparent;
    color: var(--text);
    cursor: pointer;
  }
  .icon:focus-visible {
    outline: 2px solid var(--accent-text);
    outline-offset: -2px;
  }
  .stage {
    position: relative;
    display: grid;
    place-items: center;
    min-height: 200px;
    background: var(--bg);
  }
  .stage img {
    display: block;
    max-width: 100%;
    max-height: calc(96dvh - 190px);
    object-fit: contain;
  }
  /* The thumbnail stands in until the full image is there. */
  .stage img.preview {
    width: 100%;
    filter: blur(6px);
  }
  .nav {
    position: absolute;
    top: 50%;
    transform: translateY(-50%);
    background: var(--surface);
    border: 1px solid var(--border);
    z-index: 1;
  }
  .prev {
    left: 8px;
  }
  .next {
    right: 8px;
  }
  .status {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    min-height: 1.4em;
    margin: 0;
    padding: 8px 16px 0;
    color: var(--text-soft);
    font-size: 0.86rem;
  }
  .upload {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .acts {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
    padding: 10px 16px 14px;
  }
  .ask {
    flex: 1 1 220px;
    margin: 0;
    font-weight: 600;
  }
</style>
