<script lang="ts">
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { hiveUrl, qrSvg, shortCode } from '#lib/qr.js';

  let { hiveId, name = '' }: { hiveId: string; name?: string } = $props();
  let svg = $state('');
  onMount(async () => {
    svg = await qrSvg(hiveUrl(hiveId), 240);
  });

  // Print a clean label in a separate window (avoids app chrome).
  function printLabel() {
    const w = window.open('', '_blank', 'width=420,height=560');
    if (!w) return;
    w.document.write(`<!doctype html><html><head><title>${name}</title>
      <style>body{font-family:system-ui,sans-serif;text-align:center;padding:28px;margin:0}
      .n{font-size:20px;font-weight:700;margin:14px 0 2px}.c{color:#666;letter-spacing:.18em}
      svg{width:280px;height:auto}</style></head>
      <body>${svg}<div class="n">${name || 'Hive'}</div>
      <div class="c">${shortCode(hiveId)}</div></body></html>`);
    w.document.close();
    w.focus();
    setTimeout(() => w.print(), 150);
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
    <strong>{name || 'Hive'}</strong>
    <span class="code">{shortCode(hiveId)}</span>
  </div>
  <div class="actions">
    <button class="btn primary" onclick={printLabel}>{$_('qr.print')}</button>
    <button class="btn ghost" onclick={download}>SVG</button>
  </div>
</div>

<style>
  .card {
    background: var(--cream2, #fffdf7);
    border: 1px solid var(--line, #e5dcc6);
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
    color: var(--ink-soft, #6b5e48);
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
    background: var(--honey, #c77f22);
    color: #fff;
  }
  .ghost {
    background: transparent;
    border-color: var(--line, #e5dcc6);
    color: var(--ink, #2c2316);
  }
</style>
