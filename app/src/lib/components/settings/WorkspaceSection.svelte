<script lang="ts">
  // The account's workspaces: switch, create, and manage the active one
  // (members, invites). Deleting it is offered at the end of the Settings
  // group, after everything else.
  import { _ } from 'svelte-i18n';
  import { call, openWorkspace, type SessionPayload } from '#lib/account.js';
  import MembersSection from './MembersSection.svelte';
  import InvitesSection from './InvitesSection.svelte';
  import type { Workspace } from './members.js';
  import './settings.css';

  let {
    workspaces,
    activeOrg,
    readOnly = false
  }: { workspaces: Workspace[]; activeOrg: string; readOnly?: boolean } = $props();

  const active = $derived(workspaces.find((w) => w.id === activeOrg));
  const isOwner = $derived(active?.role === 'owner');

  let newName = $state('');
  let busy = $state(false);
  let errorKey = $state('');

  // Both answer with a session for the workspace to continue in.
  async function enter(path: string, body: Record<string, string>) {
    if (busy) return;
    busy = true;
    errorKey = '';
    const r = await call<SessionPayload>(path, body);
    busy = false;
    if (r.ok) openWorkspace(r.body);
    else errorKey = r.errorKey;
  }

  function switchTo(id: string) {
    if (id !== activeOrg) void enter('/auth/switch', { org_id: id });
  }

  function create(e: Event) {
    e.preventDefault();
    const name = newName.trim();
    if (name) void enter('/tenants/create', { name });
  }
</script>

<section class="st-card">
  <h2>{$_('tenant.title')}</h2>
  <p class="st-help">{$_('tenant.help')}</p>
  <div class="workspaces">
    {#each workspaces as w (w.id)}
      <button
        type="button"
        class="workspace"
        class:on={w.id === activeOrg}
        aria-pressed={w.id === activeOrg}
        disabled={busy}
        onclick={() => switchTo(w.id)}
      >
        <span class="name">{w.name}</span>
        <span class="st-badge"
          >{w.role === 'owner' ? $_('tenant.role_owner') : $_('tenant.role_member')}</span
        >
        {#if w.id === activeOrg}<span class="st-badge accent">{$_('tenant.active')}</span>{/if}
      </button>
    {/each}
  </div>

  {#if !readOnly}
    <form class="st-row" onsubmit={create}>
      <label class="st-field">
        <span>{$_('tenant.create_label')}</span>
        <input bind:value={newName} placeholder={$_('tenant.create_ph')} disabled={busy} />
      </label>
      <button type="submit" class="st-btn" disabled={busy || !newName.trim()}>
        + {$_('tenant.create')}
      </button>
    </form>
  {/if}
  {#if errorKey}<p class="st-msg err" role="alert">{$_(errorKey)}</p>{/if}

  {#if active}
    <MembersSection orgId={active.id} workspaceName={active.name} {readOnly} />
    {#if isOwner && !readOnly}
      <InvitesSection orgId={active.id} />
    {/if}
  {/if}
</section>

<style>
  .workspaces {
    display: grid;
    gap: 8px;
    margin-bottom: 12px;
  }
  .workspace {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 48px;
    padding: 11px 14px;
    cursor: pointer;
    border: 1px solid var(--border-strong);
    border-radius: 12px;
    background: var(--bg);
    font: inherit;
    color: var(--text);
    text-align: left;
  }
  .workspace.on {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 10%, var(--surface));
  }
  .name {
    font-weight: 700;
    flex: 1;
    overflow-wrap: anywhere;
  }
</style>
