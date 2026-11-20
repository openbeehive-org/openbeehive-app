<script lang="ts">
  // System / Light / Dark switch. The choice is stored on this device.
  import { _ } from 'svelte-i18n';
  import { Monitor, Moon, Sun } from '@lucide/svelte';
  import { setTheme, themeChoice, THEME_CHOICES } from '#lib/theme.js';

  const ICONS = { system: Monitor, light: Sun, dark: Moon };
</script>

<div class="switch" role="radiogroup" aria-label={$_('theme.title')}>
  {#each THEME_CHOICES as choice (choice)}
    {@const Icon = ICONS[choice]}
    <label class:on={$themeChoice === choice}>
      <input
        type="radio"
        name="theme"
        value={choice}
        checked={$themeChoice === choice}
        onchange={() => setTheme(choice)}
      />
      <Icon size={18} aria-hidden="true" />
      <span>{$_(`theme.${choice}`)}</span>
    </label>
  {/each}
</div>

<style>
  .switch {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 8px;
  }
  label {
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    min-height: 46px;
    padding: 10px 8px;
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    background: var(--bg);
    color: var(--text);
    font-weight: 600;
    cursor: pointer;
  }
  /* The selected choice is filled, so it does not rely on hue alone. */
  label.on {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--on-accent);
  }
  label:has(input:focus-visible) {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  /* The radio stays in the accessibility tree and keyboard order; the label
     is what is drawn. */
  input {
    position: absolute;
    inset: 0;
    opacity: 0;
    margin: 0;
    cursor: pointer;
  }
  span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
