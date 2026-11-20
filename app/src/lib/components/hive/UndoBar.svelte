<script lang="ts">
  // "Deleted — Undo": shown for a few seconds after a delete. The page holds
  // one instance and calls offer() on it.
  import { _ } from 'svelte-i18n';
  import type { Undo } from '#lib/local/history.js';

  const TTL_MS = 8000;

  let pending = $state<{ text: string; undo: Undo } | null>(null);
  let busy = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  export function offer(text: string, undo: Undo) {
    clearTimeout(timer);
    pending = { text, undo };
    timer = setTimeout(() => (pending = null), TTL_MS);
  }

  async function run() {
    if (!pending || busy) return;
    clearTimeout(timer);
    busy = true;
    try {
      await pending.undo();
    } finally {
      busy = false;
      pending = null;
    }
  }

  $effect(() => () => clearTimeout(timer));
</script>

<div class="wrap" role="status" aria-live="polite">
  {#if pending}
    <div class="bar">
      <span>{pending.text}</span>
      <button type="button" onclick={run} disabled={busy}>{$_('hives.undo')}</button>
    </div>
  {/if}
</div>

<style>
  .wrap {
    position: fixed;
    left: 50%;
    transform: translateX(-50%);
    /* above the mobile tab bar */
    bottom: calc(84px + env(safe-area-inset-bottom));
    width: min(92vw, 480px);
    z-index: 900;
    pointer-events: none;
  }
  .bar {
    pointer-events: auto;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 6px 6px 6px 16px;
    border-radius: 12px;
    background: var(--text);
    color: var(--bg);
    font-size: 0.92rem;
  }
  button {
    min-height: 44px;
    padding: 0 14px;
    border: 1px solid var(--bg);
    border-radius: 9px;
    background: transparent;
    color: var(--bg);
    font: inherit;
    font-weight: 700;
    cursor: pointer;
  }
  button:focus-visible {
    outline: 2px solid var(--bg);
    outline-offset: 2px;
  }
  @media (min-width: 900px) {
    .wrap {
      bottom: 24px;
    }
  }
</style>
