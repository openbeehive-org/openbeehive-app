<script lang="ts">
  // A printable sheet of QR labels. The caller mounts it on <body>, where it
  // is only visible on paper: while it exists, printing hides the rest of the
  // app. That needs no popup window and no markup assembled from strings, so
  // hive names are ordinary text and the sheet works under a strict
  // Content-Security-Policy and inside the installed app.
  import { onMount } from 'svelte';

  type Label = { id: string; name: string; code: string; svg: string };
  let { title, labels, onclose }: { title: string; labels: Label[]; onclose: () => void } =
    $props();

  onMount(() => {
    document.body.classList.add('printing-sheet');
    window.addEventListener('afterprint', onclose);
    // One frame later, so the codes are laid out before the dialog opens.
    const timer = setTimeout(() => window.print(), 50);
    return () => {
      clearTimeout(timer);
      window.removeEventListener('afterprint', onclose);
      document.body.classList.remove('printing-sheet');
    };
  });
</script>

<section class="print-sheet" aria-hidden="true">
  <h1>{title}</h1>
  <div class="grid">
    {#each labels as l (l.id)}
      <div class="cell">
        <!-- eslint-disable-next-line svelte/no-at-html-tags -- svg is rendered locally by qr.ts, never user input -->
        <div class="qr">{@html l.svg}</div>
        <div class="n">{l.name}</div>
        <div class="c">{l.code}</div>
      </div>
    {/each}
  </div>
</section>

<style>
  .print-sheet {
    display: none;
  }
  @media print {
    :global(body.printing-sheet > :not(.print-sheet)) {
      display: none !important;
    }
    :global(body.printing-sheet) {
      background: none;
    }
    .print-sheet {
      display: block;
      color: black;
      font-family: system-ui, sans-serif;
    }
    h1 {
      font-family: inherit;
      font-size: 16px;
      margin: 0 0 10px;
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(3, 1fr);
      gap: 10px;
    }
    .cell {
      border: 1px solid silver;
      border-radius: 10px;
      padding: 10px;
      text-align: center;
      break-inside: avoid;
    }
    .qr :global(svg) {
      width: 100%;
      height: auto;
      max-width: 150px;
    }
    .n {
      font-weight: 700;
      font-size: 13px;
      margin-top: 6px;
      overflow-wrap: anywhere;
    }
    .c {
      color: dimgray;
      letter-spacing: 0.15em;
      font-size: 11px;
    }
  }
</style>
