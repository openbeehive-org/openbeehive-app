<script lang="ts">
  // Change the password of the signed-in account. Needs the current one;
  // afterwards this device is the only one still signed in.
  import { _ } from 'svelte-i18n';
  import { call, newPasswordProblem, saveSession, type SessionPayload } from '#lib/account.js';
  import './settings.css';

  let { email }: { email: string } = $props();

  let current = $state('');
  let next = $state('');
  let repeat = $state('');
  let busy = $state(false);
  let errorKey = $state('');
  let done = $state(false);

  async function submit(e: Event) {
    e.preventDefault();
    if (busy) return;
    done = false;
    errorKey = newPasswordProblem(next, repeat);
    if (errorKey) return;
    busy = true;
    const r = await call<SessionPayload>('/auth/password/change', {
      current_password: current,
      new_password: next
    });
    busy = false;
    if (!r.ok) {
      errorKey = r.errorKey;
      return;
    }
    // The old session ended with all the others; continue with the new one.
    saveSession(r.body);
    current = next = repeat = '';
    done = true;
  }
</script>

<form class="st-stack" onsubmit={submit}>
  <!-- Lets password managers file the new password under the right account. -->
  <input
    class="st-visually-hidden"
    type="text"
    autocomplete="username"
    value={email}
    readonly
    tabindex="-1"
    aria-hidden="true"
  />
  <label class="st-field">
    <span>{$_('account.password_current')}</span>
    <input type="password" bind:value={current} autocomplete="current-password" required />
  </label>
  <label class="st-field">
    <span>{$_('account.password_new')}</span>
    <input type="password" bind:value={next} autocomplete="new-password" minlength="8" required />
  </label>
  <label class="st-field">
    <span>{$_('account.password_repeat')}</span>
    <input type="password" bind:value={repeat} autocomplete="new-password" required />
  </label>
  <div>
    <button type="submit" class="st-btn" disabled={busy}>{$_('account.password_change')}</button>
  </div>
  {#if errorKey}<p class="st-msg err" role="alert">{$_(errorKey)}</p>{/if}
  {#if done}<p class="st-msg" role="status">{$_('account.password_changed')}</p>{/if}
</form>
