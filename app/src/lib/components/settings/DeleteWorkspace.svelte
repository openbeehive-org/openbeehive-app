<script lang="ts">
  // Deleting a workspace removes its data for every member, so the admin
  // types its name before the button works.
  import { _ } from 'svelte-i18n';
  import { call, openWorkspace, type SessionPayload } from '#lib/account.js';
  import { namesMatch } from './members.js';
  import './settings.css';

  let { workspace }: { workspace: { id: string; name: string } } = $props();

  let typed = $state('');
  let busy = $state(false);
  let errorKey = $state('');
  const confirmed = $derived(namesMatch(typed, workspace.name));

  async function remove(e: Event) {
    e.preventDefault();
    if (!confirmed || busy) return;
    busy = true;
    errorKey = '';
    const r = await call<SessionPayload>('/tenants/delete', { org_id: workspace.id });
    busy = false;
    if (r.ok) openWorkspace(r.body);
    else errorKey = r.errorKey;
  }
</script>

<form class="st-danger-zone" onsubmit={remove}>
  <h3 class="st-sub">{$_('tenant.danger')}</h3>
  <p class="st-help">{$_('tenant.delete_help', { values: { name: workspace.name } })}</p>
  <div class="st-row">
    <label class="st-field">
      <span>{$_('tenant.delete_type_name', { values: { name: workspace.name } })}</span>
      <input bind:value={typed} autocomplete="off" autocapitalize="none" spellcheck="false" />
    </label>
    <button type="submit" class="st-btn danger-solid" disabled={!confirmed || busy}>
      {$_('tenant.delete')}
    </button>
  </div>
  {#if errorKey}<p class="st-msg err" role="alert">{$_(errorKey)}</p>{/if}
</form>
