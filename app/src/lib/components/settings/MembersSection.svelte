<script lang="ts">
  // Who belongs to the workspace. Admins see addresses and roles, change
  // roles and remove members; everyone can leave.
  import { _ } from 'svelte-i18n';
  import { call, openWorkspace, type SessionPayload } from '#lib/account.js';
  import { memberLabel, type Member } from './members.js';
  import './settings.css';

  let {
    orgId,
    workspaceName,
    readOnly = false
  }: { orgId: string; workspaceName: string; readOnly?: boolean } = $props();

  let members = $state<Member[]>([]);
  let canManage = $state(false);
  let busy = $state(false);
  let errorKey = $state('');

  async function load() {
    const r = await call<{ members?: Member[]; can_manage?: boolean }>(
      `/tenants/members?org_id=${encodeURIComponent(orgId)}`
    );
    if (!r.ok) return;
    members = r.body.members ?? [];
    canManage = !!r.body.can_manage && !readOnly;
  }
  $effect(() => {
    orgId;
    void load();
  });

  // Runs one change and reloads the list; a refusal shows its reason.
  async function change(path: string, body: Record<string, string>) {
    if (busy) return null;
    busy = true;
    errorKey = '';
    const r = await call<SessionPayload>(path, { org_id: orgId, ...body });
    busy = false;
    if (!r.ok) errorKey = r.errorKey;
    await load();
    return r.ok ? r.body : null;
  }

  const name = (m: Member) => memberLabel(m, $_('members.unnamed'));

  function setRole(m: Member, role: string) {
    if (role !== m.role) void change('/tenants/members/role', { user_id: m.user_id, role });
  }

  function remove(m: Member) {
    if (!confirm($_('members.remove_confirm', { values: { name: name(m) } }))) return;
    void change('/tenants/members/remove', { user_id: m.user_id });
  }

  async function leave() {
    if (!confirm($_('members.leave_confirm', { values: { name: workspaceName } }))) return;
    const next = await change('/tenants/leave', {});
    if (next) openWorkspace(next);
  }
</script>

<h3 class="st-sub">{$_('members.title')}</h3>
<p class="st-help">{$_(canManage ? 'members.help_owner' : 'members.help_member')}</p>
<ul class="st-list">
  {#each members as m (m.user_id)}
    <li>
      <div class="st-meta">
        <strong>{name(m)}</strong>
        {#if m.email && m.email !== name(m)}<span class="st-date">{m.email}</span>{/if}
        {#if m.me}<span class="st-badge accent">{$_('members.you')}</span>{/if}
        {#if m.role && !(canManage && !m.me)}
          <span class="st-badge">
            {m.role === 'owner' ? $_('tenant.role_owner') : $_('tenant.role_member')}
          </span>
        {/if}
      </div>
      <div class="st-actions">
        <!-- Alone in a workspace there is nothing to leave: it is deleted instead. -->
        {#if m.me && !readOnly && members.length > 1}
          <button type="button" class="st-btn danger" disabled={busy} onclick={leave}>
            {$_('members.leave')}
          </button>
        {:else if canManage}
          <label class="st-field narrow">
            <span>{$_('members.role')}</span>
            <select
              value={m.role}
              disabled={busy}
              onchange={(e) => setRole(m, e.currentTarget.value)}
            >
              <option value="owner">{$_('tenant.role_owner')}</option>
              <option value="member">{$_('tenant.role_member')}</option>
            </select>
          </label>
          <button type="button" class="st-btn danger" disabled={busy} onclick={() => remove(m)}>
            {$_('members.remove')}
          </button>
        {/if}
      </div>
    </li>
  {/each}
</ul>
{#if errorKey}<p class="st-msg err" role="alert">{$_(errorKey)}</p>{/if}
