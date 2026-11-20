<script lang="ts">
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { call, type AccountInfo } from '#lib/account.js';
  import ThemeSwitch from '#lib/components/ThemeSwitch.svelte';
  import SyncProblems from '#lib/components/sync/SyncProblems.svelte';
  import LanguageSection from '#lib/components/settings/LanguageSection.svelte';
  import UnitsSection from '#lib/components/settings/UnitsSection.svelte';
  import WorkspaceSection from '#lib/components/settings/WorkspaceSection.svelte';
  import DataSection from '#lib/components/settings/DataSection.svelte';
  import AccountSection from '#lib/components/settings/AccountSection.svelte';
  import PasskeysSection from '#lib/components/settings/PasskeysSection.svelte';
  import ApiKeysSection from '#lib/components/settings/ApiKeysSection.svelte';
  import AdminResetTool from '#lib/components/settings/AdminResetTool.svelte';
  import DeleteAccount from '#lib/components/settings/DeleteAccount.svelte';
  import type { Workspace } from '#lib/components/settings/members.js';
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

  let email = $state(localStorage.getItem('obh.email') ?? 'local@openbeehive');
  // Without a login (single-user mode) there is no account to manage.
  let signedIn = $state(false);
  let me = $state<Me>({});
  let account = $state<AccountInfo | null>(null);

  const workspaces = $derived(me.tenants ?? []);
  const activeOrg = $derived(me.active_org ?? '');
  const isOwner = $derived(workspaces.find((w) => w.id === activeOrg)?.role === 'owner');
  // The demo account is shared and read-only: nothing about it can be changed.
  const isDemo = $derived(!!me.is_demo);

  onMount(async () => {
    const r = await call<Me>('/auth/me');
    signedIn = r.ok;
    if (!r.ok) return;
    me = r.body;
    if (r.body.email) email = r.body.email;
    const info = await call<AccountInfo>('/auth/account');
    if (info.ok) account = info.body;
  });
</script>

<div class="st-page">
  <h1>{$_('settings.title')}</h1>

  <LanguageSection />

  <section class="st-card">
    <h2>{$_('theme.title')}</h2>
    <p class="st-help">{$_('theme.help')}</p>
    <ThemeSwitch />
  </section>

  <UnitsSection />

  {#if workspaces.length}
    <WorkspaceSection {workspaces} {activeOrg} readOnly={isDemo} />
  {/if}

  <section class="st-card">
    <SyncProblems />
  </section>

  <DataSection />

  <AccountSection {email} orgId={activeOrg} {signedIn} {account} readOnly={isDemo} />

  {#if signedIn && !isDemo}
    <PasskeysSection />
    <ApiKeysSection {isOwner} />
    {#if me.instance_admin}
      <AdminResetTool />
    {/if}
    {#if account}
      <DeleteAccount {account} orgId={activeOrg} />
    {/if}
  {/if}

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
</div>

<style>
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
