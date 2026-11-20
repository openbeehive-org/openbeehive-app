<script lang="ts">
  // Export and import of the workspace's records.
  import { _ } from 'svelte-i18n';
  import { Printer } from '@lucide/svelte';
  import {
    exportJSON,
    exportXLSX,
    exportCSVZip,
    exportBeeXML,
    printReport,
    importFile,
    type ImportMode
  } from '#lib/data/portability.js';
  import './settings.css';

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

<section class="st-card">
  <h2>{$_('data.title')}</h2>
  <p class="st-help">{$_('data.export_help')}</p>
  <div class="exports">
    <button type="button" class="st-btn" disabled={busy} onclick={() => runExport(exportJSON)}
      >⤓ {$_('data.json')}</button
    >
    <button type="button" class="st-btn" disabled={busy} onclick={() => runExport(exportXLSX)}
      >⤓ {$_('data.xlsx')}</button
    >
    <button type="button" class="st-btn" disabled={busy} onclick={() => runExport(exportCSVZip)}
      >⤓ {$_('data.csv')}</button
    >
    <button type="button" class="st-btn" disabled={busy} onclick={() => runExport(exportBeeXML)}
      >⤓ {$_('data.beexml')}</button
    >
    <button type="button" class="st-btn" disabled={busy} onclick={() => runExport(printReport)}
      ><Printer size={16} aria-hidden="true" /> {$_('data.pdf')}</button
    >
  </div>

  <h3 class="st-sub">{$_('data.import')}</h3>
  <p class="st-help">{$_('data.import_help')}</p>
  <div class="st-row">
    <label class="st-field">
      <span>{$_('settings.import_format')}</span>
      <select bind:value={importMode} disabled={busy}>
        <option value="auto">{$_('data.mode_auto')}</option>
        <option value="json">{$_('data.mode_json')}</option>
        <option value="beexml">{$_('data.mode_beexml')}</option>
        <option value="csv">{$_('data.mode_csv')}</option>
      </select>
    </label>
    <!-- The input stays in the tab order (it is only painted invisibly), so
         the picker opens from the keyboard; the label shows its focus. -->
    <label class="st-btn file" class:disabled={busy}>
      {$_('data.import_btn')}
      <input
        class="st-visually-hidden"
        type="file"
        accept=".json,.xml,.csv,.txt"
        disabled={busy}
        onchange={onImport}
      />
    </label>
  </div>
  {#if importMsg}<p class="st-msg" class:err={importErr} role="status">{importMsg}</p>{/if}
</section>

<style>
  .exports {
    display: grid;
    gap: 10px;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  }
  .file {
    position: relative;
    flex-shrink: 0;
  }
  .file:focus-within {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .file.disabled {
    opacity: 0.55;
    cursor: default;
  }
</style>
