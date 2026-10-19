<script lang="ts">
  // Loading placeholder: n shimmering blocks shaped like the cards they stand
  // in for. Shown while a page's first local read is still pending, so lists
  // never flash an empty state (or zeros) before the data is actually known.
  let { n = 3, height = 64 } = $props();
</script>

<div class="skl" aria-hidden="true">
  {#each Array(n) as _, i (i)}
    <div class="blk" style={`height:${height}px`}></div>
  {/each}
</div>

<style>
  .skl {
    display: grid;
    gap: 12px;
  }
  .blk {
    border: 1px solid var(--line);
    border-radius: 16px;
    background: linear-gradient(
      100deg,
      var(--cream2) 40%,
      rgba(199, 127, 34, 0.07) 50%,
      var(--cream2) 60%
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
</style>
