<script lang="ts">
  // One-tap rating: a row of buttons instead of a select. Tapping the
  // chosen level again clears it (0 = not rated).
  let {
    value = $bindable(0),
    label,
    options
  }: { value?: number; label: string; options: { value: number; label: string }[] } = $props();

  const uid = $props.id();
</script>

<div class="seg">
  <span class="lbl" id={uid}>{label}</span>
  <div class="opts" role="group" aria-labelledby={uid}>
    {#each options as o}
      <button
        type="button"
        class:on={value === o.value}
        aria-pressed={value === o.value}
        onclick={() => (value = value === o.value ? 0 : o.value)}>{o.label}</button
      >
    {/each}
  </div>
</div>

<style>
  .seg {
    display: flex;
    flex-direction: column;
    gap: 5px;
    min-width: 0;
  }
  .lbl {
    font-size: 0.86rem;
    font-weight: 600;
    color: var(--text-soft);
  }
  .opts {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  button {
    flex: 1 1 auto;
    min-height: 46px;
    padding: 8px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    background: var(--surface);
    color: var(--text);
    font: inherit;
    font-size: 0.95rem;
    font-weight: 600;
    cursor: pointer;
  }
  button.on {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--on-accent);
  }
  button:focus-visible {
    outline: 2px solid var(--accent-text);
    outline-offset: 2px;
  }
</style>
