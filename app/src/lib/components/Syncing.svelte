<script lang="ts">
  // "Syncing your data…" hint: shown instead of an empty state while the
  // device's first pull is still pending (see initialSyncPending in
  // lib/local/live.ts) — an empty local table is not yet a real empty state.
  import { _ } from 'svelte-i18n';
</script>

<p class="syncing" role="status">
  <span class="spin" aria-hidden="true"></span>{$_('common.syncing')}
</p>

<style>
  .syncing {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 10px;
    color: var(--ink-soft);
    font-size: 0.9rem;
    padding: 36px 0;
  }
  .spin {
    width: 16px;
    height: 16px;
    border-radius: 50%;
    flex-shrink: 0;
    border: 2px solid var(--line);
    border-top-color: var(--honey);
    animation: rot 0.8s linear infinite;
  }
  @keyframes rot {
    to {
      transform: rotate(360deg);
    }
  }
</style>
