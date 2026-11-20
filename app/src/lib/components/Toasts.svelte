<script lang="ts">
  import { _ } from 'svelte-i18n';
  import { X } from '@lucide/svelte';
  import { toasts, dismiss } from '#lib/toast.js';
</script>

{#if $toasts.length}
  <div class="toasts">
    {#each $toasts as t (t.id)}
      <div class="toast">
        <p role="alert">{t.text}</p>
        <button type="button" aria-label={$_('app.dismiss')} onclick={() => dismiss(t.id)}>
          <X size={18} aria-hidden="true" />
        </button>
      </div>
    {/each}
  </div>
{/if}

<style>
  .toasts {
    position: fixed;
    bottom: calc(84px + env(safe-area-inset-bottom));
    left: 50%;
    transform: translateX(-50%);
    display: grid;
    gap: 8px;
    z-index: 1000;
    width: min(92vw, 480px);
  }
  /* Inverted page colours, so a toast stands out in both themes. */
  .toast {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    background: var(--text);
    color: var(--bg);
    border-radius: 12px;
    padding: 6px 6px 6px 16px;
    box-shadow: 0 6px 20px var(--shadow);
  }
  p {
    flex: 1;
    font-size: 0.88rem;
    padding: 6px 0;
    overflow-wrap: anywhere;
  }
  button {
    flex: none;
    display: grid;
    place-items: center;
    width: 32px;
    height: 32px;
    border: none;
    border-radius: 8px;
    background: transparent;
    color: inherit;
    cursor: pointer;
  }
  button:focus-visible {
    outline-color: var(--bg);
  }
</style>
