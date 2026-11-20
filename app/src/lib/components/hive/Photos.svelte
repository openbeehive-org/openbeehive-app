<script lang="ts" module>
  // Everything the hive screens do with photos lives in this file: the hive
  // picture (pick / remove), the photo picker of the visit form, and the
  // thumbnails on a visit card. Photos are small JPEG data URLs stored
  // inline on the record.
  import { hives, inspections } from '#lib/local/repo.js';
  import { fileToThumbnail } from '#lib/image.js';

  /** Stores the photos picked in the visit form on a saved inspection. */
  export async function attachVisitPhotos(inspectionId: string, scope: string, photos: string[]) {
    // Photos go to the inspection's photo_keys OR-Set (add-wins, conflict-free).
    for (const ph of photos) await inspections.addPhoto(inspectionId, scope, ph);
  }
</script>

<script lang="ts">
  import { _ } from 'svelte-i18n';
  import { Camera } from '@lucide/svelte';
  import './hive.css';

  type HiveRef = { id: string; apiary_id: string; name: string; photo: string | null };

  let {
    kind,
    hive,
    photos = $bindable([]),
    onchange
  }: {
    /** hive: picture with picker · hive-remove: its remove link ·
        picker: photos for a visit being entered · gallery: a visit's photos */
    kind: 'hive' | 'hive-remove' | 'picker' | 'gallery';
    hive?: HiveRef;
    photos?: string[];
    onchange?: () => void;
  } = $props();

  let input = $state<HTMLInputElement>();
  let busy = $state(false);

  async function pickHivePhoto(e: Event) {
    const file = (e.target as HTMLInputElement).files?.[0];
    if (!file || !hive) return;
    busy = true;
    try {
      const dataUrl = await fileToThumbnail(file);
      await hives.setPhoto(hive.id, hive.apiary_id, dataUrl);
      onchange?.();
    } finally {
      busy = false;
    }
  }
  async function removeHivePhoto() {
    if (!hive) return;
    await hives.setPhoto(hive.id, hive.apiary_id, '');
    onchange?.();
  }

  async function pickVisitPhotos(e: Event) {
    const el = e.target as HTMLInputElement;
    if (!el.files) return;
    const added: string[] = [];
    for (const f of Array.from(el.files)) added.push(await fileToThumbnail(f, 640));
    photos = [...photos, ...added];
    el.value = '';
  }
</script>

{#if kind === 'hive' && hive}
  <!-- A real button opens the picker, so it is reachable by keyboard. -->
  <button type="button" class="avatar" onclick={() => input?.click()}>
    {#if hive.photo}
      <img src={hive.photo} alt={hive.name} />
    {:else}
      <img class="ph" src="/favicon.svg" alt="" />
    {/if}
    <span class="cam"
      >{#if busy}…{:else}<Camera size={12} aria-hidden="true" />{/if}</span
    >
    <span class="hv-sr">{$_('hives.photo')}</span>
  </button>
  <input type="file" accept="image/*" onchange={pickHivePhoto} bind:this={input} hidden />
{:else if kind === 'hive-remove' && hive?.photo}
  <button type="button" class="hv-link soft" onclick={removeHivePhoto}
    >{$_('hives.remove_photo')}</button
  >
{:else if kind === 'picker'}
  <div class="photos-edit">
    {#each photos as ph, idx}
      <span class="thumb">
        <img src={ph} alt="" />
        <button
          type="button"
          class="rm"
          aria-label={$_('insp.remove_photo', { values: { n: idx + 1 } })}
          onclick={() => (photos = photos.filter((_p, i) => i !== idx))}>×</button
        >
      </span>
    {/each}
    <button type="button" class="addphoto" onclick={() => input?.click()}>
      <Camera size={18} aria-hidden="true" />
      {$_('insp.add_photo')}
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
{:else if kind === 'gallery' && photos.length}
  <div class="vphotos">
    {#each photos as ph, idx}
      <a class="vphoto" href={ph} target="_blank" rel="noreferrer"
        ><img src={ph} alt={$_('insp.photo_n', { values: { n: idx + 1 } })} /></a
      >
    {/each}
  </div>
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
  .vphotos {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 10px;
  }
  .vphoto {
    width: 76px;
    height: 76px;
    border-radius: 10px;
    overflow: hidden;
    border: 1px solid var(--border);
  }
  .vphoto img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
</style>
