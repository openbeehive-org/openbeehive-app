<script lang="ts">
  // Loading placeholder: n shimmering blocks shaped like the cards they stand
  // in for. Shown while a page's first local read is still pending, so lists
  // never flash an empty state (or zeros) before the data is actually known.
  let { n = 3, height = 64 } = $props();

  // Set as a style property rather than a style attribute, which a strict
  // Content-Security-Policy would block.
  function setHeight(el: HTMLElement) {
    el.style.height = `${height}px`;
  }
</script>

<div class="skl" aria-hidden="true">
  {#each Array(n) as _, i (i)}
    <div class="blk" {@attach setHeight}></div>
  {/each}
</div>

<style>
  .skl {
    display: grid;
    gap: 12px;
  }
  .blk {
    border: 1px solid var(--border);
    border-radius: 16px;
    background: linear-gradient(
      100deg,
      var(--surface) 40%,
      color-mix(in srgb, var(--accent) 9%, var(--surface)) 50%,
      var(--surface) 60%
    );
    background-size: 200% 100%;
    animation: shimmer 1.4s ease-in-out infinite;
  }
  @keyframes shimmer {
    from {
      background-position: 200% 0;
    }
    to {
      background-position: -200% 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .blk {
      animation: none;
      background: var(--surface);
    }
  }
</style>
