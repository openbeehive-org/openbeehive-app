<script lang="ts">
  import { onMount } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { Check, KeyRound, Printer, Settings } from '@lucide/svelte';
  import { changeLanguage, SUPPORTED, LANG_LABELS, type Lang } from '#lib/i18n/index.js';
  import {
    exportJSON,
    exportXLSX,
    exportCSVZip,
    exportBeeXML,
    printReport,
    importFile,
    type ImportMode
  } from '#lib/data/portability.js';
  import {
    enrollPasskey,
    listPasskeys,
    deletePasskey,
    passkeysSupported,
    type PasskeyInfo
  } from '#lib/webauthn.js';
  import {
    listApiKeys,
    createApiKey,
    deleteApiKey,
    type ApiKeyInfo,
    type ApiKeyScope
  } from '#lib/apikeys.js';

  const API = (import.meta.env.BEEHIVE_API_URL ?? '').replace(/\/$/, '');

  // Legal pages live on the separate marketing site (e.g. openbeehive.org).
  // Link to them only when its base URL is configured (central deployments).
  const marketingUrl = (import.meta.env.BEEHIVE_MARKETING_URL ?? '').replace(/\/$/, '');
  const showLegal = !!marketingUrl;

  const LABELS = LANG_LABELS;

  let email = $state(localStorage.getItem('obh.email') ?? 'local@openbeehive');

  function pick(l: Lang) {
    changeLanguage(l);
  }

  // --- Tenants (multi-tenant: switch / create / invite) ---
  type Tenant = { id: string; name: string; role: string };
  let tenants = $state<Tenant[]>([]);
  let activeOrg = $state('');
  let newTenant = $state('');
  let inviteEmail = $state('');
  let tenantMsg = $state('');
  type OpenInvite = { id: string; email: string; role: string; created_at: string; link: string };
  let openInvites = $state<OpenInvite[]>([]);
  let inviteLink = $state('');
  let copied = $state('');
  let signedIn = $state(false);
  const activeTenant = $derived(tenants.find((t) => t.id === activeOrg));
  const isOwner = $derived(activeTenant?.role === 'owner');

  const authHeaders = (): Record<string, string> => {
    const h: Record<string, string> = { 'Content-Type': 'application/json' };
    const t = localStorage.getItem('session');
    if (t) h.Authorization = `Bearer ${t}`;
    return h;
  };

  async function loadTenants() {
    try {
      const r = await fetch(`${API}/auth/me`, { credentials: 'include', headers: authHeaders() });
      signedIn = r.ok;
      if (!r.ok) return;
      const j = await r.json();
      tenants = j.tenants ?? [];
      activeOrg = j.active_org ?? '';
      await loadInvites();
      await loadPasskeys();
      await loadApiKeys();
    } catch {
      /* single-user mode: no tenants */
    }
  }
  onMount(loadTenants);

  // --- Passkeys (bound to the signed-in account) ---
  let passkeys = $state<PasskeyInfo[]>([]);
  let pkName = $state('');
  let pkMsg = $state('');
  let pkErr = $state(false);
  let pkBusy = $state(false);

  async function loadPasskeys() {
    passkeys = await listPasskeys(API, authHeaders());
  }

  async function addPasskey() {
    if (pkBusy) return;
    pkBusy = true;
    pkMsg = '';
    pkErr = false;
    try {
      await enrollPasskey(API, pkName.trim(), authHeaders());
      pkName = '';
      pkMsg = $_('pk.added');
      await loadPasskeys();
    } catch (e) {
      pkErr = true;
      pkMsg = String((e as Error)?.message ?? e);
    } finally {
      pkBusy = false;
    }
  }

  async function removePasskey(id: string) {
    if (!confirm($_('pk.delete_confirm'))) return;
    if (await deletePasskey(API, id, authHeaders())) await loadPasskeys();
  }

  // --- API keys (scripts and integrations, bound to account + active tenant) ---
  let apiKeys = $state<ApiKeyInfo[]>([]);
  let akName = $state('');
  let akScope = $state<ApiKeyScope>('write');
  let akExpiry = $state(0); // days; 0 = never
  let akToken = $state('');
  let akMsg = $state('');
  let akErr = $state(false);
  let akBusy = $state(false);
  let akCopied = $state(false);

  // Tenant admins also see the other members' keys (owner-only endpoint).
  let tenantKeys = $state<ApiKeyInfo[]>([]);

  async function loadApiKeys() {
    apiKeys = await listApiKeys(API, authHeaders());
    tenantKeys = isOwner
      ? (await listApiKeys(API, authHeaders(), true)).filter((k) => !k.mine)
      : [];
  }

  async function addApiKey() {
    if (akBusy) return;
    akBusy = true;
    akMsg = '';
    akErr = false;
    akToken = '';
    akCopied = false;
    try {
      const k = await createApiKey(API, akName.trim(), akScope, akExpiry, authHeaders());
      akToken = k.token;
      akName = '';
      akScope = 'write';
      akExpiry = 0;
      await loadApiKeys();
    } catch (e) {
      akErr = true;
      akMsg = String((e as Error)?.message ?? e);
    } finally {
      akBusy = false;
    }
  }

  const isExpired = (k: ApiKeyInfo) => !!k.expires_at && new Date(k.expires_at) <= new Date();

  async function copyApiKey() {
    try {
      await navigator.clipboard.writeText(akToken);
      akCopied = true;
      setTimeout(() => {
        akCopied = false;
      }, 2000);
    } catch {
      /* clipboard unavailable: the key stays selectable */
    }
  }

  async function removeApiKey(id: string, theirs = false) {
    if (!confirm($_(theirs ? 'apikeys.delete_theirs_confirm' : 'apikeys.delete_confirm'))) return;
    if (await deleteApiKey(API, id, authHeaders())) await loadApiKeys();
  }

  async function loadInvites() {
    // Owner-only endpoint; non-owners get a 403 and simply see no list.
    try {
      const r = await fetch(`${API}/tenants/invites?org_id=${encodeURIComponent(activeOrg)}`, {
        credentials: 'include',
        headers: authHeaders()
      });
      openInvites = r.ok ? ((await r.json()).invites ?? []) : [];
    } catch {
      openInvites = [];
    }
  }

  async function copyLink(link: string) {
    try {
      await navigator.clipboard.writeText(link);
      copied = link;
      setTimeout(() => {
        if (copied === link) copied = '';
      }, 2000);
    } catch {
      /* clipboard unavailable (http, permissions); link stays selectable */
    }
  }

  async function revokeInvite(id: string) {
    const r = await fetch(`${API}/tenants/invite/revoke`, {
      method: 'POST',
      credentials: 'include',
      headers: authHeaders(),
      body: JSON.stringify({ id, org_id: activeOrg })
    });
    if (r.ok) await loadInvites();
  }

  async function deleteTenant() {
    const name = activeTenant?.name ?? '';
    if (!confirm($_('tenant.delete_confirm', { values: { name } }))) return;
    const r = await fetch(`${API}/tenants/delete`, {
      method: 'POST',
      credentials: 'include',
      headers: authHeaders(),
      body: JSON.stringify({ org_id: activeOrg })
    });
    if (r.ok) applyTenant(await r.json().catch(() => ({})));
  }

  async function logout() {
    try {
      await fetch(`${API}/auth/logout`, {
        method: 'POST',
        credentials: 'include',
        headers: authHeaders()
      });
    } catch {
      /* clear client state regardless */
    }
    for (const k of ['session', 'obh.userId', 'obh.orgId', 'obh.email']) localStorage.removeItem(k);
    location.href = '/login';
  }

  // Persist the new session + active tenant, then reload so the per-tenant local
  // store opens and the sync client re-authenticates for the new tenant.
  function applyTenant(j: { token?: string; active_org?: string }) {
    if (j?.token) localStorage.setItem('session', j.token);
    if (j?.active_org) localStorage.setItem('obh.orgId', j.active_org);
    location.reload();
  }

  async function switchTenant(id: string) {
    if (id === activeOrg) return;
    const r = await fetch(`${API}/auth/switch`, {
      method: 'POST',
      credentials: 'include',
      headers: authHeaders(),
      body: JSON.stringify({ org_id: id })
    });
    applyTenant(await r.json().catch(() => ({})));
  }

  async function createTenant() {
    const name = newTenant.trim();
    if (!name) return;
    const r = await fetch(`${API}/tenants/create`, {
      method: 'POST',
      credentials: 'include',
      headers: authHeaders(),
      body: JSON.stringify({ name })
    });
    applyTenant(await r.json().catch(() => ({})));
  }

  async function sendInvite() {
    const email2 = inviteEmail.trim();
    if (!email2) return;
    const r = await fetch(`${API}/tenants/invite`, {
      method: 'POST',
      credentials: 'include',
      headers: authHeaders(),
      body: JSON.stringify({ email: email2, org_id: activeOrg })
    });
    if (r.ok) {
      const j = await r.json().catch(() => ({}));
      inviteLink = j.link ?? '';
      tenantMsg = $_('tenant.invite_sent');
      inviteEmail = '';
      await loadInvites();
    }
  }

  // --- Data export / import ---
  let busy = $state(false);
  let importMode = $state<ImportMode>('auto');
  let importMsg = $state('');
  let importErr = $state(false);

  async function runExport(fn: () => Promise<void>) {
    if (busy) return;
    busy = true;
    importMsg = '';
    importErr = false;
    try {
      await fn();
    } catch (e) {
      importErr = true;
      importMsg = String((e as Error)?.message ?? e);
    } finally {
      busy = false;
    }
  }

  async function onImport(ev: Event) {
    const input = ev.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    busy = true;
    importMsg = $_('data.importing');
    importErr = false;
    try {
      const res = await importFile(file, importMode);
      const total = Object.values(res).reduce((a, b) => a + b, 0);
      importMsg = $_('data.imported', { values: { n: total } });
    } catch (e) {
      importErr = true;
      importMsg = $_('data.import_failed', { values: { msg: String((e as Error)?.message ?? e) } });
    } finally {
      busy = false;
      input.value = '';
    }
  }
</script>

<div class="page">
  <h1>{$_('settings.title')}</h1>

  <section class="card">
    <h2>{$_('settings.language')}</h2>
    <p class="help">{$_('settings.language_help')}</p>
    <div class="langs">
      {#each SUPPORTED as l}
        <button class="lang" class:on={$locale?.startsWith(l)} onclick={() => pick(l)}>
          <span class="code">{l.toUpperCase()}</span>
          <span class="name">{LABELS[l]}</span>
          {#if $locale?.startsWith(l)}<span class="tick"
              ><Check size={16} aria-hidden="true" /></span
            >{/if}
        </button>
      {/each}
    </div>
  </section>

  {#if tenants.length}
    <section class="card">
      <h2>{$_('tenant.title')}</h2>
      <p class="help">{$_('tenant.help')}</p>
      <div class="tenants">
        {#each tenants as t}
          <button class="tenant" class:on={t.id === activeOrg} onclick={() => switchTenant(t.id)}>
            <span class="tname">{t.name}</span>
            <span class="trole"
              >{t.role === 'owner' ? $_('tenant.role_owner') : $_('tenant.role_member')}</span
            >
            {#if t.id === activeOrg}<span class="ttick">{$_('tenant.active')}</span>{/if}
          </button>
        {/each}
      </div>
      <div class="trow">
        <input bind:value={newTenant} placeholder={$_('tenant.create_ph')} />
        <button class="data-btn" onclick={createTenant}>+ {$_('tenant.create')}</button>
      </div>
      {#if isOwner}
        <h3 class="sub">{$_('tenant.invite')}</h3>
        <div class="trow">
          <input type="email" bind:value={inviteEmail} placeholder={$_('tenant.invite_ph')} />
          <button class="data-btn" onclick={sendInvite}>{$_('tenant.invite_send')}</button>
        </div>
        {#if tenantMsg}<p class="import-msg">{tenantMsg}</p>{/if}
        {#if inviteLink}
          <div class="invite-link">
            <code>{inviteLink}</code>
            <button class="data-btn" onclick={() => copyLink(inviteLink)}>
              {copied === inviteLink ? $_('tenant.copied') : $_('tenant.copy_link')}
            </button>
          </div>
        {/if}
        {#if openInvites.length}
          <h3 class="sub">{$_('tenant.open_invites')}</h3>
          <ul class="invites">
            {#each openInvites as i (i.id)}
              <li>
                <div class="imeta">
                  <strong>{i.email}</strong>
                  <span class="trole"
                    >{i.role === 'owner' ? $_('tenant.role_owner') : $_('tenant.role_member')}</span
                  >
                  <span class="idate"
                    >{new Date(i.created_at).toLocaleDateString($locale ?? undefined)}</span
                  >
                </div>
                <div class="iactions">
                  <button class="data-btn" onclick={() => copyLink(i.link)}>
                    {copied === i.link ? $_('tenant.copied') : $_('tenant.copy_link')}
                  </button>
                  <button class="data-btn danger" onclick={() => revokeInvite(i.id)}
                    >{$_('tenant.invite_revoke')}</button
                  >
                </div>
              </li>
            {/each}
          </ul>
        {/if}
        <div class="danger-zone">
          <h3 class="sub">{$_('tenant.danger')}</h3>
          <p class="help">{$_('tenant.delete_help')}</p>
          <button class="data-btn danger" onclick={deleteTenant}>{$_('tenant.delete')}</button>
        </div>
      {/if}
    </section>
  {/if}

  {#if signedIn}
    <section class="card">
      <h2>{$_('pk.title')}</h2>
      <p class="help">{$_('pk.help')}</p>
      {#if passkeys.length}
        <ul class="invites">
          {#each passkeys as k (k.id)}
            <li>
              <div class="imeta">
                <strong
                  ><KeyRound size={16} aria-hidden="true" /> {k.name || $_('pk.unnamed')}</strong
                >
                <span class="idate"
                  >{$_('pk.created')}
                  {new Date(k.created_at).toLocaleDateString($locale ?? undefined)}</span
                >
                {#if k.last_used_at}
                  <span class="idate"
                    >{$_('pk.last_used')}
                    {new Date(k.last_used_at).toLocaleDateString($locale ?? undefined)}</span
                  >
                {/if}
              </div>
              <div class="iactions">
                <button class="data-btn danger" onclick={() => removePasskey(k.id)}
                  >{$_('pk.delete')}</button
                >
              </div>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="help">{$_('pk.none')}</p>
      {/if}
      {#if passkeysSupported()}
        <div class="trow">
          <input bind:value={pkName} placeholder={$_('pk.name_ph')} disabled={pkBusy} />
          <button class="data-btn" onclick={addPasskey} disabled={pkBusy}>+ {$_('pk.add')}</button>
        </div>
      {:else}
        <p class="help">{$_('pk.not_supported')}</p>
      {/if}
      {#if pkMsg}<p class="import-msg" class:err={pkErr}>{pkMsg}</p>{/if}
    </section>
  {/if}

  {#if signedIn}
    <section class="card">
      <h2>{$_('apikeys.title')}</h2>
      <p class="help">{$_('apikeys.help')}</p>
      {#if apiKeys.length}
        <ul class="invites">
          {#each apiKeys as k (k.id)}
            <li>
              <div class="imeta">
                <strong
                  ><Settings size={16} aria-hidden="true" />
                  {k.name || $_('apikeys.unnamed')}</strong
                >
                <code class="akprefix">{k.prefix}...</code>
                <span class="trole"
                  >{k.scope === 'read' ? $_('apikeys.scope_read') : $_('apikeys.scope_write')}</span
                >
                <span class="idate"
                  >{$_('apikeys.created')}
                  {new Date(k.created_at).toLocaleDateString($locale ?? undefined)}</span
                >
                {#if k.expires_at}
                  <span class="idate" class:akexpired={isExpired(k)}
                    >{isExpired(k) ? $_('apikeys.expired') : $_('apikeys.expires')}
                    {new Date(k.expires_at).toLocaleDateString($locale ?? undefined)}</span
                  >
                {/if}
                {#if k.last_used_at}
                  <span class="idate"
                    >{$_('apikeys.last_used')}
                    {new Date(k.last_used_at).toLocaleDateString($locale ?? undefined)}</span
                  >
                {/if}
              </div>
              <div class="iactions">
                <button class="data-btn danger" onclick={() => removeApiKey(k.id)}
                  >{$_('apikeys.delete')}</button
                >
              </div>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="help">{$_('apikeys.none')}</p>
      {/if}
      <div class="trow">
        <input bind:value={akName} placeholder={$_('apikeys.name_ph')} disabled={akBusy} />
        <select bind:value={akScope} disabled={akBusy} aria-label={$_('apikeys.scope_label')}>
          <option value="write">{$_('apikeys.scope_write')}</option>
          <option value="read">{$_('apikeys.scope_read')}</option>
        </select>
        <select bind:value={akExpiry} disabled={akBusy} aria-label={$_('apikeys.expiry_label')}>
          <option value={0}>{$_('apikeys.expiry_never')}</option>
          <option value={30}>{$_('apikeys.expiry_30')}</option>
          <option value={90}>{$_('apikeys.expiry_90')}</option>
          <option value={365}>{$_('apikeys.expiry_365')}</option>
        </select>
        <button class="data-btn" onclick={addApiKey} disabled={akBusy}>+ {$_('apikeys.add')}</button
        >
      </div>
      {#if akToken}
        <div class="aknew">
          <p class="help">{$_('apikeys.new_help')}</p>
          <div class="trow">
            <input
              readonly
              value={akToken}
              onfocus={(e) => (e.currentTarget as HTMLInputElement).select()}
            />
            <button class="data-btn" onclick={copyApiKey}
              >{akCopied ? $_('apikeys.copied') : $_('apikeys.copy')}</button
            >
          </div>
        </div>
      {/if}
      {#if akMsg}<p class="import-msg" class:err={akErr}>{akMsg}</p>{/if}
      {#if isOwner && tenantKeys.length}
        <h3 class="aksub">{$_('apikeys.tenant_title')}</h3>
        <p class="help">{$_('apikeys.tenant_help')}</p>
        <ul class="invites">
          {#each tenantKeys as k (k.id)}
            <li>
              <div class="imeta">
                <strong
                  ><Settings size={16} aria-hidden="true" />
                  {k.name || $_('apikeys.unnamed')}</strong
                >
                <code class="akprefix">{k.prefix}...</code>
                <span class="trole"
                  >{k.scope === 'read' ? $_('apikeys.scope_read') : $_('apikeys.scope_write')}</span
                >
                <span class="idate">{k.user_email}</span>
                {#if k.last_used_at}
                  <span class="idate"
                    >{$_('apikeys.last_used')}
                    {new Date(k.last_used_at).toLocaleDateString($locale ?? undefined)}</span
                  >
                {/if}
              </div>
              <div class="iactions">
                <button class="data-btn danger" onclick={() => removeApiKey(k.id, true)}
                  >{$_('apikeys.delete')}</button
                >
              </div>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  {/if}

  <section class="card">
    <h2>{$_('data.title')}</h2>
    <p class="help">{$_('data.export_help')}</p>
    <div class="data-btns">
      <button class="data-btn" disabled={busy} onclick={() => runExport(exportJSON)}
        >⤓ {$_('data.json')}</button
      >
      <button class="data-btn" disabled={busy} onclick={() => runExport(exportXLSX)}
        >⤓ {$_('data.xlsx')}</button
      >
      <button class="data-btn" disabled={busy} onclick={() => runExport(exportCSVZip)}
        >⤓ {$_('data.csv')}</button
      >
      <button class="data-btn" disabled={busy} onclick={() => runExport(exportBeeXML)}
        >⤓ {$_('data.beexml')}</button
      >
      <button class="data-btn" disabled={busy} onclick={() => runExport(printReport)}
        ><Printer size={16} aria-hidden="true" /> {$_('data.pdf')}</button
      >
    </div>

    <h3 class="sub">{$_('data.import')}</h3>
    <p class="help">{$_('data.import_help')}</p>
    <div class="import-row">
      <select bind:value={importMode} disabled={busy}>
        <option value="auto">{$_('data.mode_auto')}</option>
        <option value="json">{$_('data.mode_json')}</option>
        <option value="beexml">{$_('data.mode_beexml')}</option>
        <option value="csv">{$_('data.mode_csv')}</option>
      </select>
      <label class="data-btn file">
        {$_('data.import_btn')}
        <input
          type="file"
          accept=".json,.xml,.csv,.txt"
          hidden
          disabled={busy}
          onchange={onImport}
        />
      </label>
    </div>
    {#if importMsg}<p class="import-msg" class:err={importErr}>{importMsg}</p>{/if}
  </section>

  <section class="card">
    <h2>{$_('settings.account')}</h2>
    <p class="help">{$_('settings.user')}</p>
    <div class="user">
      <span class="ava">⬡</span>
      <strong>{email}</strong>
    </div>
    {#if signedIn}
      <button class="data-btn logout" onclick={logout}>{$_('settings.logout')}</button>
    {/if}
  </section>

  {#if showLegal}
    <section class="card">
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
  .page {
    max-width: 640px;
    margin: 0 auto;
    padding: 26px 18px 96px;
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.9rem;
    margin-bottom: 18px;
  }
  h2 {
    font-family: 'Fraunces', serif;
    font-size: 1.2rem;
    margin-bottom: 6px;
  }
  .help {
    color: var(--ink-soft);
    font-size: 0.88rem;
    margin-bottom: 14px;
  }
  .card {
    background: var(--cream2);
    border: 1px solid var(--line);
    border-radius: 16px;
    padding: 20px;
    margin-bottom: 16px;
  }

  .langs {
    display: grid;
    gap: 10px;
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  }
  .lang {
    display: flex;
    align-items: center;
    gap: 11px;
    padding: 13px 15px;
    cursor: pointer;
    border: 1px solid var(--line);
    border-radius: 13px;
    background: #fff;
    font: inherit;
    color: var(--ink);
    text-align: left;
  }
  .lang.on {
    border-color: var(--honey);
    background: rgba(199, 127, 34, 0.08);
  }
  .lang .code {
    font-weight: 800;
    font-size: 0.82rem;
    background: var(--cream);
    border: 1px solid var(--line);
    border-radius: 7px;
    padding: 3px 7px;
    color: var(--ink-soft);
  }
  .lang.on .code {
    background: var(--honey);
    color: #fff;
    border-color: var(--honey);
  }
  .lang .name {
    font-weight: 600;
    flex: 1;
  }
  .lang .tick {
    color: var(--honey-d);
    display: inline-flex;
  }
  .page :global(.lucide-icon) {
    vertical-align: -0.15em;
  }

  .sub {
    font-family: 'Fraunces', serif;
    font-size: 1.02rem;
    margin: 20px 0 6px;
  }
  .data-btns {
    display: grid;
    gap: 10px;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  }
  .data-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    padding: 12px 14px;
    cursor: pointer;
    border: 1px solid var(--line);
    border-radius: 12px;
    background: #fff;
    font: inherit;
    font-weight: 600;
    color: var(--ink);
    text-align: center;
  }
  .data-btn:hover:not(:disabled) {
    border-color: var(--honey);
    background: rgba(199, 127, 34, 0.08);
  }
  .data-btn:disabled {
    opacity: 0.55;
    cursor: default;
  }
  .import-row {
    display: flex;
    gap: 10px;
    flex-wrap: wrap;
    align-items: center;
  }
  .import-row select {
    padding: 11px 12px;
    border: 1px solid var(--line);
    border-radius: 12px;
    background: #fff;
    font: inherit;
    color: var(--ink);
    flex: 1;
    min-width: 150px;
  }
  .data-btn.file {
    flex-shrink: 0;
  }
  .import-msg {
    margin-top: 12px;
    font-size: 0.9rem;
    font-weight: 600;
    color: var(--moss);
  }
  .import-msg.err {
    color: #b5402f;
  }

  .tenants {
    display: grid;
    gap: 8px;
    margin-bottom: 12px;
  }
  .tenant {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 11px 14px;
    cursor: pointer;
    border: 1px solid var(--line);
    border-radius: 12px;
    background: #fff;
    font: inherit;
    color: var(--ink);
    text-align: left;
  }
  .tenant.on {
    border-color: var(--honey);
    background: rgba(199, 127, 34, 0.08);
  }
  .tenant .tname {
    font-weight: 700;
    flex: 1;
  }
  .tenant .trole {
    font-size: 0.72rem;
    font-weight: 700;
    color: var(--ink-soft);
    background: var(--cream);
    border: 1px solid var(--line);
    border-radius: 7px;
    padding: 2px 7px;
  }
  .tenant .ttick {
    color: var(--honey-d);
    font-weight: 700;
    font-size: 0.76rem;
  }
  .trow {
    display: flex;
    gap: 10px;
    flex-wrap: wrap;
    margin-top: 6px;
  }
  .trow input {
    flex: 1;
    min-width: 150px;
    padding: 11px 12px;
    border: 1px solid var(--line);
    border-radius: 12px;
    background: #fff;
    font: inherit;
    color: var(--ink);
  }
  .invite-link {
    display: flex;
    gap: 10px;
    flex-wrap: wrap;
    align-items: center;
    margin-top: 10px;
    padding: 10px 12px;
    border: 1px solid var(--honey);
    border-radius: 12px;
    background: rgba(199, 127, 34, 0.08);
  }
  .invite-link code {
    flex: 1;
    min-width: 150px;
    word-break: break-all;
    font-size: 0.82rem;
  }
  .invites {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 8px;
  }
  .invites li {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    padding: 11px 14px;
    border: 1px solid var(--line);
    border-radius: 12px;
    background: #fff;
  }
  .invites .imeta {
    flex: 1;
    min-width: 150px;
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .invites .trole {
    font-size: 0.72rem;
    font-weight: 700;
    color: var(--ink-soft);
    background: var(--cream);
    border: 1px solid var(--line);
    border-radius: 7px;
    padding: 2px 7px;
  }
  .invites .idate {
    font-size: 0.8rem;
    color: var(--ink-soft);
  }
  .akprefix {
    font-size: 0.8rem;
    color: var(--ink-soft);
    background: var(--cream);
    padding: 2px 6px;
    border-radius: 6px;
  }
  .akexpired {
    color: #b5402f;
    font-weight: 700;
  }
  .aksub {
    margin: 18px 0 4px;
    font-size: 1rem;
  }
  .trow select {
    padding: 11px 12px;
    border: 1px solid var(--line);
    border-radius: 12px;
    background: var(--cream2);
    font: inherit;
  }
  .aknew {
    margin-top: 12px;
    padding: 12px 14px;
    border: 1px solid var(--honey);
    border-radius: 12px;
    background: rgba(199, 127, 34, 0.06);
  }
  .aknew input {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.85rem;
  }
  .invites .iactions {
    display: flex;
    gap: 8px;
  }
  .data-btn.danger:hover:not(:disabled) {
    border-color: #b5402f;
    background: rgba(181, 64, 47, 0.08);
    color: #b5402f;
  }
  .danger-zone {
    margin-top: 20px;
    padding: 14px;
    border: 1px solid rgba(181, 64, 47, 0.4);
    border-radius: 12px;
  }
  .danger-zone .sub {
    margin: 0 0 4px;
    color: #b5402f;
  }
  .danger-zone .data-btn {
    border-color: rgba(181, 64, 47, 0.4);
    color: #b5402f;
  }
  .logout {
    margin-top: 14px;
  }
  .logout:hover {
    border-color: #b5402f;
    color: #b5402f;
  }

  .user {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .user .ava {
    font-size: 1.6rem;
    color: var(--honey);
  }
  .legal-links {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .legal-links a {
    padding: 11px 2px;
    text-decoration: none;
    color: var(--ink);
    font-weight: 600;
    border-top: 1px solid var(--line);
  }
  .legal-links a:first-child {
    border-top: none;
  }
  .legal-links a:hover {
    color: var(--honey-d);
  }
</style>
