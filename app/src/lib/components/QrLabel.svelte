<script lang="ts">
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { hiveUrl, qrSvg, shortCode } from '#lib/qr.js';
  import { printQrLabel } from '#lib/qrlabel.js';

  let { hiveId, name = '' }: { hiveId: string; name?: string } = $props();
  let svg = $state('');
  onMount(async () => {
    svg = await qrSvg(hiveUrl(hiveId), 240);
  });

  // Print a clean label in a separate window (avoids app chrome).
  function printLabel() {
    printQrLabel({ name, code: shortCode(hiveId), svg });
  }

  // Download the QR as an SVG file.
  function download() {
    const blob = new Blob([svg], { type: 'image/svg+xml' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `hive-${shortCode(hiveId)}.svg`;
    a.click();
    URL.revokeObjectURL(a.href);
  }
</script>

<div class="card">
  <!-- eslint-disable-next-line svelte/no-at-html-tags -- svg is rendered locally by qr.ts, never user input -->
  <div class="qr">{@html svg}</div>
  <div class="meta">
    {#if name}<strong>{name}</strong>{/if}
    <span class="code">{shortCode(hiveId)}</span>
  </div>
  <div class="actions">
    <button type="button" class="btn primary" onclick={printLabel}>{$_('qr.print')}</button>
    <button type="button" class="btn ghost" onclick={download}>SVG</button>
  </div>
</div>

<style>
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 16px;
    padding: 18px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 12px;
    max-width: 300px;
  }
  .qr :global(svg) {
    width: 200px;
    height: auto;
    display: block;
  }
  .meta {
    text-align: center;
  }
  .meta strong {
    font-family: 'Fraunces', serif;
    font-size: 1.15rem;
    display: block;
  }
  .code {
    color: var(--text-soft);
    letter-spacing: 0.18em;
    font-size: 0.8rem;
    font-weight: 600;
  }
  .actions {
    display: flex;
    gap: 8px;
  }
  .btn {
    font-family: inherit;
    font-weight: 600;
    cursor: pointer;
    border-radius: 10px;
    padding: 9px 16px;
    border: 1px solid transparent;
  }
  .primary {
    background: var(--accent);
    color: var(--on-accent);
  }
  .ghost {
    background: transparent;
    border-color: var(--border-strong);
    color: var(--text);
  }
</style>
