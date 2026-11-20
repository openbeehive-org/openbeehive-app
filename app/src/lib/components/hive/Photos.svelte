<script lang="ts" module>
  // Everything the hive screens do with photos lives in this file and in
  // PhotoViewer: the hive's picture, the photos of a visit while it is
  // entered or corrected, and the thumbnails on a visit card.
  //
  // A photo is a record with a thumbnail plus a full-size image that is
  // uploaded on its own (lib/photos). What is shown in lists is always the
  // thumbnail; the viewer loads the full image.
  import { addPhoto } from '#lib/photos/index.js';
  import type { PreparedPhoto } from '#lib/image.js';

  /** Stores the photos picked in the visit form on a saved inspection. */
  export async function attachVisitPhotos(inspectionId: string, picked: PreparedPhoto[]) {
    for (const photo of picked) await addPhoto({ entity: 'inspection', id: inspectionId }, photo);
  }
</script>

<script lang="ts">
  import { _ } from 'svelte-i18n';
  import { Camera, CloudUpload } from '@lucide/svelte';
  import { preparePhoto } from '#lib/image.js';
  import { photoRecords } from '#lib/local/repo.js';
  import { dataVersion } from '#lib/local/live.js';
  import { deletePhoto, setHivePicture, uploadStates, type PhotoRow } from '#lib/photos/index.js';
  import PhotoViewer from './PhotoViewer.svelte';
  import './hive.css';

  type HiveRef = { id: string; name: string };

  let {
    kind,
    hive,
    visitId,
    label = '',
    photos = [],
    picked = $bindable([]),
    onchange
  }: {
    /** hive: the hive's picture · picker: the photos of a visit in its form ·
        gallery: the photos of a saved visit */
    kind: 'hive' | 'picker' | 'gallery';
    hive?: HiveRef;
    /** picker: the visit being corrected; its stored photos can be deleted. */
    visitId?: string | undefined;
    /** What the photos belong to, for their alternative text (a visit's date). */
    label?: string;
    /** gallery: the stored photos to show. */
    photos?: PhotoRow[];
    /** picker: photos chosen in the form, stored when the visit is saved. */
    picked?: PreparedPhoto[];
    onchange?: (() => void) | undefined;
  } = $props();

  let input = $state<HTMLInputElement>();
  let viewer = $state<PhotoViewer>();
  let busy = $state(false);
  let failed = $state(false);
  // hive and picker read their stored photos themselves.
  let stored = $state<PhotoRow[]>([]);

  const shown = $derived(kind === 'gallery' ? photos : stored);
  const picture = $derived(kind === 'hive' ? stored[0] : undefined);

  async function load() {
    if (kind === 'hive' && hive) {
      const row = await photoRecords.hivePicture(hive.id);
      stored = row ? [row] : [];
    } else if (kind === 'picker' && visitId) {
      stored = await photoRecords.of('inspection', visitId);
    }
  }
  $effect(() => {
    $dataVersion;
    void load();
  });

  async function changed() {
    await load();
    onchange?.();
  }

  const describe = (index: number) =>
    kind === 'hive'
      ? $_('photo.alt_hive', { values: { name: hive?.name ?? '' } })
      : $_('photo.alt_visit', { values: { n: index + 1, total: shown.length, date: label } });

  // Reads the chosen files; `use` receives each prepared photo.
  async function pick(e: Event, use: (photo: PreparedPhoto) => Promise<void> | void) {
    const el = e.target as HTMLInputElement;
    const files = Array.from(el.files ?? []);
    el.value = '';
    if (files.length === 0) return;
    busy = true;
    failed = false;
    try {
      for (const file of files) await use(await preparePhoto(file));
    } catch (err) {
      console.warn('photo not added:', err);
      failed = true;
    } finally {
      busy = false;
    }
  }

  const pickHivePicture = (e: Event) =>
    pick(e, async (photo) => {
      if (!hive) return;
      await setHivePicture(hive.id, photo);
      await changed();
    });

  const pickVisitPhotos = (e: Event) =>
    pick(e, (photo) => {
      picked = [...picked, photo];
    });

  async function removeStored(photo: PhotoRow, position: number) {
    const question = $_('photo.confirm_delete_n', { values: { n: position } });
    if (!window.confirm(question)) return;
    await deletePhoto(photo.id);
    await changed();
  }
</script>

{#snippet uploadBadge(id: string)}
  {@const state = $uploadStates.get(id)}
  {#if state}
    <span class="badge" class:problem={state !== 'waiting'} title={$_(`photo.upload_${state}`)}>
      <CloudUpload size={13} aria-hidden="true" />
      <span class="hv-sr">{$_(`photo.upload_${state}`)}</span>
    </span>
  {/if}
{/snippet}

{#if kind === 'hive' && hive}
  <!-- A real button, so it is reachable by keyboard: it opens the picture,
       or the file picker while there is none. -->
  <button type="button" class="avatar" onclick={() => (picture ? viewer?.open(0) : input?.click())}>
    {#if picture?.thumb}
      <img src={picture.thumb} alt={describe(0)} />
    {:else}
      <img class="ph" src="/favicon.svg" alt="" />
    {/if}
    <span class="cam"
      >{#if busy}…{:else}<Camera size={12} aria-hidden="true" />{/if}</span
    >
    {#if picture}{@render uploadBadge(picture.id)}{/if}
    <span class="hv-sr">{picture ? $_('photo.open_hive') : $_('hives.photo')}</span>
  </button>
  <input type="file" accept="image/*" onchange={pickHivePicture} bind:this={input} hidden />
  {#if failed}<span class="hv-sr" role="alert">{$_('photo.failed')}</span>{/if}
  <PhotoViewer
    bind:this={viewer}
    photos={stored}
    {describe}
    onchange={changed}
    onreplace={() => input?.click()}
  />
{:else if kind === 'picker'}
  <div class="photos-edit">
    {#each stored as ph, idx (ph.id)}
      <span class="thumb">
        {#if ph.thumb}<img src={ph.thumb} alt={describe(idx)} />{/if}
        {@render uploadBadge(ph.id)}
        <button
          type="button"
          class="rm"
          aria-label={$_('photo.delete_n', { values: { n: idx + 1 } })}
          onclick={() => removeStored(ph, idx + 1)}>×</button
        >
      </span>
    {/each}
    {#each picked as ph, idx}
      <span class="thumb">
        <img src={ph.thumb} alt={$_('photo.alt_new', { values: { n: idx + 1 } })} />
        <button
          type="button"
          class="rm"
          aria-label={$_('insp.remove_photo', { values: { n: stored.length + idx + 1 } })}
          onclick={() => (picked = picked.filter((_p, i) => i !== idx))}>×</button
        >
      </span>
    {/each}
    <button type="button" class="addphoto" disabled={busy} onclick={() => input?.click()}>
      <Camera size={18} aria-hidden="true" />
      {busy ? $_('photo.preparing') : $_('insp.add_photo')}
    </button>
    <input
      type="file"
      accept="image/*"
      multiple
      onchange={pickVisitPhotos}
      bind:this={input}
      hidden
    />
  </div>
  {#if failed}<p class="failed" role="alert">{$_('photo.failed')}</p>{/if}
{:else if kind === 'gallery' && photos.length}
  <ul class="vphotos">
    {#each photos as ph, idx (ph.id)}
      <li>
        <!-- Named by the photo's alternative text; the badge adds its state. -->
        <button type="button" class="vphoto" onclick={() => viewer?.open(idx)}>
          {#if ph.thumb}
            <img src={ph.thumb} alt={describe(idx)} />
          {:else}
            <Camera size={22} aria-hidden="true" />
            <span class="hv-sr">{describe(idx)}</span>
          {/if}
          {@render uploadBadge(ph.id)}
        </button>
      </li>
    {/each}
  </ul>
  <PhotoViewer bind:this={viewer} {photos} {describe} {onchange} />
{/if}

<style>
  .avatar {
    position: relative;
    width: 68px;
    height: 68px;
    border-radius: 16px;
    overflow: hidden;
    flex-shrink: 0;
    cursor: pointer;
    border: 1px solid var(--border);
    background: var(--warn-bg);
    display: grid;
    place-items: center;
    padding: 0;
  }
  .avatar img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .avatar .ph {
    width: 30px;
    height: 30px;
  }
  .avatar .cam {
    display: flex;
    align-items: center;
    position: absolute;
    right: 3px;
    bottom: 3px;
    font-size: 0.72rem;
    background: var(--surface);
    color: var(--text);
    border-radius: 7px;
    padding: 1px 4px;
    line-height: 1.2;
  }
  .avatar:focus-visible,
  .addphoto:focus-visible,
  .rm:focus-visible,
  .vphoto:focus-visible {
    outline: 2px solid var(--accent-text);
    outline-offset: 2px;
  }

  /* "Waiting to upload", on the thumbnail. */
  .badge {
    position: absolute;
    left: 3px;
    bottom: 3px;
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    background: var(--surface);
    color: var(--text-soft);
    border: 1px solid var(--border);
  }
  .badge.problem {
    background: var(--warn-bg);
    color: var(--warn-text);
  }

  .photos-edit {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .thumb {
    position: relative;
    width: 72px;
    height: 72px;
    border-radius: 10px;
    overflow: hidden;
    background: var(--bg);
  }
  .thumb img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .rm {
    position: absolute;
    top: 0;
    right: 0;
    width: 44px;
    height: 44px;
    display: grid;
    place-items: start end;
    padding: 3px 5px;
    border: none;
    background: transparent;
    color: var(--on-accent);
    text-shadow: 0 0 3px var(--text);
    font-size: 1.2rem;
    font-weight: 700;
    line-height: 1;
    cursor: pointer;
  }
  .addphoto {
    min-width: 72px;
    min-height: 72px;
    padding: 6px 10px;
    border: 1px dashed var(--border-strong);
    border-radius: 10px;
    background: transparent;
    display: grid;
    place-items: center;
    cursor: pointer;
    color: var(--text-soft);
    font: inherit;
    font-size: 0.8rem;
    font-weight: 600;
    text-align: center;
  }
  .addphoto:disabled {
    cursor: default;
    opacity: 0.6;
  }
  .failed {
    margin: 0;
    color: var(--danger);
    font-size: 0.86rem;
  }
  .vphotos {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin: 10px 0 0;
    padding: 0;
    list-style: none;
  }
  .vphoto {
    position: relative;
    display: grid;
    place-items: center;
    width: 76px;
    height: 76px;
    padding: 0;
    border-radius: 10px;
    overflow: hidden;
    border: 1px solid var(--border);
    background: var(--bg);
    color: var(--text-soft);
    cursor: pointer;
  }
  .vphoto img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
</style>
