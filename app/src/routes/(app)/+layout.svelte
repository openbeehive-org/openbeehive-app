<script lang="ts">
  import { _ } from 'svelte-i18n';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { startSync, syncStatus } from '#lib/local/sync.js';
  import { dataVersion, initialSyncPending, readOnlyRefusals } from '#lib/local/live.js';
  import { reconcile } from '#lib/local/userSettings.js';
  import { recoverSession, takeNotice, takeResume } from '#lib/account.js';
  import { startPhotoTransfers } from '#lib/photos/index.js';
  import NavIcon from '#lib/components/NavIcon.svelte';
  import Toasts from '#lib/components/Toasts.svelte';
  import SyncChip from '#lib/components/sync/SyncChip.svelte';
  import { toast } from '#lib/toast.js';
  import { fetchInstance } from '#lib/instance.js';
  import UpdatePrompt from '#lib/components/UpdatePrompt.svelte';
  import { get } from 'svelte/store';

  const api = (import.meta.env.BEEHIVE_API_URL ?? '').replace(/\/$/, '');
  async function fetchJSON(path: string, opts: RequestInit = {}) {
    try {
      const r = await fetch(`${api}${path}`, { credentials: 'include', ...opts });
      return r.ok ? await r.json() : null;
    } catch {
      return null;
    }
  }

  let online = $state(true);
  let started = $state(false);
  let email = $state('local@openbeehive');
  let isDemo = $state(false);
  let { children } = $props();

  const initial = $derived((email[0] ?? 'L').toUpperCase());

  onMount(() => {
    online = navigator.onLine;
    const on = () => (online = true);
    const off = () => (online = false);
    addEventListener('online', on);
    addEventListener('offline', off);

    // Safety net: a failed local write whose promise nobody catches must never
    // die silently (the classic symptom: a button that "does nothing"). Show
    // the error, so the user knows the action did not stick.
    const onRejection = (e: PromiseRejectionEvent) => {
      const msg = String(
        (e.reason as Error)?.message ?? e.reason ?? 'unhandled promise rejection without a message'
      );
      toast(get(_)('common.save_failed', { values: { msg } }));
    };
    addEventListener('unhandledrejection', onRejection);

    // Resolve the session before starting sync. Without this an unauthenticated
    // visitor to a cloud/demo instance would land in an empty shell while the
    // sync loop 401s. (Async work in an IIFE so onMount can still return the
    // listener cleanup synchronously.)
    void (async () => {
      // Both requests are independent, so fetch them in parallel: sync (and
      // with it the first data on a fresh device) starts a round-trip earlier.
      const tok = localStorage.getItem('session');
      const [instRes, me] = await Promise.all([
        fetchInstance(),
        fetchJSON('/auth/me', { headers: tok ? { Authorization: `Bearer ${tok}` } : {} })
      ]);
      const inst = instRes ?? {};
      const authRequired = !!(
        inst.password_auth ||
        inst.oidc_providers?.length ||
        inst.webauthn ||
        inst.demo
      );

      if (authRequired && !me) {
        // Signed in, but the workspace this session was for is gone (removed
        // from it, or it was deleted): continue in another one.
        if (tok && (await recoverSession()) === 'recovered') {
          location.reload();
          return;
        }
        // Pure-showcase host (BEEHIVE_DEMO_AUTOLOGIN): sign in as the demo user
        // automatically so the URL "just works". Combined production+demo
        // instances leave this off: visitors get /login, which carries the
        // demo button. Guarded so a failing demo-login can't loop on reload.
        if (inst.demo_autologin && !sessionStorage.getItem('obh.demoTried')) {
          sessionStorage.setItem('obh.demoTried', '1');
          const j = await fetchJSON('/auth/demo-login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: '{}'
          });
          if (j?.token) {
            localStorage.setItem('session', j.token);
            if (j.user_id) localStorage.setItem('obh.userId', j.user_id);
            if (j.active_org) localStorage.setItem('obh.orgId', j.active_org);
            localStorage.setItem('obh.email', 'demo');
            location.href = '/'; // full reload: opens the tenant store, then sync runs authenticated
            return;
          }
        }
        // Otherwise this instance needs a real login.
        goto('/login');
        return;
      }
      sessionStorage.removeItem('obh.demoTried');
      const notice = takeNotice();
      if (notice) toast(get(_)(notice));
      // A dialog asked for a fresh sign-in; take the user back to it.
      const resume = takeResume();
      if (resume) void goto(resume);

      if (me) {
        // Signed in: mirror the server identity into the local store keys.
        if (me.user_id) localStorage.setItem('obh.userId', me.user_id);
        if (me.active_org) localStorage.setItem('obh.orgId', me.active_org);
        if (me.email) {
          localStorage.setItem('obh.email', me.email);
          email = me.email;
        }
        isDemo = !!me.is_demo;
      } else {
        // Single-user self-host (no auth configured): fixed "local" identity so
        // pushed rows (organization_id = "local") are returned again on Pull.
        if (!localStorage.getItem('obh.userId')) localStorage.setItem('obh.userId', 'local');
        if (!localStorage.getItem('obh.orgId')) localStorage.setItem('obh.orgId', 'local');
        email = localStorage.getItem('obh.email') ?? 'local@openbeehive';
      }

      startSync(); // start background sync once a session (or single-user identity) is established
      startPhotoTransfers(); // and the uploads of photos taken while offline
      started = true;
    })();

    return () => {
      removeEventListener('online', on);
      removeEventListener('offline', off);
      removeEventListener('unhandledrejection', onRejection);
    };
  });

  // Units and language follow the account's settings record as it arrives.
  $effect(() => {
    void [$dataVersion, $initialSyncPending];
    if (started) reconcile().catch((e) => console.warn('applying account settings:', e));
  });

  // The server stopped accepting the session while the app was open. If only
  // the workspace is gone, move to another one instead of asking to sign in.
  const signedOut = $derived($syncStatus.state === 'signed_out');
  $effect(() => {
    if (!signedOut) return;
    void recoverSession().then((outcome) => {
      if (outcome === 'recovered') location.reload();
    });
  });

  // The server refused a change because this session may only read; sync
  // has undone it on this device. Say so each time, not only once.
  let refusalsSeen = get(readOnlyRefusals);
  $effect(() => {
    if ($readOnlyRefusals <= refusalsSeen) return;
    refusalsSeen = $readOnlyRefusals;
    toast(get(_)(isDemo ? 'sync.read_only_demo' : 'sync.read_only'));
  });

  // Main navigation. Settings lives separately, as the account block at the
  // bottom of the sidebar (and an account tab on mobile).
  const tabs = [
    { href: '/', key: 'nav.dashboard', icon: 'dashboard' },
    { href: '/apiaries', key: 'nav.apiaries', icon: 'apiaries' },
    { href: '/scan', key: 'nav.scan', icon: 'scan' },
    { href: '/hives', key: 'nav.hives', icon: 'hives' },
    { href: '/tasks', key: 'nav.tasks', icon: 'tasks' }
  ];

  function isActive(href: string): boolean {
    const p = page.url.pathname;
    return href === '/' ? p === '/' : p === href || p.startsWith(href + '/');
  }
  const current = (href: string) => (isActive(href) ? 'page' : undefined);
</script>

<!-- The main destinations, shared by the desktop sidebar and the mobile tab bar. -->
{#snippet tabLinks()}
  {#each tabs as t}
    <a href={t.href} class:active={isActive(t.href)} aria-current={current(t.href)}>
      <span class="ic"><NavIcon name={t.icon} /></span>
      <span class="lbl">{$_(t.key)}</span>
    </a>
  {/each}
{/snippet}

<div class="shell">
  <!-- Desktop sidebar -->
  <aside class="sidebar">
    <a class="brand" href="/">
      <span class="mark" aria-hidden="true">⬡</span>
      <span class="word">
        <strong>{$_('app.name')}</strong>
        <small>{$_('app.tagline')}</small>
      </span>
    </a>

    <nav class="side-nav" aria-label={$_('nav.main')}>
      {@render tabLinks()}
    </nav>

    <a
      class="account"
      href="/settings"
      class:active={isActive('/settings')}
      aria-current={current('/settings')}
      title={$_('nav.settings')}
    >
      <span class="avatar" aria-hidden="true">{initial}</span>
      <span class="acc-info">
        <strong>{email}</strong>
        <SyncChip passive />
      </span>
    </a>
  </aside>

  <div class="main-col">
    <UpdatePrompt />
    {#if isDemo}
      <div class="demo-bar">{$_('demo.banner')}</div>
    {/if}
    {#if !online}
      <div class="offline-bar">{$_('app.offline_banner')}</div>
    {/if}
    <div class="sync-row"><SyncChip problemsHref="/settings" /></div>
    <main class="content">
      {@render children()}
    </main>
  </div>
</div>

<Toasts />

<!-- Mobile bottom tab bar -->
<nav class="tabbar" aria-label={$_('nav.main')}>
  {@render tabLinks()}
  <a href="/settings" class:active={isActive('/settings')} aria-current={current('/settings')}>
    <span class="ic avatar-sm" aria-hidden="true">{initial}</span>
    <span class="lbl">{$_('nav.settings')}</span>
  </a>
</nav>

<style>
  /* ---- Mobile-first: single column above a fixed bottom nav ---- */
  .shell {
    min-height: 100vh;
  }
  .sidebar {
    display: none;
  }
  .content {
    min-height: 100vh;
    padding-bottom: calc(74px + env(safe-area-inset-bottom));
  }

  /* Sync status on phones; the desktop sidebar shows it in the account block. */
  .sync-row {
    display: flex;
    justify-content: flex-end;
    padding: 8px 14px 0;
  }

  .offline-bar {
    position: sticky;
    top: 0;
    z-index: 30;
    background: var(--text);
    color: var(--bg);
    font-size: 0.82rem;
    text-align: center;
    padding: 9px 12px;
  }
  /* Not sticky: only the offline banner stays in view, so the two never
     cover each other. */
  .demo-bar {
    background: var(--warn-bg);
    color: var(--warn-text);
    font-size: 0.82rem;
    font-weight: 600;
    text-align: center;
    padding: 9px 12px;
  }

  .tabbar {
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 40;
    display: grid;
    grid-template-columns: repeat(6, 1fr);
    background: color-mix(in srgb, var(--surface) 94%, transparent);
    backdrop-filter: blur(10px);
    border-top: 1px solid var(--border);
    padding-bottom: env(safe-area-inset-bottom);
  }
  .tabbar a {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    padding: 9px 2px 8px;
    text-decoration: none;
    color: var(--text-soft);
    font-size: 0.75rem;
    font-weight: 600;
    min-width: 0;
    /* Marks the current tab by shape as well as colour. */
    border-top: 2px solid transparent;
  }
  .tabbar a .lbl {
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tabbar a .ic {
    width: 22px;
    height: 22px;
    opacity: 0.75;
  }
  .tabbar a.active {
    color: var(--accent-text);
    border-top-color: var(--accent);
  }
  .tabbar a.active .ic {
    opacity: 1;
  }
  .avatar-sm {
    display: grid;
    place-items: center;
    border-radius: 50%;
    background: var(--accent);
    color: var(--on-accent);
    font-weight: 800;
    font-size: 0.8rem;
    opacity: 1 !important;
  }
  .tabbar a.active .avatar-sm {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 35%, transparent);
  }

  /* ---- Desktop: persistent sidebar, no bottom bar ---- */
  @media (min-width: 900px) {
    .shell {
      display: flex;
      align-items: stretch;
    }
    .tabbar,
    .sync-row {
      display: none;
    }
    .content {
      padding-bottom: 0;
    }

    .sidebar {
      display: flex;
      flex-direction: column;
      width: 252px;
      flex-shrink: 0;
      position: sticky;
      top: 0;
      height: 100vh;
      background: var(--surface);
      border-right: 1px solid var(--border);
      padding: 22px 16px;
    }
    .main-col {
      flex: 1;
      min-width: 0;
    }

    .brand {
      display: flex;
      align-items: center;
      gap: 11px;
      padding: 6px 10px 20px;
      text-decoration: none;
      color: var(--text);
    }
    .brand .mark {
      font-size: 1.7rem;
      color: var(--accent);
      line-height: 1;
    }
    .brand .word {
      display: flex;
      flex-direction: column;
      line-height: 1.15;
    }
    .brand .word strong {
      font-family: 'Fraunces', serif;
      font-size: 1.15rem;
    }
    .brand .word small {
      color: var(--text-soft);
      font-size: 0.75rem;
    }

    .side-nav {
      display: flex;
      flex-direction: column;
      gap: 3px;
    }
    .side-nav a {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 11px 12px;
      border-radius: 11px;
      text-decoration: none;
      color: var(--text-soft);
      font-weight: 600;
      font-size: 0.95rem;
      transition:
        background 0.12s,
        color 0.12s;
    }
    .side-nav a .ic {
      width: 21px;
      height: 21px;
      flex-shrink: 0;
      opacity: 0.8;
    }
    .side-nav a:hover {
      background: color-mix(in srgb, var(--accent) 8%, transparent);
      color: var(--text);
    }
    .side-nav a.active {
      background: color-mix(in srgb, var(--accent) 14%, transparent);
      color: var(--accent-text);
      /* Marks the current page by shape as well as colour. */
      box-shadow: inset 3px 0 0 var(--accent);
    }
    .side-nav a.active .ic {
      opacity: 1;
    }

    /* Account block pinned to the bottom of the sidebar -> Settings. */
    .account {
      margin-top: auto;
      display: flex;
      align-items: center;
      gap: 11px;
      padding: 10px 10px;
      border-radius: 12px;
      text-decoration: none;
      color: var(--text);
      border: 1px solid transparent;
      transition:
        background 0.12s,
        border-color 0.12s;
    }
    .account:hover {
      background: color-mix(in srgb, var(--accent) 8%, transparent);
    }
    .account.active {
      background: color-mix(in srgb, var(--accent) 12%, transparent);
      border-color: var(--accent);
    }
    .account .avatar {
      width: 38px;
      height: 38px;
      border-radius: 50%;
      flex-shrink: 0;
      display: grid;
      place-items: center;
      color: var(--on-accent);
      font-weight: 800;
      font-size: 1rem;
      background: var(--accent);
    }
    .account .acc-info {
      display: flex;
      flex-direction: column;
      gap: 2px;
      min-width: 0;
    }
    .account .acc-info strong {
      font-size: 0.85rem;
      font-weight: 700;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
  }
</style>
