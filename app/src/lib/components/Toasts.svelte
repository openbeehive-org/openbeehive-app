<script lang="ts">
  import { toasts, dismiss } from '#lib/toast.js';
</script>

{#if $toasts.length}
  <div class="toasts">
    {#each $toasts as t (t.id)}
      <button class="toast" onclick={() => dismiss(t.id)}>
        <span role="alert">{t.text}</span>
      </button>
    {/each}
  </div>
{/if}

<style>
  .toasts {
    position: fixed;
    bottom: 84px;
    left: 50%;
    transform: translateX(-50%);
    display: grid;
    gap: 8px;
    z-index: 1000;
    width: min(92vw, 480px);
  }
  .toast {
    background: #3b2f23;
    color: #fdf6ea;
    border: none;
    border-radius: 12px;
    padding: 12px 16px;
    font: inherit;
    font-size: 0.88rem;
    text-align: left;
    cursor: pointer;
    box-shadow: 0 6px 20px rgba(0, 0, 0, 0.25);
  }
</style>
