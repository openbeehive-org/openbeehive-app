<script lang="ts">
  // A count entered with big − / + buttons. Empty (null) means "not
  // recorded"; 0 is a real observation, one tap on − away.
  import { _ } from 'svelte-i18n';
  import { Minus, Plus } from '@lucide/svelte';

  let {
    value = $bindable(null),
    label,
    step = 1,
    max = 999
  }: { value?: number | null; label: string; step?: number; max?: number } = $props();

  const uid = $props.id();

  function bump(dir: 1 | -1) {
    if (value == null) value = dir === 1 ? step : 0;
    else value = Math.min(max, Math.max(0, value + dir * step));
  }
  function typed(e: Event) {
    const raw = (e.currentTarget as HTMLInputElement).value.trim();
    const n = Number(raw);
    value = raw === '' || Number.isNaN(n) ? null : Math.min(max, Math.max(0, n));
  }
</script>

<div class="stepper">
  <label for={uid}>{label}</label>
  <div class="row">
    <button
      type="button"
      onclick={() => bump(-1)}
      aria-label={$_('insp.decrease', { values: { label } })}
    >
      <Minus size={22} aria-hidden="true" />
    </button>
    <input
      id={uid}
      type="number"
      inputmode="numeric"
      min="0"
      {max}
      {step}
      placeholder="–"
      value={value ?? ''}
      oninput={typed}
    />
    <button
      type="button"
      onclick={() => bump(1)}
      aria-label={$_('insp.increase', { values: { label } })}
    >
      <Plus size={22} aria-hidden="true" />
    </button>
  </div>
</div>

<style>
  .stepper {
    display: flex;
    flex-direction: column;
    gap: 5px;
    min-width: 0;
  }
  label {
    font-size: 0.86rem;
    font-weight: 600;
    color: var(--text-soft);
  }
  .row {
    display: grid;
    grid-template-columns: 52px minmax(0, 1fr) 52px;
    gap: 6px;
  }
  button {
    display: grid;
    place-items: center;
    min-height: 52px;
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    background: var(--bg);
    color: var(--text);
    cursor: pointer;
  }
  button:active {
    background: var(--border);
  }
  input {
    font: inherit;
    font-size: 1.15rem;
    font-weight: 700;
    text-align: center;
    min-height: 52px;
    width: 100%;
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    background: var(--surface);
    color: var(--text);
    appearance: textfield;
    -moz-appearance: textfield;
  }
  input::-webkit-outer-spin-button,
  input::-webkit-inner-spin-button {
    appearance: none;
    margin: 0;
  }
  button:focus-visible,
  input:focus-visible {
    outline: 2px solid var(--accent-text);
    outline-offset: 2px;
  }
</style>
