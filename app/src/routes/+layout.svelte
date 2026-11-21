<script lang="ts">
  import { onMount } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { page } from '$app/state';
  import { initTheme } from '#lib/theme.js';
  import { pageTitle } from '#lib/titles.js';
  import { watchForUpdates } from '#lib/update.js';
  import { setupI18n } from '#lib/i18n/index.js';
  import { isLoading } from 'svelte-i18n';
  import StorageNotice from '#lib/components/sync/StorageNotice.svelte';
  setupI18n();
  let { children } = $props();

  onMount(() => {
    const stopTheme = initTheme();
    // Notices a new app version; the banner itself is <UpdatePrompt />.
    const stopUpdates = watchForUpdates();
    return () => {
      stopTheme();
      stopUpdates();
    };
  });

  // Screen readers, hyphenation and spell checking follow the UI language.
  $effect(() => {
    if ($locale) document.documentElement.lang = $locale;
  });

  // One translated title per route; see lib/titles.ts.
  const title = $derived($isLoading ? 'Openbeehive' : pageTitle(page.route.id, $_));
</script>

<svelte:head>
  <title>{title}</title>
</svelte:head>

{#if !$isLoading}
  <StorageNotice />
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

  /* Semantic tokens: components use these, never raw colours, so a theme
     only has to redefine this block. Contrast is checked by
     lib/theme-tokens.test.ts (WCAG AA for text, 3:1 for control borders). */
  :global(:root) {
    color-scheme: light;
    --bg: #fbf6ea;
    --surface: #fffdf7;
    --text: #2c2316;
    --text-soft: #6b5e48;
    --border: #e5dcc6;
    --border-strong: #8f8270; /* form controls */
    --accent: #a4641a; /* button backgrounds, focus rings, active markers */
    --on-accent: #ffffff;
    --accent-text: #8a5210; /* accent-coloured text on --bg / --surface */
    --danger: #b3261e;
    --on-danger: #ffffff;
    --ok: #5c6b4a;
    --warn-bg: #f6e3b4;
    --warn-text: #5a3d00;
    --shadow: rgb(44 35 22 / 0.25);
  }
  /* data-theme is set by static/theme-init.js before the first paint and
     kept up to date by lib/theme.ts. */
  :global(:root[data-theme='dark']) {
    color-scheme: dark;
    --bg: #1a1510;
    --surface: #251e16;
    --text: #f3eadb;
    --text-soft: #b9ab95;
    --border: #3f3528;
    --border-strong: #8c7d68;
    --accent: #e9a744;
    --on-accent: #2c2316;
    --accent-text: #edb45c;
    --danger: #f28b82;
    --on-danger: #3b0b07;
    --ok: #a9bd8f;
    --warn-bg: #4a3a12;
    --warn-text: #f6e3b4;
    --shadow: rgb(0 0 0 / 0.5);
  }
  :global(*) {
    box-sizing: border-box;
    margin: 0;
    padding: 0;
  }
  :global(.app-root) {
    display: contents;
  }
  /* SvelteKit hides its route announcer with an inline style attribute, which
     a strict Content-Security-Policy blocks; these rules hide it regardless. */
  :global(#svelte-announcer) {
    position: absolute;
    left: 0;
    top: 0;
    clip: rect(0 0 0 0);
    clip-path: inset(50%);
    overflow: hidden;
    white-space: nowrap;
    width: 1px;
    height: 1px;
  }
  :global(body) {
    font-family: 'Hanken Grotesk', system-ui, sans-serif;
    color: var(--text);
    background: var(--bg);
    background-image:
      radial-gradient(
        circle at 100% 0%,
        color-mix(in srgb, var(--accent) 10%, transparent),
        transparent 45%
      ),
      radial-gradient(
        circle at 0% 100%,
        color-mix(in srgb, var(--ok) 8%, transparent),
        transparent 40%
      );
    min-height: 100vh;
  }
  :global(:focus-visible) {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  /* People who ask for less motion get no looping or sliding animation. */
  @media (prefers-reduced-motion: reduce) {
    :global(*),
    :global(*::before),
    :global(*::after) {
      animation-duration: 0.01ms !important;
      animation-iteration-count: 1 !important;
      transition-duration: 0.01ms !important;
      scroll-behavior: auto !important;
    }
  }
  :global(h1),
  :global(h2),
  :global(h3) {
    font-family: 'Fraunces', serif;
    letter-spacing: -0.01em;
  }
</style>
