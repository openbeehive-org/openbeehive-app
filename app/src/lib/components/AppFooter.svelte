<script lang="ts">
  // Which build of the app this is and where to find the docs, the source and
  // the project's homepage. Shown at the end of every app page and on the
  // sign-in pages.
  import { _, locale } from 'svelte-i18n';
  import {
    buildInfo,
    commitUrl,
    formatBuildTime,
    links,
    releaseUrl,
    shortCommit
  } from '#lib/build.js';

  const version = buildInfo.version;
  const versionHref = releaseUrl(links.source, version);
  const commit = shortCommit(buildInfo.commit);
  const commitHref = commitUrl(links.source, buildInfo.commit);
  const builtAt = $derived(formatBuildTime(buildInfo.builtAt, $locale));
  const homepageLabel = links.homepage.replace(/^https?:\/\//, '');
</script>

<footer class="app-footer">
  <p class="build">
    <span class="item name">Openbeehive</span>
    {#if version}
      <span class="item">
        <span class="sep" aria-hidden="true">·</span>{#if versionHref}<a
            href={versionHref}
            rel="noopener"
            target="_blank"
          >
            {$_('footer.version', { values: { version } })}
          </a>{:else}{$_('footer.version', { values: { version } })}{/if}
      </span>
    {/if}
    {#if commit}
      <span class="item">
        <span class="sep" aria-hidden="true">·</span><a
          href={commitHref}
          rel="noopener"
          target="_blank"
        >
          {$_('footer.build', { values: { commit } })}
        </a>
      </span>
    {/if}
    {#if builtAt}
      <span class="item">
        <span class="sep" aria-hidden="true">·</span>{$_('footer.built', {
          values: { date: builtAt }
        })}
      </span>
    {/if}
  </p>
  <nav class="links" aria-label={$_('footer.app_info')}>
    <a href={links.docs} rel="noopener" target="_blank">{$_('footer.docs')}</a>
    <a href={links.source} rel="noopener" target="_blank">{$_('footer.source')}</a>
    <a href={links.homepage} rel="noopener" target="_blank" title={$_('footer.website')}>
      {homepageLabel}
    </a>
  </nav>
</footer>

<style>
  .app-footer {
    margin-top: 32px;
    padding: 16px 16px 20px;
    border-top: 1px solid var(--border);
    color: var(--text-soft);
    font-size: 0.82rem;
    text-align: center;
  }
  .build {
    margin: 0;
    line-height: 1.6;
  }
  /* Each part keeps its separator, so a wrapped line never ends in a dot. */
  .item {
    white-space: nowrap;
  }
  .name {
    font-weight: 700;
    color: var(--text);
  }
  .sep {
    margin: 0 6px;
  }
  .links {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    column-gap: 18px;
    margin-top: 4px;
  }
  a {
    color: var(--accent-text);
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  /* Comfortable tap targets for the project links on phones. */
  .links a {
    display: inline-flex;
    align-items: center;
    min-height: 44px;
  }
</style>
