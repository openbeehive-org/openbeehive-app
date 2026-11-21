<script lang="ts">
  // Scan target. A hive QR points here (`/h/<id>`). The hive is looked up in
  // the local database and opened. When it is not on this device, the page
  // finds out why (offline, not signed in, server unreachable, or simply not
  // part of this workspace) and says what to do about it.
  import { onMount } from 'svelte';
  import { get } from 'svelte/store';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { _ } from 'svelte-i18n';
  import { hives } from '#lib/local/repo.js';
  import { syncOnce } from '#lib/local/sync.js';
  import { dataVersion, initialSyncPending } from '#lib/local/live.js';
  import { rememberScanTarget } from '#lib/qr.js';

  type View = 'resolving' | 'offline' | 'signed_out' | 'unreachable' | 'missing';
  let view = $state<View>('resolving');
  const id = page.params.id ?? '';
  const api = (import.meta.env.BEEHIVE_API_URL ?? '').replace(/\/$/, '');

  // Opens the hive if this device has it.
  async function open(): Promise<boolean> {
    if (!(await hives.get(id))) return false;
    await goto(`/hives/${id}`, { replace: true });
    return true;
  }

  // Whether the server can be reached and would let this browser sync.
  async function session(): Promise<'ok' | 'signed_out' | 'unreachable'> {
    const token = localStorage.getItem('session');
    try {
      const [instance, me] = await Promise.all([
        fetch(`${api}/auth/instance`, { credentials: 'include' }),
        fetch(`${api}/auth/me`, {
          credentials: 'include',
          headers: token ? { Authorization: `Bearer ${token}` } : {}
        })
      ]);
      if (me.ok) return 'ok';
      if (!instance.ok) return 'unreachable';
      const i = await instance.json();
      // An instance without any sign-in method is single-user: nothing to sign in to.
      const loginNeeded = !!(i.password_auth || i.oidc_providers?.length || i.webauthn || i.demo);
      return loginNeeded ? 'signed_out' : 'ok';
    } catch {
      return 'unreachable';
    }
  }

  // Resolves when this device's first download has finished (or given up).
  function firstSyncSettled(timeoutMs: number): Promise<void> {
    return new Promise((resolve) => {
      let unsubscribe = () => {};
      const done = () => {
        clearTimeout(timer);
        queueMicrotask(() => unsubscribe());
        resolve();
      };
      const timer = setTimeout(done, timeoutMs);
      unsubscribe = initialSyncPending.subscribe((pending) => {
        if (!pending) done();
      });
    });
  }

  async function lookUp() {
    view = 'resolving';
    try {
      if (await open()) return;
      if (!navigator.onLine) {
        view = 'offline';
        return;
      }
      const s = await session();
      if (s !== 'ok') {
        view = s;
        return;
      }
      await syncOnce(); // the hive may exist on the server but not be pulled yet
      if (await open()) return;
      if (get(initialSyncPending)) {
        // A sync started elsewhere is still downloading this device's first copy.
        await firstSyncSettled(20_000);
        if (await open()) return;
      }
      view = 'missing';
    } catch {
      view = 'missing';
    }
  }

  onMount(() => {
    void lookUp();
    const retry = () => void lookUp();
    window.addEventListener('online', retry);
    return () => window.removeEventListener('online', retry);
  });

  // A later sync batch can still deliver the hive: open it as soon as it lands.
  $effect(() => {
    $dataVersion;
    if (view !== 'resolving') void open();
  });
</script>

<svelte:head><title>{$_('qr.opening')}</title></svelte:head>

<div class="wrap">
  {#if view === 'resolving'}
    <div class="spin" aria-hidden="true">⬡</div>
    <p role="status">{$_('qr.opening')}</p>
  {:else}
    <h1>{$_(`qr.${view}_title`)}</h1>
    <p>{$_(`qr.${view}_text`)}</p>
    {#if view === 'signed_out' || view === 'missing'}
      <p class="hint">{$_('qr.installed_hint')}</p>
    {/if}
    <div class="actions">
      {#if view === 'signed_out'}
        <a class="btn" href="/login" onclick={() => rememberScanTarget(id)}>{$_('qr.signin')}</a>
      {:else}
        <button class="btn" onclick={lookUp}>{$_('qr.retry')}</button>
        <a class="btn ghost" href="/">{$_('qr.to_overview')}</a>
      {/if}
    </div>
  {/if}
</div>

<style>
  .wrap {
    min-height: 100vh;
    display: grid;
    place-content: center;
    justify-items: center;
    gap: 12px;
    text-align: center;
    padding: 24px;
    color: var(--text);
  }
  .spin {
    font-size: 40px;
    color: var(--accent);
    animation: s 1.4s linear infinite;
  }
  @keyframes s {
    to {
      transform: rotate(360deg);
    }
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.5rem;
  }
  p {
    color: var(--text-soft);
    max-width: 40ch;
    line-height: 1.5;
  }
  .hint {
    font-size: 0.88rem;
    background: var(--warn-bg);
    color: var(--warn-text);
    border-radius: 12px;
    padding: 10px 14px;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: 10px;
    margin-top: 8px;
  }
  .btn {
    display: inline-flex;
    align-items: center;
    min-height: 44px;
    background: var(--accent);
    color: var(--on-accent);
    border: 1px solid var(--accent);
    padding: 10px 20px;
    border-radius: 11px;
    text-decoration: none;
    font: inherit;
    font-weight: 700;
    cursor: pointer;
  }
  .btn.ghost {
    background: transparent;
    border-color: var(--border-strong);
    color: var(--text);
  }
</style>
