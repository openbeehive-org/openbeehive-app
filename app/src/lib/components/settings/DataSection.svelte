<script lang="ts">
  // Export and import of the workspace's records.
  import { onMount } from 'svelte';
  import { _, locale } from 'svelte-i18n';
  import { Printer } from '@lucide/svelte';
  import {
    exportXLSX,
    exportCSVZip,
    exportBeeXML,
    printReport,
    downloadBlob
  } from '#lib/data/portability.js';
  import {
    buildExport,
    exportReadiness,
    fetchServerExport,
    ServerExportError,
    type Readiness
  } from '#lib/data/backup.js';
  import {
    applyImport,
    lastImport,
    planImport,
    readImportFile,
    undoLastImport,
    type ImportMode,
    type ImportPlan,
    type ParsedImport
  } from '#lib/data/importer.js';
  import { formatDate } from '#lib/dates.js';
  import { syncStatus } from '#lib/local/sync.js';
  import './settings.css';

  let { workspaceName = '', readOnly = false }: { workspaceName?: string; readOnly?: boolean } =
    $props();

  let busy = $state(false);
  let readiness = $state<Readiness | null>(null);
  let withPhotos = $state(false);
  let serverPhotos = $state(false);
  let msg = $state('');
  let msgErr = $state(false);

  let importMode = $state<ImportMode>('auto');
  let parsed = $state<ParsedImport | null>(null);
  let plan = $state<ImportPlan | null>(null);
  let overwrite = $state(false);
  let restoreDeleted = $state(false);
  let undo = $state<{ at: string; created: number } | null>(null);

  const ready = $derived(readiness?.ready ?? false);
  const errText = (e: unknown) => String((e as Error)?.message ?? e);

  async function refresh() {
    readiness = await exportReadiness().catch(() => null);
    undo = await lastImport().catch(() => null);
  }
  // Readiness and the counts change with every sync run.
  onMount(() =>
    syncStatus.subscribe(() => {
      if (!busy) void refresh();
    })
  );

  function say(text: string, err = false) {
    msg = text;
    msgErr = err;
  }

  async function run(fn: () => Promise<void>) {
    if (busy) return;
    busy = true;
    say('');
    try {
      await refresh();
      await fn();
    } catch (e) {
      say(errText(e), true);
    } finally {
      busy = false;
      await refresh();
    }
  }

  const localExport = (fn: () => Promise<void>) =>
    run(async () => {
      if (!readiness?.ready) return say($_('data.not_synced'), true);
      await fn();
    });

  const exportBackup = () =>
    localExport(async () => {
      const built = await buildExport({ photos: withPhotos });
      downloadBlob(built.filename, built.blob);
      say(
        built.missingPhotos.length
          ? $_('data.photos_missing', { values: { n: built.missingPhotos.length } })
          : $_('data.exported'),
        built.missingPhotos.length > 0
      );
    });

  const exportServer = () =>
    run(async () => {
      try {
        const got = await fetchServerExport(serverPhotos);
        downloadBlob(got.filename, got.blob);
        say($_('data.exported'));
      } catch (e) {
        if (!(e instanceof ServerExportError)) throw e;
        const key =
          e.code === 'rate_limited'
            ? 'data.server_rate_limited'
            : e.code === 'network'
              ? 'data.server_offline'
              : e.status === 403
                ? 'data.server_forbidden'
                : 'data.server_failed';
        say($_(key), true);
      }
    });

  async function replan() {
    if (!parsed) return;
    plan = await planImport(parsed, { overwrite, restoreDeleted: overwrite && restoreDeleted });
  }

  async function onPick(ev: Event) {
    const input = ev.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!file) return;
    cancelImport();
    await run(async () => {
      if (!readiness?.ready) return say($_('data.not_synced'), true);
      try {
        parsed = await readImportFile(file, importMode);
        await replan();
      } catch (e) {
        parsed = null;
        say($_('data.import_failed', { values: { msg: errText(e) } }), true);
      }
    });
  }

  function cancelImport() {
    parsed = null;
    plan = null;
    overwrite = false;
    restoreDeleted = false;
  }

  const startImport = () =>
    run(async () => {
      if (!plan) return;
      try {
        const done = await applyImport(plan);
        cancelImport();
        say($_('data.imported', { values: done }));
      } catch (e) {
        say($_('data.import_failed', { values: { msg: errText(e) } }), true);
      }
    });

  const undoImport = () =>
    run(async () => {
      const res = await undoLastImport();
      say(
        res.kept
          ? $_('data.undone_kept', { values: { n: res.removed, kept: res.kept } })
          : $_('data.undone', { values: { n: res.removed } })
      );
    });

  const ENTITY_ORDER = [
    'apiary',
    'hive',
    'queen',
    'placement',
    'inspection',
    'harvest',
    'treatment',
    'task',
    'event',
    'photo'
  ];
  const planRows = $derived(
    plan ? ENTITY_ORDER.filter((t) => plan!.counts[t]).map((t) => ({ t, c: plan!.counts[t]! })) : []
  );
  const nothingToDo = $derived(!!plan && plan.ops.length === 0);
</script>

<section class="st-card">
  <h2>{$_('data.title')}</h2>
  <p class="st-help">{$_('data.export_help')}</p>

  {#if readiness && !readiness.ready}
    <p class="st-warn" role="status">{$_('data.not_synced')}</p>
  {:else if readiness}
    {#if readiness.pending > 0}
      <p class="st-help">{$_('data.pending_note', { values: { n: readiness.pending } })}</p>
    {/if}
    {#if readiness.failed > 0}
      <p class="st-warn">{$_('data.failed_note', { values: { n: readiness.failed } })}</p>
    {/if}
  {/if}

  <h3 class="st-sub">{$_('data.backup_title')}</h3>
  <p class="st-help">{$_('data.json_help')}</p>
  <label class="st-check">
    <input type="checkbox" bind:checked={withPhotos} disabled={busy} />
    <span>{$_('data.include_photos')}<small>{$_('data.include_photos_help')}</small></span>
  </label>
  <button type="button" class="st-btn primary" disabled={busy || !ready} onclick={exportBackup}
    >⤓ {$_('data.json')}</button
  >

  <h3 class="st-sub">{$_('data.other_title')}</h3>
  <p class="st-help">{$_('data.other_help')}</p>
  <div class="exports">
    <button
      type="button"
      class="st-btn"
      disabled={busy || !ready}
      onclick={() => localExport(exportXLSX)}>⤓ {$_('data.xlsx')}</button
    >
    <button
      type="button"
      class="st-btn"
      disabled={busy || !ready}
      onclick={() => localExport(exportCSVZip)}>⤓ {$_('data.csv')}</button
    >
    <button
      type="button"
      class="st-btn"
      disabled={busy || !ready}
      onclick={() => localExport(exportBeeXML)}>⤓ {$_('data.beexml')}</button
    >
    <button
      type="button"
      class="st-btn"
      disabled={busy || !ready}
      onclick={() => localExport(printReport)}
      ><Printer size={16} aria-hidden="true" /> {$_('data.pdf')}</button
    >
  </div>

  {#if !readOnly}
    <h3 class="st-sub">{$_('data.server_title')}</h3>
    <p class="st-help">{$_('data.server_help')}</p>
    <label class="st-check">
      <input type="checkbox" bind:checked={serverPhotos} disabled={busy} />
      <span>{$_('data.include_photos')}</span>
    </label>
    <button type="button" class="st-btn" disabled={busy} onclick={exportServer}
      >⤓ {$_('data.server_btn')}</button
    >

    <h3 class="st-sub">{$_('data.import')}</h3>
    <p class="st-help">{$_('data.import_help')}</p>
    <p class="st-help">{$_('data.import_default')}</p>
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
      <label class="st-btn file" class:disabled={busy || !ready}>
        {$_('data.import_btn')}
        <input
          class="st-visually-hidden"
          type="file"
          accept=".json,.zip,.xml,.csv,.txt"
          disabled={busy || !ready}
          onchange={onPick}
        />
      </label>
    </div>

    {#if plan}
      <div class="st-note preview" role="region" aria-label={$_('data.preview_title')}>
        <h4>{$_('data.preview_title')}</h4>
        <p>
          {workspaceName
            ? $_('data.preview_into', { values: { name: workspaceName } })
            : $_('data.preview_into_this')}
        </p>
        {#if plan.foreign}<p class="st-help">{$_('data.preview_foreign')}</p>{/if}
        <!-- Scrolls sideways on a narrow screen; focusable so that works by keyboard too. -->
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <div class="table-wrap" tabindex="0">
          <table>
            <thead>
              <tr>
                <th scope="col">{$_('data.col_kind')}</th>
                <th scope="col">{$_('data.col_create')}</th>
                {#if overwrite}
                  <th scope="col">{$_('data.col_update')}</th>
                  <th scope="col">{$_('data.col_restore')}</th>
                {/if}
                <th scope="col">{$_('data.col_skip')}</th>
              </tr>
            </thead>
            <tbody>
              {#each planRows as r (r.t)}
                <tr>
                  <th scope="row">{$_('data.entity_' + r.t)}</th>
                  <td>{r.c.create}</td>
                  {#if overwrite}
                    <td>{r.c.update}</td>
                    <td>{r.c.restore}</td>
                  {/if}
                  <td>{r.c.skip}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        {#if plan.photosWithoutImage > 0}
          <p class="st-help">
            {$_('data.photos_no_image', { values: { n: plan.photosWithoutImage } })}
          </p>
        {/if}
        <label class="st-check">
          <input type="checkbox" bind:checked={overwrite} disabled={busy} onchange={replan} />
          <span>{$_('data.overwrite')}<small>{$_('data.overwrite_help')}</small></span>
        </label>
        {#if overwrite}
          <label class="st-check">
            <input
              type="checkbox"
              bind:checked={restoreDeleted}
              disabled={busy}
              onchange={replan}
            />
            <span>{$_('data.restore_deleted')}</span>
          </label>
        {/if}
        {#if nothingToDo}<p class="st-help">{$_('data.nothing_to_do')}</p>{/if}
        <div class="st-actions">
          <button
            type="button"
            class="st-btn primary"
            disabled={busy || nothingToDo}
            onclick={startImport}>{$_('data.import_start')}</button
          >
          <button type="button" class="st-btn" disabled={busy} onclick={cancelImport}
            >{$_('common.cancel')}</button
          >
        </div>
      </div>
    {/if}

    {#if undo && !plan}
      <div class="st-row undo">
        <p class="st-help">
          {$_('data.undo_help', {
            values: { n: undo.created, date: formatDate(undo.at, $locale) }
          })}
        </p>
        <button type="button" class="st-btn danger" disabled={busy} onclick={undoImport}
          >{$_('data.undo')}</button
        >
      </div>
    {/if}
  {/if}

  {#if msg}<p class="st-msg" class:err={msgErr} role="status">{msg}</p>{/if}
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
  .preview h4 {
    margin: 0 0 6px;
  }
  .table-wrap {
    overflow-x: auto;
    margin: 8px 0;
  }
  table {
    border-collapse: collapse;
    font-size: 0.9rem;
  }
  th,
  td {
    text-align: left;
    padding: 4px 12px 4px 0;
    border-bottom: 1px solid var(--border);
  }
  td {
    font-variant-numeric: tabular-nums;
  }
  .undo {
    align-items: center;
  }
</style>
