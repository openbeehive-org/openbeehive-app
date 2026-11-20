<script lang="ts">
  // One hive in a list: photo, name, status, queen and the last visit.
  import { _ } from 'svelte-i18n';
  import { MapPin } from '@lucide/svelte';
  import { MARKING_COLORS } from '#lib/local/repo.js';
  import { daysSince, type HiveOverview } from '#lib/hivelist.js';

  let { hive, showApiary = false }: { hive: HiveOverview; showApiary?: boolean } = $props();

  const days = $derived(daysSince(hive.last_inspection));
  const alert = $derived(hive.status === 3 || hive.status === 4);
</script>

<a class="hive" href={`/hives/${hive.id}`}>
  <span class="thumb">
    {#if hive.photo}<img src={hive.photo} alt="" />{:else}<img
        class="ph"
        src="/favicon.svg"
        alt=""
      />{/if}
  </span>
  <span class="body">
    <strong>{hive.name}</strong>
    <span class="meta">
      {#if showApiary}
        {#if hive.apiary_name !== null}
          <span class="loc"><MapPin size={14} aria-hidden="true" /> {hive.apiary_name}</span>
        {:else}
          <span class="badge warn">{$_('hivelist.apiary_deleted')}</span>
        {/if}
      {/if}
      <span class="badge" class:alert>{$_('hivestatus.' + (hive.status ?? 0))}</span>
      {#if hive.queen_year}
        <span class="queen">
          <span
            class="dot"
            style:background={MARKING_COLORS[hive.queen_marking ?? 0] || 'transparent'}
            aria-hidden="true"
          ></span>
          {$_('queens.title')}
          {hive.queen_year}
        </span>
      {/if}
      <span>
        {$_('hives.last_inspection')}:
        {days === null
          ? $_('hives.never')
          : days === 0
            ? $_('dashboard.today')
            : $_('dashboard.days_ago', { values: { days } })}
      </span>
    </span>
  </span>
  <span class="chev" aria-hidden="true">›</span>
</a>

<style>
  .hive {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 12px 14px;
    min-height: 44px;
    text-decoration: none;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 16px;
  }
  .hive:hover strong {
    color: var(--accent-text);
  }
  .thumb {
    width: 54px;
    height: 54px;
    border-radius: 12px;
    overflow: hidden;
    flex-shrink: 0;
    background: var(--warn-bg);
    display: grid;
    place-items: center;
    border: 1px solid var(--border);
  }
  .thumb img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .thumb .ph {
    width: 24px;
    height: 24px;
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .body strong {
    display: block;
    font-weight: 700;
    margin-bottom: 5px;
    overflow-wrap: anywhere;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 8px;
    align-items: center;
    font-size: 0.8rem;
    color: var(--text-soft);
  }
  .loc {
    font-weight: 600;
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }
  .badge {
    font-weight: 700;
    font-size: 0.72rem;
    padding: 3px 9px;
    border-radius: 999px;
    background: color-mix(in srgb, var(--ok) 16%, transparent);
    color: var(--text);
  }
  .badge.alert {
    background: color-mix(in srgb, var(--danger) 13%, transparent);
    color: var(--danger);
  }
  .badge.warn {
    background: var(--warn-bg);
    color: var(--warn-text);
  }
  .queen {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-weight: 600;
  }
  .dot {
    width: 11px;
    height: 11px;
    border-radius: 50%;
    border: 1px solid var(--border-strong);
  }
  .chev {
    color: var(--text-soft);
    font-size: 1.4rem;
  }
</style>
