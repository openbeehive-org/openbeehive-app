<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { page } from '$app/state';
  import { call, type AccountInfo } from '#lib/account.js';
  import { fetchInstance, loginConfigured } from '#lib/instance.js';
  import ThemeSwitch from '#lib/components/ThemeSwitch.svelte';
  import SyncProblems from '#lib/components/sync/SyncProblems.svelte';
  import LanguageSection from '#lib/components/settings/LanguageSection.svelte';
  import UnitsSection from '#lib/components/settings/UnitsSection.svelte';
  import WorkspaceSection from '#lib/components/settings/WorkspaceSection.svelte';
  import DeleteWorkspace from '#lib/components/settings/DeleteWorkspace.svelte';
  import DataSection from '#lib/components/settings/DataSection.svelte';
  import AccountSection from '#lib/components/settings/AccountSection.svelte';
  import PasskeysSection from '#lib/components/settings/PasskeysSection.svelte';
  import ApiKeysSection from '#lib/components/settings/ApiKeysSection.svelte';
  import AdminResetTool from '#lib/components/settings/AdminResetTool.svelte';
  import DeleteAccount from '#lib/components/settings/DeleteAccount.svelte';
  import type { Workspace } from '#lib/components/settings/members.js';
  import { anchorOf, groupFor, groupHref, type Group } from '#lib/components/settings/groups.js';
  import '#lib/components/settings/settings.css';

  // Legal pages live on the separate marketing site (e.g. openbeehive.org).
  // Link to them only when its base URL is configured (central deployments).
  const marketingUrl = (import.meta.env.BEEHIVE_MARKETING_URL ?? '').replace(/\/$/, '');

  type Me = {
    email?: string;
    active_org?: string;
    instance_admin?: boolean;
    is_demo?: boolean;
    tenants?: Workspace[];
  };

  let email = $state(localStorage.getItem('obh.email') ?? '');
  // Without a login (single-user mode) there is no account to manage.
  let signedIn = $state(false);
  // The server has no sign-in at all; false while unknown (e.g. offline).
  let noLogin = $state(false);
  let me = $state<Me>({});
  let account = $state<AccountInfo | null>(null);

  const workspaces = $derived(me.tenants ?? []);
  const activeOrg = $derived(me.active_org ?? '');
  const active = $derived(workspaces.find((w) => w.id === activeOrg));
  const isOwner = $derived(active?.role === 'owner');
  // The demo account is shared and read-only: nothing about it can be changed.
  const isDemo = $derived(!!me.is_demo);

  // The groups this account has, in order. Workspaces exist only with a
  // login; a server without one has no account either.
  const groups = $derived<Group[]>([
    'preferences',
    ...(workspaces.length ? (['workspace'] as const) : []),
    'data',
    ...(noLogin ? [] : (['account'] as const))
  ]);
  const group = $derived(groupFor(page.url, groups));

  onMount(async () => {
    const [r, instance] = await Promise.all([call<Me>('/auth/me'), fetchInstance()]);
    noLogin = !!instance && !loginConfigured(instance);
    signedIn = r.ok && !noLogin;
    if (!r.ok) return;
    me = r.body;
    if (r.body.email) email = r.body.email;
    if (noLogin) return;
    const info = await call<AccountInfo>('/auth/account');
    if (info.ok) account = info.body;
  });

  // A link to a section (/settings#sync-problems) opens its group, then
  // scrolls to the section and moves focus there, so keyboard and screen
  // reader users land on it too. Runs again when the section appears later
  // (groups that wait for the account details).
  $effect(() => {
    const id = anchorOf(page.url);
    void [group, signedIn, workspaces.length];
    if (!id) return;
    void tick().then(() => {
      const target = document.getElementById(id);
      if (!target) return;
      target.scrollIntoView({ block: 'start' });
      if (target.tabIndex === -1) target.focus({ preventScroll: true });
    });
  });
</script>

<div class="st-page">
  <h1>{$_('settings.title')}</h1>

  <nav class="groups" aria-label={$_('settings.groups')}>
    {#each groups as g (g)}
      <a href={groupHref(g)} aria-current={g === group ? 'page' : undefined}
        >{$_(`settings.group_${g}`)}</a
      >
    {/each}
  </nav>

  {#if group === 'preferences'}
    {#if noLogin}
      <section class="st-card">
        <h2>{$_('settings.no_login_title')}</h2>
        <p class="st-help">{$_('settings.no_login_help')}</p>
      </section>
    {/if}

    <div id="language" tabindex="-1"><LanguageSection /></div>

    <section class="st-card" id="appearance" tabindex="-1">
      <h2>{$_('theme.title')}</h2>
      <p class="st-help">{$_('theme.help')}</p>
      <ThemeSwitch />
    </section>

    <div id="units" tabindex="-1"><UnitsSection /></div>

    {#if marketingUrl}
      <section class="st-card">
        <h2>{$_('footer.legal')}</h2>
        <nav class="legal-links">
          <a href={`${marketingUrl}/about`}>{$_('footer.about')} ›</a>
          <a href={`${marketingUrl}/imprint`}>{$_('footer.imprint')} ›</a>
          <a href={`${marketingUrl}/privacy`}>{$_('footer.privacy')} ›</a>
        </nav>
      </section>
    {/if}
  {:else if group === 'workspace'}
    <div id="workspaces" tabindex="-1">
      <WorkspaceSection {workspaces} {activeOrg} readOnly={isDemo} />
    </div>
    {#if signedIn && !isDemo}
      <div id="api-keys" tabindex="-1"><ApiKeysSection {isOwner} /></div>
    {/if}
    <!-- Destructive actions come last. -->
    {#if active && isOwner && !isDemo}
      <section class="st-card" id="delete-workspace" tabindex="-1">
        <DeleteWorkspace workspace={active} />
      </section>
    {/if}
  {:else if group === 'data'}
    <section class="st-card" id="sync-problems" tabindex="-1">
      <SyncProblems />
    </section>

    <div id="data-export" tabindex="-1">
      <DataSection workspaceName={active?.name ?? ''} readOnly={isDemo} apiKeys={!noLogin} />
    </div>
    <!-- Workspace-wide records (such as a treatment register) go here. -->
  {:else if group === 'account'}
    <div id="account" tabindex="-1">
      <AccountSection {email} orgId={activeOrg} {signedIn} {account} readOnly={isDemo} />
    </div>
    {#if signedIn && !isDemo}
      <div id="passkeys" tabindex="-1"><PasskeysSection /></div>
      {#if me.instance_admin}
        <AdminResetTool />
      {/if}
      <!-- Destructive actions come last. -->
      {#if account}
        <DeleteAccount {account} orgId={activeOrg} />
      {/if}
    {/if}
  {/if}
</div>

<style>
  /* One link per group; two rows of two on narrow phones. */
  .groups {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 6px;
    margin-bottom: 18px;
  }
  @media (min-width: 520px) {
    .groups {
      grid-template-columns: repeat(4, 1fr);
    }
  }
  .groups a {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 44px;
    padding: 6px 10px;
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    background: var(--surface);
    color: var(--text);
    font-weight: 600;
    text-align: center;
    text-decoration: none;
    overflow-wrap: anywhere;
  }
  .groups a[aria-current='page'] {
    border-color: var(--accent);
    background: var(--accent);
    color: var(--on-accent);
  }
  /* Anchor targets take focus from a link but draw no ring for it. */
  [tabindex='-1']:focus {
    outline: none;
  }
  .legal-links {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .legal-links a {
    display: flex;
    align-items: center;
    min-height: 44px;
    padding: 0 2px;
    text-decoration: none;
    color: var(--text);
    font-weight: 600;
    border-top: 1px solid var(--border);
  }
</style>
