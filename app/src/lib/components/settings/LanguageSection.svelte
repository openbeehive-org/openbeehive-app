<script lang="ts">
  import { _, locale } from 'svelte-i18n';
  import { Check } from '@lucide/svelte';
  import { changeLanguage, SUPPORTED, LANG_LABELS } from '#lib/i18n/index.js';
  import './settings.css';
</script>

<section class="st-card">
  <h2>{$_('settings.language')}</h2>
  <p class="st-help">{$_('settings.language_help')}</p>
  <div class="langs">
    {#each SUPPORTED as l (l)}
      {@const on = !!$locale?.startsWith(l)}
      <button
        type="button"
        class="lang"
        class:on
        aria-pressed={on}
        lang={l}
        onclick={() => changeLanguage(l)}
      >
        <span class="code">{l.toUpperCase()}</span>
        <span class="name">{LANG_LABELS[l]}</span>
        {#if on}<span class="tick"><Check size={16} aria-hidden="true" /></span>{/if}
      </button>
    {/each}
  </div>
</section>

<style>
  .langs {
    display: grid;
    gap: 10px;
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  }
  .lang {
    display: flex;
    align-items: center;
    gap: 11px;
    min-height: 48px;
    padding: 12px 15px;
    cursor: pointer;
    border: 1px solid var(--border-strong);
    border-radius: 13px;
    background: var(--bg);
    font: inherit;
    color: var(--text);
    text-align: left;
  }
  .lang.on {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 10%, var(--surface));
  }
  .code {
    font-weight: 800;
    font-size: 0.82rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 7px;
    padding: 3px 7px;
    color: var(--text-soft);
  }
  .lang.on .code {
    background: var(--accent);
    color: var(--on-accent);
    border-color: var(--accent);
  }
  .name {
    font-weight: 600;
    flex: 1;
  }
  .tick {
    color: var(--accent-text);
    display: inline-flex;
  }
</style>
