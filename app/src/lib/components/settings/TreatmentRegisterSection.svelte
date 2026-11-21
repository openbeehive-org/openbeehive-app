<script lang="ts">
  // The treatment register of the whole workspace, one apiary or one year,
  // printed from here; the hive and apiary pages print their own part.
  import { onMount } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { Printer } from '@lucide/svelte';
  import { apiaries, hives, treatments } from '#lib/local/repo.js';
  import { dataVersion } from '#lib/local/live.js';
  import {
    printTreatmentRegister,
    treatmentYears,
    workspaceRegister
  } from '#lib/components/hive/treatmentRegister.js';
  import type { TreatmentRow } from '../../../routes/(app)/rows.js';
  import './settings.css';

  type Named = { id: string; name: string; apiary_id?: string | null };

  let apiaryList = $state<Named[]>([]);
  let hiveList = $state<Named[]>([]);
  let rows = $state<TreatmentRow[]>([]);
  let apiary = $state('');
  let year = $state('');
  let blocked = $state(false);

  const years = $derived(treatmentYears(rows));

  async function load() {
    apiaryList = (await apiaries.list()) as Named[];
    hiveList = (await hives.list()) as Named[];
    rows = (await treatments.listAll()) as TreatmentRow[];
  }
  onMount(() => dataVersion.subscribe(() => void load()));

  function print() {
    const register = workspaceRegister(apiaryList, hiveList, rows, { apiary, year });
    const scope = [apiaryList.find((a) => a.id === apiary)?.name ?? '', year]
      .filter(Boolean)
      .join(' · ');
    blocked = !printTreatmentRegister(register, $_, $locale ?? 'en', scope);
  }
</script>

<section class="st-card" id="treatment-register">
  <h2>{$_('treat.register')}</h2>
  <p class="st-help">{$_('treat.register_help')}</p>
  <div class="st-row">
    <label class="st-field">
      <span>{$_('hivelist.apiary')}</span>
      <select bind:value={apiary}>
        <option value="">{$_('hivelist.all_apiaries')}</option>
        {#each apiaryList as a (a.id)}
          <option value={a.id}>{a.name}</option>
        {/each}
      </select>
    </label>
    <label class="st-field narrow">
      <span>{$_('queens.year')}</span>
      <select bind:value={year}>
        <option value="">{$_('treat.all_years')}</option>
        {#each years as y (y)}
          <option value={y}>{y}</option>
        {/each}
      </select>
    </label>
    <button type="button" class="st-btn" onclick={print}
      ><Printer size={16} aria-hidden="true" /> {$_('treat.print_register')}</button
    >
  </div>
  {#if blocked}
    <p class="st-warn" role="alert">{$_('treat.print_blocked')}</p>
  {/if}
</section>
