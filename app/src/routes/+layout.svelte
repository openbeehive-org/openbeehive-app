<script lang="ts">
  import { setupI18n } from '#lib/i18n/index.js';
  import { isLoading } from 'svelte-i18n';
  setupI18n();
  let { children } = $props();
</script>

{#if !$isLoading}
  {@render children()}
{/if}

<style>
  /* Self-hosted fonts (app/static/fonts) - no remote resource loading. */
  @font-face {
    font-family: 'Fraunces';
    font-style: normal;
    font-weight: 500;
    font-display: swap;
    src: url('/fonts/fraunces-v38-latin_latin-ext-500.woff2') format('woff2');
  }
  @font-face {
    font-family: 'Fraunces';
    font-style: normal;
    font-weight: 600;
    font-display: swap;
    src: url('/fonts/fraunces-v38-latin_latin-ext-600.woff2') format('woff2');
  }
  @font-face {
    font-family: 'Fraunces';
    font-style: normal;
    font-weight: 700;
    font-display: swap;
    src: url('/fonts/fraunces-v38-latin_latin-ext-700.woff2') format('woff2');
  }
  @font-face {
    font-family: 'Hanken Grotesk';
    font-style: normal;
    font-weight: 400;
    font-display: swap;
    src: url('/fonts/hanken-grotesk-v12-latin_latin-ext-regular.woff2') format('woff2');
  }
  @font-face {
    font-family: 'Hanken Grotesk';
    font-style: normal;
    font-weight: 500;
    font-display: swap;
    src: url('/fonts/hanken-grotesk-v12-latin_latin-ext-500.woff2') format('woff2');
  }
  @font-face {
    font-family: 'Hanken Grotesk';
    font-style: normal;
    font-weight: 600;
    font-display: swap;
    src: url('/fonts/hanken-grotesk-v12-latin_latin-ext-600.woff2') format('woff2');
  }
  @font-face {
    font-family: 'Hanken Grotesk';
    font-style: normal;
    font-weight: 700;
    font-display: swap;
    src: url('/fonts/hanken-grotesk-v12-latin_latin-ext-700.woff2') format('woff2');
  }

  :global(:root) {
    --cream: #fbf6ea;
    --cream2: #fffdf7;
    --ink: #2c2316;
    --ink-soft: #6b5e48;
    --honey: #c77f22;
    --honey-d: #a4641a;
    --honey-l: #e9a744;
    --moss: #5c6b4a;
    --line: #e5dcc6;

    /* Semantic tokens: components use these, never raw colours, so a theme
       only has to redefine this block. */
    --bg: var(--cream);
    --surface: var(--cream2);
    --text: var(--ink);
    --text-soft: var(--ink-soft);
    --border: var(--line);
    --border-strong: #8f8270; /* form controls: 3:1 against --surface */
    --accent: var(--honey-d); /* button backgrounds: 4.5:1 with --on-accent */
    --on-accent: #ffffff;
    --accent-text: #8a5210; /* accent-coloured text on --bg / --surface */
    --danger: #b3261e;
    --on-danger: #ffffff;
    --ok: var(--moss);
    --warn-bg: #f6e3b4;
    --warn-text: #5a3d00;
  }
  :global(*) {
    box-sizing: border-box;
    margin: 0;
    padding: 0;
  }
  :global(body) {
    font-family: 'Hanken Grotesk', system-ui, sans-serif;
    color: var(--ink);
    background: var(--cream);
    background-image:
      radial-gradient(circle at 100% 0%, rgba(233, 167, 68, 0.12), transparent 45%),
      radial-gradient(circle at 0% 100%, rgba(92, 107, 74, 0.08), transparent 40%);
    min-height: 100vh;
  }
  :global(h1),
  :global(h2),
  :global(h3) {
    font-family: 'Fraunces', serif;
    letter-spacing: -0.01em;
  }
</style>
