<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { _ } from 'svelte-i18n';
  import {
    hives,
    queens,
    inspections,
    apiaries,
    harvests,
    treatments,
    markingForYear,
    MARKING_COLORS,
    MARKING_NAMES
  } from '$lib/local/repo';
  import {
    recordInspection,
    setQueen,
    moveHive,
    recordHarvest,
    recordTreatment
  } from '$lib/local/history';
  import { dataVersion } from '$lib/local/live';
  import { fileToThumbnail } from '$lib/image';
  import QrLabel from '$lib/components/QrLabel.svelte';

  const id = $page.params.id ?? '';
  let hive = $state<any>(null);
  let apiaryName = $state('');
  let queen = $state<any>(null);
  let queenLog = $state<any[]>([]);
  let visits = $state<any[]>([]);
  let locations = $state<any[]>([]);
  let allApiaries = $state<any[]>([]);
  let loaded = $state(false);
  let showQr = $state(false);

  // ---- move ----
  let moveForm = $state(false);
  let moveTarget = $state('');
  let photoBusy = $state(false);

  // ---- hive edit ----
  let editingHive = $state(false);
  let hName = $state('');
  let hType = $state(0);
  let hStatus = $state(1);

  // ---- queen form ----
  let queenForm = $state(false);
  const thisYear = new Date().getFullYear();
  let qYear = $state(thisYear);
  let qOrigin = $state('');
  let qNumber = $state('');
  let qMarkingAuto = $state(true);
  let qMarking = $state(markingForYear(thisYear));
  let qNote = $state('');
  const effMarking = $derived(qMarkingAuto ? markingForYear(qYear) : qMarking);

  // ---- visit form ----
  const today = () => new Date().toISOString().slice(0, 10);
  function emptyVisit() {
    return {
      date: today(),
      weather: '',
      queen_seen: false,
      eggs_seen: false,
      youngest_larva: 0,
      covered_larva: false,
      frames: 0,
      brood_frames: 0,
      stores: 0,
      queen_cells: 0,
      temperament: 0,
      calmness: 0,
      varroa: '',
      fed_kg: 0,
      frames_added: 0,
      frames_removed: 0,
      drone_frame_cut: false,
      super_added: false,
      weight_kg: 0,
      honey_kg: 0,
      temp_hive: null,
      temp_outside: null,
      humidity_hive: null,
      humidity_outside: null,
      note: ''
    };
  }
  let visitForm = $state(false);
  let v = $state(emptyVisit());
  let vPhotos = $state<string[]>([]);

  // ---- harvest ----
  let harvestList = $state<any[]>([]);
  let harvestTotal = $state(0);
  let harvestForm = $state(false);
  function emptyHarvest() {
    return {
      date: today(),
      amount_kg: 0,
      variety: '',
      water_content: 0,
      batch_number: '',
      best_before: '',
      note: ''
    };
  }
  let hv = $state(emptyHarvest());

  // ---- treatments (Bestandsbuch) ----
  let treatmentList = $state<any[]>([]);
  let treatmentForm = $state(false);
  function emptyTreatment() {
    return {
      date: today(),
      product: '',
      active_ingredient: '',
      dose: '',
      method: '',
      batch_number: '',
      withdrawal_until: '',
      reason: 'varroa',
      note: ''
    };
  }
  let tr = $state(emptyTreatment());

  // Latest-reading pills (link into the development charts) + capped visit list.
  const PILL_KEYS: { key: string; label: string; unit?: string; parse?: boolean }[] = [
    { key: 'frames', label: 'insp.frames' },
    { key: 'brood_frames', label: 'insp.brood_frames' },
    { key: 'queen_cells', label: 'insp.queen_cells' },
    { key: 'weight_kg', label: 'insp.weight_kg', unit: 'kg' },
    { key: 'varroa', label: 'insp.varroa', parse: true }
  ];
  const latestVisit = $derived(visits[0]);
  const statPills = $derived(
    latestVisit
      ? PILL_KEYS.map((p) => ({
          ...p,
          value: p.parse ? parseFloat(latestVisit[p.key]) : Number(latestVisit[p.key])
        })).filter((p) => !isNaN(p.value) && p.value > 0)
      : []
  );
  const recentVisits = $derived(visits.slice(0, 5));

  const TEMPERAMENTS = [1, 2, 3, 4, 5];
  const STORES = [1, 2, 3, 4];
  const CALM = [1, 2, 3, 4];
  const HIVE_TYPES = [1, 2, 3, 4, 5, 6, 99];
  const HIVE_STATUSES = [1, 2, 3, 4, 5];

  async function load() {
    hive = await hives.get(id);
    if (hive) {
      const ap = hive.apiary_id ? await apiaries.get(hive.apiary_id) : null;
      apiaryName = ap?.name ?? '';
      queen = await queens.current(id);
      queenLog = await queens.listByHive(id);
      visits = await inspections.listByHive(id);
      locations = await hives.locationHistory(id);
      allApiaries = await apiaries.list();
      harvestList = await harvests.listByHive(id);
      harvestTotal = await harvests.totalByHive(id);
      treatmentList = await treatments.listByHive(id);
    }
    loaded = true;
  }

  async function saveHarvest() {
    if (!hv.amount_kg) {
      harvestForm = false;
      return;
    }
    await recordHarvest({
      hiveId: id,
      date: new Date(hv.date).toISOString(),
      amount_kg: hv.amount_kg,
      variety: hv.variety.trim(),
      water_content: hv.water_content,
      batch_number: hv.batch_number.trim(),
      best_before: hv.best_before ? new Date(hv.best_before).toISOString() : '',
      note: hv.note.trim()
    });
    hv = emptyHarvest();
    harvestForm = false;
    await load();
  }

  async function saveTreatment() {
    if (!tr.product.trim()) {
      treatmentForm = false;
      return;
    }
    await recordTreatment({
      hiveId: id,
      date: new Date(tr.date).toISOString(),
      product: tr.product.trim(),
      active_ingredient: tr.active_ingredient.trim(),
      dose: tr.dose.trim(),
      method: tr.method.trim(),
      batch_number: tr.batch_number.trim(),
      withdrawal_until: tr.withdrawal_until ? new Date(tr.withdrawal_until).toISOString() : '',
      reason: tr.reason.trim(),
      note: tr.note.trim()
    });
    tr = emptyTreatment();
    treatmentForm = false;
    await load();
  }

  async function onPhoto(e: Event) {
    const file = (e.target as HTMLInputElement).files?.[0];
    if (!file) return;
    photoBusy = true;
    try {
      const dataUrl = await fileToThumbnail(file);
      await hives.setPhoto(id, hive.apiary_id, dataUrl);
      await load();
    } finally {
      photoBusy = false;
    }
  }
  async function removePhoto() {
    await hives.setPhoto(id, hive.apiary_id, '');
    await load();
  }

  function openMove() {
    moveTarget = allApiaries.find((a: any) => a.id !== hive.apiary_id)?.id ?? '';
    moveForm = true;
  }
  async function doMove() {
    if (!moveTarget || moveTarget === hive.apiary_id) {
      moveForm = false;
      return;
    }
    await moveHive(id, hive.apiary_id, moveTarget);
    moveForm = false;
    await load();
  }

  function startEditHive() {
    hName = hive.name;
    hType = hive.type ?? 0;
    hStatus = hive.status ?? 1;
    editingHive = true;
  }
  async function saveHive() {
    if (!hName.trim()) return;
    await hives.update(id, hive.apiary_id, { name: hName.trim(), type: hType, status: hStatus });
    editingHive = false;
    await load();
  }
  async function delHive() {
    if (!confirm($_('common.confirm_delete'))) return;
    const back = hive.apiary_id;
    await hives.remove(id, hive.apiary_id);
    await goto(back ? `/apiaries/${back}` : '/hives', { replaceState: true });
  }

  function openQueenForm() {
    qYear = thisYear;
    qOrigin = '';
    qNumber = '';
    qMarkingAuto = true;
    qMarking = markingForYear(thisYear);
    qNote = '';
    queenForm = true;
  }
  async function saveQueen() {
    await setQueen(id, hive.apiary_id, {
      year: qYear,
      origin: qOrigin.trim(),
      marking: effMarking,
      breeder_number: qNumber.trim(),
      note: qNote.trim()
    });
    queenForm = false;
    await load();
  }

  async function onVisitPhotos(e: Event) {
    const files = (e.target as HTMLInputElement).files;
    if (!files) return;
    for (const f of Array.from(files)) vPhotos.push(await fileToThumbnail(f, 640));
    vPhotos = vPhotos;
    (e.target as HTMLInputElement).value = '';
  }

  let visitError = $state('');
  async function saveVisit() {
    visitError = '';
    try {
      const inspId = await recordInspection(id, {
        date: new Date(v.date).toISOString(),
        weather: v.weather.trim(),
        queen_seen: v.queen_seen ? 1 : 0,
        eggs_seen: v.eggs_seen ? 1 : 0,
        youngest_larva: v.youngest_larva,
        covered_larva: v.covered_larva ? 1 : 0,
        frames: v.frames,
        brood_frames: v.brood_frames,
        stores: v.stores,
        queen_cells: v.queen_cells,
        temperament: v.temperament,
        calmness: v.calmness,
        varroa: v.varroa.trim(),
        fed_kg: v.fed_kg,
        frames_added: v.frames_added,
        frames_removed: v.frames_removed,
        drone_frame_cut: v.drone_frame_cut ? 1 : 0,
        super_added: v.super_added ? 1 : 0,
        weight_kg: v.weight_kg,
        honey_kg: v.honey_kg,
        temp_hive: v.temp_hive,
        temp_outside: v.temp_outside,
        humidity_hive: v.humidity_hive,
        humidity_outside: v.humidity_outside,
        note: v.note.trim()
      });
      // Photos go to the inspection's photo_keys OR-Set (add-wins, conflict-free).
      for (const ph of vPhotos) await inspections.addPhoto(inspId, hive.apiary_id, ph);
      v = emptyVisit();
      vPhotos = [];
      visitForm = false;
      await load();
    } catch (e) {
      visitError = String((e as Error)?.message ?? e);
    }
  }

  // Compact list of chips summarising a recorded visit.
  function chips(i: any): string[] {
    const out: string[] = [];
    if (i.queen_seen) out.push('👑 ' + $_('insp.queen_seen'));
    if (i.eggs_seen) out.push($_('insp.eggs_seen'));
    if (i.youngest_larva) out.push(`${$_('insp.youngest_larva')}: ${i.youngest_larva}d`);
    if (i.covered_larva) out.push($_('insp.covered_larva'));
    if (i.frames) out.push(`${i.frames} ${$_('insp.frames')}`);
    if (i.brood_frames) out.push(`${i.brood_frames} ${$_('insp.brood_frames')}`);
    if (i.stores) out.push(`${$_('insp.stores')}: ${$_('storeslvl.' + i.stores)}`);
    if (i.queen_cells) out.push(`${i.queen_cells} ${$_('insp.queen_cells')}`);
    if (i.temperament) out.push(`${$_('insp.temperament')}: ${$_('temperament.' + i.temperament)}`);
    if (i.calmness) out.push(`${$_('insp.calmness')}: ${$_('calmlvl.' + i.calmness)}`);
    if (i.fed_kg) out.push(`${$_('insp.fed_kg')} ${i.fed_kg} kg`);
    if (i.frames_added) out.push(`+${i.frames_added} ${$_('insp.frames')}`);
    if (i.frames_removed) out.push(`−${i.frames_removed} ${$_('insp.frames')}`);
    if (i.drone_frame_cut) out.push('✂ ' + $_('insp.drone_frame_cut'));
    if (i.super_added) out.push($_('insp.super_added'));
    if (i.weight_kg) out.push(`${i.weight_kg} kg`);
    if (i.honey_kg) out.push(`🍯 ${i.honey_kg} kg`);
    if (i.temp_hive != null) out.push(`🌡 ${i.temp_hive}°C`);
    if (i.humidity_hive != null) out.push(`💧 ${i.humidity_hive}%`);
    if (i.varroa) out.push(`${$_('insp.varroa')}: ${i.varroa}`);
    return out;
  }

  $effect(() => {
    $dataVersion;
    load();
  });
</script>

<div class="page">
  <a class="back" href={hive?.apiary_id ? `/apiaries/${hive.apiary_id}` : '/hives'}>
    ‹ {apiaryName || $_('hives.title')}
  </a>

  {#if !loaded}
    <p class="muted">…</p>
  {:else if !hive}
    <p class="muted">{$_('hives.not_found')}</p>
  {:else}
    <header>
      <div class="title">
        <label class="avatar" title={$_('hives.photo')}>
          {#if hive.photo}
            <img src={hive.photo} alt={hive.name} />
          {:else}
            <span class="ph">🐝</span>
          {/if}
          <input type="file" accept="image/*" onchange={onPhoto} hidden />
          <span class="cam">{photoBusy ? '…' : '📷'}</span>
        </label>
        <div>
          <h1>{hive.name}</h1>
          <p class="muted">
            {$_('hivestatus.' + (hive.status ?? 0))}
            {#if hive.type}· {$_('hivetype.' + hive.type)}{/if}
          </p>
          {#if hive.photo}
            <button class="linkbtn" onclick={removePhoto}>{$_('hives.remove_photo')}</button>
          {/if}
        </div>
      </div>
      <div class="acts">
        <a class="icon" href={`/hives/${id}/stockkarte`} title={$_('insp.stockkarte')}>▦</a>
        <a class="icon" href={`/hives/${id}/stats`} title={$_('stats.title')}>📈</a>
        <button class="icon" onclick={startEditHive} title={$_('hives.edit')}>✎</button>
        <button class="icon" onclick={openMove} title={$_('hives.move')}>⇄</button>
        <button class="icon" onclick={() => (showQr = !showQr)} title="QR">⬚</button>
        <button class="icon danger" onclick={delHive} title={$_('common.delete')}>🗑</button>
      </div>
    </header>

    {#if statPills.length}
      <div class="pills">
        {#each statPills as p}
          <a class="pill" href={`/hives/${id}/stats#${p.key}`}>
            <span class="pl">{$_(p.label)}</span>
            <strong>{p.value}{p.unit ? ' ' + p.unit : ''}</strong>
          </a>
        {/each}
        <a class="pill dev" href={`/hives/${id}/stats`}>📈 {$_('stats.title')}</a>
      </div>
    {/if}

    {#if moveForm}
      <form
        class="card form"
        onsubmit={(e) => {
          e.preventDefault();
          doMove();
        }}
      >
        <label class="field">
          <span>{$_('hives.move_to')}</span>
          <select bind:value={moveTarget}>
            {#each allApiaries.filter((a) => a.id !== hive.apiary_id) as a}
              <option value={a.id}>{a.name}</option>
            {/each}
          </select>
        </label>
        <div class="actions">
          <button type="button" class="ghost" onclick={() => (moveForm = false)}
            >{$_('common.cancel')}</button
          >
          <button type="submit" class="primary" disabled={!moveTarget}>{$_('hives.move')}</button>
        </div>
      </form>
    {/if}

    {#if editingHive}
      <form
        class="card form"
        onsubmit={(e) => {
          e.preventDefault();
          saveHive();
        }}
      >
        <input bind:value={hName} placeholder={$_('hives.name')} />
        <label class="field">
          <span>{$_('hives.type')}</span>
          <select bind:value={hType}>
            {#each HIVE_TYPES as t}<option value={t}>{$_('hivetype.' + t)}</option>{/each}
          </select>
        </label>
        <label class="field">
          <span>{$_('hives.status')}</span>
          <select bind:value={hStatus}>
            {#each HIVE_STATUSES as s}<option value={s}>{$_('hivestatus.' + s)}</option>{/each}
          </select>
        </label>
        <div class="actions">
          <button type="button" class="ghost" onclick={() => (editingHive = false)}
            >{$_('common.cancel')}</button
          >
          <button type="submit" class="primary" disabled={!hName.trim()}>{$_('common.save')}</button
          >
        </div>
      </form>
    {/if}

    {#if showQr}
      <div class="qrwrap card">
        <QrLabel hiveId={id} name={hive.name} />
      </div>
    {/if}

    <!-- ---- Queen ---- -->
    <section>
      <div class="sec-head">
        <h2>{$_('queens.title')}</h2>
        {#if queen}
          <button class="primary sm" onclick={openQueenForm}>{$_('queens.replace')}</button>
        {/if}
      </div>

      {#if queenForm}
        <form
          class="card form"
          onsubmit={(e) => {
            e.preventDefault();
            saveQueen();
          }}
        >
          <div class="grid2">
            <label class="field"
              ><span>{$_('queens.year')}</span>
              <input type="number" bind:value={qYear} min="2000" max="2100" /></label
            >
            <label class="field"
              ><span>{$_('queens.number')}</span>
              <input
                bind:value={qNumber}
                inputmode="numeric"
                placeholder={$_('queens.number_ph')}
              /></label
            >
            <label class="field"
              ><span>{$_('queens.marking')}</span>
              <div class="marking">
                <span class="dot" style={`background:${MARKING_COLORS[effMarking]}`}></span>
                {MARKING_NAMES[effMarking]}
                <label class="auto"
                  ><input type="checkbox" bind:checked={qMarkingAuto} /> auto</label
                >
                {#if !qMarkingAuto}
                  <select bind:value={qMarking}>
                    {#each [1, 2, 3, 4, 5] as m}<option value={m}>{MARKING_NAMES[m]}</option>{/each}
                  </select>
                {/if}
              </div>
            </label>
          </div>
          <input bind:value={qOrigin} placeholder={$_('queens.origin_ph')} />
          <textarea bind:value={qNote} rows="2" placeholder={$_('queens.comment_ph')}></textarea>
          <div class="actions">
            <button type="button" class="ghost" onclick={() => (queenForm = false)}
              >{$_('common.cancel')}</button
            >
            <button type="submit" class="primary">{$_('common.save')}</button>
          </div>
        </form>
      {/if}

      {#if queen}
        <div class="card queen-card">
          <span
            class="dot lg"
            style={`background:${MARKING_COLORS[queen.marking] ?? '#ccc'}`}
            title={MARKING_NAMES[queen.marking] ?? ''}
          ></span>
          <div>
            <strong
              >{$_('queens.title')}
              {queen.year}{#if queen.breeder_number}
                · #{queen.breeder_number}{/if}</strong
            >
            <div class="muted sm">
              {MARKING_NAMES[queen.marking] ?? ''}{#if queen.origin}
                · {queen.origin}{/if}
            </div>
            {#if queen.note}<div class="muted sm qnote">{queen.note}</div>{/if}
          </div>
        </div>
      {:else if !queenForm}
        <div class="empty-inline">
          <span class="muted">{$_('queens.none')}</span>
          <button class="primary sm" onclick={openQueenForm}>{$_('queens.set')}</button>
        </div>
      {/if}

      {#if queenLog.length > 1}
        <details class="qhist">
          <summary>{$_('queens.history')}</summary>
          <ul>
            {#each queenLog as q}
              <li>
                <span class="dot" style={`background:${MARKING_COLORS[q.marking] ?? '#ccc'}`}
                ></span>
                {$_('queens.title')}
                {q.year}
                <span class="muted sm">
                  {q.introduced_at ? new Date(q.introduced_at).toLocaleDateString() : ''}
                  {q.replaced_at ? '– ' + new Date(q.replaced_at).toLocaleDateString() : ''}
                </span>
              </li>
            {/each}
          </ul>
        </details>
      {/if}
    </section>

    <!-- ---- Visit log (Stockkarte) ---- -->
    <section>
      <div class="sec-head">
        <h2>{$_('insp.log')}</h2>
        <button class="primary sm" onclick={() => (visitForm = !visitForm)}
          >+ {$_('insp.record')}</button
        >
      </div>

      {#if visitForm}
        <form
          class="card form"
          onsubmit={(e) => {
            e.preventDefault();
            saveVisit();
          }}
        >
          <div class="grid2">
            <label class="field"
              ><span>{$_('insp.date')}</span><input type="date" bind:value={v.date} /></label
            >
            <label class="field"
              ><span>{$_('insp.weather')}</span><input
                bind:value={v.weather}
                placeholder={$_('insp.weather_ph')}
              /></label
            >
          </div>

          <h3>{$_('insp.colony')}</h3>
          <div class="checks">
            <label
              ><input type="checkbox" bind:checked={v.queen_seen} /> {$_('insp.queen_seen')}</label
            >
            <label
              ><input type="checkbox" bind:checked={v.eggs_seen} /> {$_('insp.eggs_seen')}</label
            >
            <label
              ><input type="checkbox" bind:checked={v.covered_larva} />
              {$_('insp.covered_larva')}</label
            >
          </div>
          <div class="grid2">
            <label class="field"
              ><span>{$_('insp.youngest_larva')} (d)</span><input
                type="number"
                min="0"
                bind:value={v.youngest_larva}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.frames')}</span><input
                type="number"
                min="0"
                bind:value={v.frames}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.brood_frames')}</span><input
                type="number"
                min="0"
                bind:value={v.brood_frames}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.stores')}</span>
              <select bind:value={v.stores}
                ><option value={0}>—</option>{#each STORES as s}<option value={s}
                    >{$_('storeslvl.' + s)}</option
                  >{/each}</select
              ></label
            >
            <label class="field"
              ><span>{$_('insp.queen_cells')}</span><input
                type="number"
                min="0"
                bind:value={v.queen_cells}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.temperament')}</span>
              <select bind:value={v.temperament}
                ><option value={0}>—</option>{#each TEMPERAMENTS as t}<option value={t}
                    >{$_('temperament.' + t)}</option
                  >{/each}</select
              ></label
            >
            <label class="field"
              ><span>{$_('insp.calmness')}</span>
              <select bind:value={v.calmness}
                ><option value={0}>—</option>{#each CALM as c}<option value={c}
                    >{$_('calmlvl.' + c)}</option
                  >{/each}</select
              ></label
            >
            <label class="field"
              ><span>{$_('insp.varroa')}</span><input
                bind:value={v.varroa}
                placeholder={$_('insp.varroa_ph')}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.weight_kg')} (kg)</span><input
                type="number"
                min="0"
                step="0.1"
                bind:value={v.weight_kg}
              /></label
            >
          </div>

          <h3>{$_('insp.climate')}</h3>
          <div class="grid2">
            <label class="field"
              ><span>{$_('insp.temp_hive')} (°C)</span><input
                type="number"
                step="0.1"
                bind:value={v.temp_hive}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.temp_outside')} (°C)</span><input
                type="number"
                step="0.1"
                bind:value={v.temp_outside}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.humidity_hive')} (%)</span><input
                type="number"
                min="0"
                max="100"
                step="1"
                bind:value={v.humidity_hive}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.humidity_outside')} (%)</span><input
                type="number"
                min="0"
                max="100"
                step="1"
                bind:value={v.humidity_outside}
              /></label
            >
          </div>

          <h3>{$_('insp.activities')}</h3>
          <div class="grid2">
            <label class="field"
              ><span>{$_('insp.fed_kg')} (kg)</span><input
                type="number"
                min="0"
                step="0.1"
                bind:value={v.fed_kg}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.honey_kg')} (kg)</span><input
                type="number"
                min="0"
                step="0.1"
                bind:value={v.honey_kg}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.frames_added')}</span><input
                type="number"
                min="0"
                bind:value={v.frames_added}
              /></label
            >
            <label class="field"
              ><span>{$_('insp.frames_removed')}</span><input
                type="number"
                min="0"
                bind:value={v.frames_removed}
              /></label
            >
          </div>
          <div class="checks">
            <label
              ><input type="checkbox" bind:checked={v.drone_frame_cut} />
              {$_('insp.drone_frame_cut')}</label
            >
            <label
              ><input type="checkbox" bind:checked={v.super_added} />
              {$_('insp.super_added')}</label
            >
          </div>

          <textarea bind:value={v.note} rows="2" placeholder={$_('insp.note_ph')}></textarea>

          <div class="photos-edit">
            {#each vPhotos as ph, idx}
              <span class="thumb">
                <img src={ph} alt="" />
                <button
                  type="button"
                  class="rm"
                  onclick={() => {
                    vPhotos.splice(idx, 1);
                    vPhotos = vPhotos;
                  }}>×</button
                >
              </span>
            {/each}
            <label class="addphoto">
              <input type="file" accept="image/*" multiple onchange={onVisitPhotos} hidden />
              <span>📷 {$_('insp.add_photo')}</span>
            </label>
          </div>

          {#if visitError}<p class="err">{visitError}</p>{/if}
          <div class="actions">
            <button
              type="button"
              class="ghost"
              onclick={() => {
                visitForm = false;
                v = emptyVisit();
                vPhotos = [];
                visitError = '';
              }}>{$_('common.cancel')}</button
            >
            <button type="submit" class="primary">{$_('common.save')}</button>
          </div>
        </form>
      {/if}

      {#if visits.length === 0}
        <p class="muted empty">{$_('insp.no_visits')}</p>
      {:else}
        <ul class="timeline">
          {#each recentVisits as i}
            <li class="card visit">
              <div class="vhead">
                <strong>{new Date(i.date).toLocaleDateString()}</strong>
                {#if i.weather}<span class="muted sm">{i.weather}</span>{/if}
              </div>
              {#if chips(i).length}
                <div class="chips">
                  {#each chips(i) as c}<span class="chip">{c}</span>{/each}
                </div>
              {/if}
              {#if i.note}<p class="vnote">{i.note}</p>{/if}
              {#if i.photos?.length}
                <div class="vphotos">
                  {#each i.photos as ph}
                    <a class="vphoto" href={ph} target="_blank" rel="noreferrer"
                      ><img src={ph} alt="" /></a
                    >
                  {/each}
                </div>
              {/if}
            </li>
          {/each}
        </ul>
        {#if visits.length > recentVisits.length}
          <a class="see-all" href={`/hives/${id}/visits`}>
            {$_('insp.see_all', { values: { n: visits.length } })} →
          </a>
        {/if}
      {/if}
    </section>

    <!-- ---- Honey harvests ---- -->
    <section>
      <div class="sec-head">
        <h2>
          {$_('harvest.title')}{#if harvestTotal}
            · {harvestTotal} kg{/if}
        </h2>
        <button class="primary sm" onclick={() => (harvestForm = !harvestForm)}
          >+ {$_('harvest.record')}</button
        >
      </div>

      {#if harvestForm}
        <form
          class="card form"
          onsubmit={(e) => {
            e.preventDefault();
            saveHarvest();
          }}
        >
          <div class="grid2">
            <label class="field"
              ><span>{$_('insp.date')}</span><input type="date" bind:value={hv.date} /></label
            >
            <label class="field"
              ><span>{$_('harvest.amount')} (kg)</span><input
                type="number"
                min="0"
                step="0.1"
                bind:value={hv.amount_kg}
              /></label
            >
            <label class="field"
              ><span>{$_('harvest.variety')}</span><input
                bind:value={hv.variety}
                placeholder={$_('harvest.variety_ph')}
              /></label
            >
            <label class="field"
              ><span>{$_('harvest.water')} (%)</span><input
                type="number"
                min="0"
                step="0.1"
                bind:value={hv.water_content}
              /></label
            >
            <label class="field"
              ><span>{$_('harvest.batch')}</span><input bind:value={hv.batch_number} /></label
            >
            <label class="field"
              ><span>{$_('harvest.best_before')}</span><input
                type="date"
                bind:value={hv.best_before}
              /></label
            >
          </div>
          <textarea bind:value={hv.note} rows="2" placeholder={$_('insp.note_ph')}></textarea>
          <div class="actions">
            <button
              type="button"
              class="ghost"
              onclick={() => {
                harvestForm = false;
                hv = emptyHarvest();
              }}>{$_('common.cancel')}</button
            >
            <button type="submit" class="primary" disabled={!hv.amount_kg}
              >{$_('common.save')}</button
            >
          </div>
        </form>
      {/if}

      {#if harvestList.length === 0}
        <p class="muted empty">{$_('harvest.none')}</p>
      {:else}
        <ul class="timeline">
          {#each harvestList as h}
            <li class="card visit">
              <div class="vhead">
                <strong
                  >🍯 {h.amount_kg} kg{#if h.variety}
                    · {h.variety}{/if}</strong
                >
                <span class="muted sm">{new Date(h.date).toLocaleDateString()}</span>
              </div>
              <div class="chips">
                {#if h.water_content}<span class="chip"
                    >{$_('harvest.water')}: {h.water_content}%</span
                  >{/if}
                {#if h.batch_number}<span class="chip">{$_('harvest.batch')}: {h.batch_number}</span
                  >{/if}
                {#if h.best_before}<span class="chip"
                    >{$_('harvest.best_before')}: {new Date(
                      h.best_before
                    ).toLocaleDateString()}</span
                  >{/if}
              </div>
              {#if h.note}<p class="vnote">{h.note}</p>{/if}
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <!-- ---- Treatments (Bestandsbuch) ---- -->
    <section>
      <div class="sec-head">
        <h2>{$_('treat.title')}</h2>
        <button class="primary sm" onclick={() => (treatmentForm = !treatmentForm)}
          >+ {$_('treat.record')}</button
        >
      </div>

      {#if treatmentForm}
        <form
          class="card form"
          onsubmit={(e) => {
            e.preventDefault();
            saveTreatment();
          }}
        >
          <div class="grid2">
            <label class="field"
              ><span>{$_('insp.date')}</span><input type="date" bind:value={tr.date} /></label
            >
            <label class="field"
              ><span>{$_('treat.product')}</span><input
                bind:value={tr.product}
                placeholder={$_('treat.product_ph')}
              /></label
            >
            <label class="field"
              ><span>{$_('treat.active')}</span><input
                bind:value={tr.active_ingredient}
                placeholder={$_('treat.active_ph')}
              /></label
            >
            <label class="field"
              ><span>{$_('treat.method')}</span><input
                bind:value={tr.method}
                placeholder={$_('treat.method_ph')}
              /></label
            >
            <label class="field"
              ><span>{$_('treat.dose')}</span><input
                bind:value={tr.dose}
                placeholder={$_('treat.dose_ph')}
              /></label
            >
            <label class="field"
              ><span>{$_('treat.batch')}</span><input bind:value={tr.batch_number} /></label
            >
            <label class="field"
              ><span>{$_('treat.reason')}</span><input
                bind:value={tr.reason}
                placeholder={$_('treat.reason_ph')}
              /></label
            >
            <label class="field"
              ><span>{$_('treat.withdrawal')}</span><input
                type="date"
                bind:value={tr.withdrawal_until}
              /></label
            >
          </div>
          <textarea bind:value={tr.note} rows="2" placeholder={$_('insp.note_ph')}></textarea>
          <div class="actions">
            <button
              type="button"
              class="ghost"
              onclick={() => {
                treatmentForm = false;
                tr = emptyTreatment();
              }}>{$_('common.cancel')}</button
            >
            <button type="submit" class="primary" disabled={!tr.product.trim()}
              >{$_('common.save')}</button
            >
          </div>
        </form>
      {/if}

      {#if treatmentList.length === 0}
        <p class="muted empty">{$_('treat.none')}</p>
      {:else}
        <ul class="timeline">
          {#each treatmentList as t}
            <li class="card visit">
              <div class="vhead">
                <strong>💊 {t.product}</strong>
                <span class="muted sm">{new Date(t.date).toLocaleDateString()}</span>
              </div>
              <div class="chips">
                {#if t.active_ingredient}<span class="chip">{t.active_ingredient}</span>{/if}
                {#if t.dose}<span class="chip">{$_('treat.dose')}: {t.dose}</span>{/if}
                {#if t.method}<span class="chip">{t.method}</span>{/if}
                {#if t.batch_number}<span class="chip">{$_('treat.batch')}: {t.batch_number}</span
                  >{/if}
                {#if t.withdrawal_until}<span class="chip warn"
                    >{$_('treat.withdrawal')}: {new Date(
                      t.withdrawal_until
                    ).toLocaleDateString()}</span
                  >{/if}
              </div>
              {#if t.note}<p class="vnote">{t.note}</p>{/if}
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <!-- ---- Location history (placements; appended on every move) ---- -->
    {#if locations.length}
      <section>
        <h2>{$_('hives.locations')}</h2>
        <ul class="locs">
          {#each locations as p, idx}
            <li>
              <span class="loc-dot" class:cur={idx === 0 && !p.end_at}></span>
              <div class="loc-body">
                <strong>{p.apiary_name ?? '—'}</strong>
                <span class="muted sm">
                  {p.start_at ? new Date(p.start_at).toLocaleDateString() : ''}
                  {p.end_at
                    ? '– ' + new Date(p.end_at).toLocaleDateString()
                    : '– ' + $_('hives.present')}
                </span>
              </div>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  {/if}
</div>

<style>
  .page {
    max-width: 760px;
    margin: 0 auto;
    padding: 22px 18px 96px;
  }
  .back {
    color: var(--ink-soft);
    text-decoration: none;
    font-weight: 600;
    font-size: 0.9rem;
  }
  .muted {
    color: var(--ink-soft);
  }
  .muted.sm,
  .sm {
    font-size: 0.82rem;
  }
  .empty {
    text-align: center;
    padding: 30px 0;
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
    margin: 14px 0 18px;
  }
  .title {
    display: flex;
    gap: 14px;
    align-items: flex-start;
  }

  .avatar {
    position: relative;
    width: 68px;
    height: 68px;
    border-radius: 16px;
    overflow: hidden;
    flex-shrink: 0;
    cursor: pointer;
    border: 1px solid var(--line);
    background: linear-gradient(150deg, #fff5e3, #ffe9c6);
    display: grid;
    place-items: center;
  }
  .avatar img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .avatar .ph {
    font-size: 1.9rem;
    filter: grayscale(0.1);
  }
  .avatar .cam {
    position: absolute;
    right: 3px;
    bottom: 3px;
    font-size: 0.72rem;
    background: rgba(255, 253, 247, 0.92);
    border-radius: 7px;
    padding: 1px 4px;
    line-height: 1.2;
  }
  .linkbtn {
    background: none;
    border: none;
    padding: 0;
    margin-top: 4px;
    cursor: pointer;
    color: var(--ink-soft);
    font: inherit;
    font-size: 0.78rem;
    text-decoration: underline;
  }
  h1 {
    font-family: 'Fraunces', serif;
    font-size: 1.9rem;
  }
  h2 {
    font-family: 'Fraunces', serif;
    font-size: 1.3rem;
  }
  h3 {
    font-size: 0.82rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--ink-soft);
    margin-top: 6px;
    font-weight: 700;
  }
  .acts {
    display: flex;
    gap: 8px;
    flex-shrink: 0;
  }
  /* .icon sits on <a> and <button> alike — center the glyph explicitly, since
     anchors have no default centering and buttons differ per browser. */
  .icon {
    width: 38px;
    height: 38px;
    border: 1px solid var(--line);
    border-radius: 10px;
    background: var(--cream2);
    cursor: pointer;
    font-size: 1rem;
    color: var(--ink);
    display: grid;
    place-items: center;
    padding: 0;
    line-height: 1;
    text-decoration: none;
  }
  .icon.danger:hover {
    border-color: #d9a59b;
    color: #b5402f;
  }

  section {
    margin-top: 26px;
  }
  .sec-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 14px;
  }

  .pills {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    margin: 4px 0 2px;
  }
  .pill {
    display: flex;
    flex-direction: column;
    gap: 1px;
    padding: 8px 13px;
    border-radius: 12px;
    background: var(--cream2);
    border: 1px solid var(--line);
    text-decoration: none;
    color: var(--ink);
  }
  .pill .pl {
    font-size: 0.68rem;
    color: var(--ink-soft);
    font-weight: 600;
  }
  .pill strong {
    font-size: 1rem;
  }
  .pill:hover {
    border-color: var(--honey);
  }
  .pill.dev {
    justify-content: center;
    align-items: center;
    font-weight: 700;
    color: var(--honey-d);
    background: rgba(199, 127, 34, 0.1);
    border-color: rgba(199, 127, 34, 0.25);
  }
  .see-all {
    display: inline-block;
    margin-top: 12px;
    color: var(--honey-d);
    font-weight: 700;
    text-decoration: none;
    font-size: 0.9rem;
  }
  .qnote {
    white-space: pre-wrap;
    margin-top: 3px;
  }

  .card {
    background: var(--cream2);
    border: 1px solid var(--line);
    border-radius: 16px;
  }
  .form {
    padding: 16px;
    display: grid;
    gap: 12px;
    margin-bottom: 16px;
  }
  .grid2 {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 12px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 5px;
    font-size: 0.82rem;
    color: var(--ink-soft);
    font-weight: 600;
  }
  .form input,
  .form select,
  .form textarea {
    font: inherit;
    padding: 10px 12px;
    border: 1px solid var(--line);
    border-radius: 10px;
    background: #fff;
    color: var(--ink);
    resize: vertical;
    font-weight: 400;
  }
  .checks {
    display: flex;
    flex-wrap: wrap;
    gap: 16px;
  }
  .checks label {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    font-weight: 600;
    font-size: 0.9rem;
    cursor: pointer;
  }
  .checks input {
    width: 17px;
    height: 17px;
    accent-color: var(--honey);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
  }

  .primary {
    background: var(--honey);
    color: #fff;
    border: none;
    border-radius: 11px;
    padding: 10px 16px;
    font-weight: 700;
    font-family: inherit;
    cursor: pointer;
    box-shadow: 0 4px 12px rgba(199, 127, 34, 0.28);
  }
  .primary.sm {
    padding: 8px 13px;
    font-size: 0.85rem;
    box-shadow: none;
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: default;
    box-shadow: none;
  }
  .ghost {
    background: transparent;
    border: 1px solid var(--line);
    border-radius: 11px;
    padding: 10px 16px;
    font-weight: 600;
    font-family: inherit;
    cursor: pointer;
    color: var(--ink);
  }

  .marking {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 600;
    color: var(--ink);
  }
  .marking .auto {
    font-weight: 400;
    font-size: 0.8rem;
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
  .marking select {
    padding: 6px 8px;
  }
  .dot {
    width: 12px;
    height: 12px;
    border-radius: 50%;
    border: 1px solid rgba(0, 0, 0, 0.18);
    display: inline-block;
    flex-shrink: 0;
  }
  .dot.lg {
    width: 20px;
    height: 20px;
  }

  .queen-card {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 14px 16px;
  }
  .empty-inline {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 14px 16px;
    border: 1px dashed var(--line);
    border-radius: 14px;
  }
  .qhist {
    margin-top: 12px;
  }
  .qhist summary {
    cursor: pointer;
    color: var(--ink-soft);
    font-weight: 600;
    font-size: 0.88rem;
  }
  .qhist ul {
    list-style: none;
    margin-top: 10px;
    display: grid;
    gap: 8px;
  }
  .qhist li {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .qrwrap {
    padding: 16px;
    display: flex;
    justify-content: center;
    margin-bottom: 12px;
  }

  .timeline {
    list-style: none;
    display: grid;
    gap: 12px;
  }
  .visit {
    padding: 14px 16px;
  }
  .vhead {
    display: flex;
    align-items: baseline;
    gap: 10px;
    margin-bottom: 8px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chip {
    background: rgba(92, 107, 74, 0.12);
    color: #41502f;
    font-size: 0.76rem;
    font-weight: 600;
    padding: 4px 10px;
    border-radius: 999px;
  }
  .chip.warn {
    background: rgba(199, 127, 34, 0.16);
    color: var(--honey-d);
  }
  .vnote {
    margin-top: 8px;
    color: var(--ink-soft);
    font-size: 0.88rem;
    white-space: pre-wrap;
  }
  .err {
    color: #b5402f;
    font-size: 0.85rem;
    font-weight: 600;
  }

  .photos-edit {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .photos-edit .thumb {
    position: relative;
    width: 64px;
    height: 64px;
    border-radius: 10px;
    overflow: hidden;
  }
  .photos-edit .thumb img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .photos-edit .rm {
    position: absolute;
    top: 2px;
    right: 2px;
    width: 18px;
    height: 18px;
    line-height: 1;
    border: none;
    border-radius: 50%;
    background: rgba(0, 0, 0, 0.6);
    color: #fff;
    cursor: pointer;
    font-size: 0.8rem;
  }
  .addphoto {
    width: 64px;
    height: 64px;
    border: 1px dashed var(--line);
    border-radius: 10px;
    display: grid;
    place-items: center;
    cursor: pointer;
    color: var(--ink-soft);
    font-size: 0.68rem;
    text-align: center;
  }
  .vphotos {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 10px;
  }
  .vphoto {
    width: 76px;
    height: 76px;
    border-radius: 10px;
    overflow: hidden;
    border: 1px solid var(--line);
  }
  .vphoto img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }

  .locs {
    list-style: none;
    display: grid;
    gap: 2px;
  }
  .locs li {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    padding: 10px 0;
    border-bottom: 1px solid var(--line);
  }
  .locs li:last-child {
    border-bottom: none;
  }
  .loc-dot {
    width: 11px;
    height: 11px;
    border-radius: 50%;
    background: var(--ink-soft);
    margin-top: 4px;
    flex-shrink: 0;
  }
  .loc-dot.cur {
    background: var(--honey);
    box-shadow: 0 0 0 3px rgba(199, 127, 34, 0.18);
  }
  .loc-body strong {
    display: block;
    font-weight: 600;
  }
</style>
