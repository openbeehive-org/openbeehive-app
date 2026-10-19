// Data portability: export and import all local data.
//
// Formats
//   - JSON  : full, round-trippable backup of every entity
//   - CSV   : one file per entity (bundled as a .zip)
//   - XLSX  : one spreadsheet, one sheet per entity
//   - BeeXML: a structured, BeeXML-style XML interchange (apiary > hive > …)
//
// Import accepts an Openbeehive JSON backup, a BeeXML file, or a generic
// beekeeping CSV (migration from other apps, matched by column name).
//
// Everything runs locally against the offline-first store; imported rows go
// through the normal repo `patch`, so they sync like any other change.

import { getDB } from '../local/db';
import { patch } from '../local/repo';
import { buildZip } from './zip';
import { buildXlsx } from './xlsx';

const orgId = () => localStorage.getItem('obh.orgId') ?? 'local';
const userId = () => localStorage.getItem('obh.userId') ?? 'local';

type Row = Record<string, any>;
type ScopeFn = (r: Row, ctx: { hiveApiary: Record<string, string> }) => string;

// Entities in import dependency order. `cols` is the human-facing column set for
// CSV/XLSX (internal columns field_hlc/deleted/photo_keys are never exported).
type Entity = { table: string; cols: string[]; scope: ScopeFn };

export const ENTITIES: Entity[] = [
  {
    table: 'apiary',
    scope: (r) => r.id,
    cols: ['id', 'name', 'address', 'lat', 'lng', 'note', 'created_at', 'updated_at']
  },
  {
    table: 'hive',
    scope: (r) => r.apiary_id,
    cols: [
      'id',
      'apiary_id',
      'name',
      'type',
      'status',
      'boxes',
      'colony_origin',
      'note',
      'qr_code',
      'created_at',
      'updated_at'
    ]
  },
  {
    table: 'queen',
    scope: (r, c) => c.hiveApiary[r.hive_id] || '',
    cols: [
      'id',
      'hive_id',
      'year',
      'marking',
      'origin',
      'breeder_number',
      'introduced_at',
      'replaced_at',
      'active',
      'note',
      'created_at',
      'updated_at'
    ]
  },
  {
    table: 'placement',
    scope: (r) => r.apiary_id,
    cols: ['id', 'hive_id', 'apiary_id', 'start_at', 'end_at']
  },
  {
    table: 'inspection',
    scope: (r, c) => c.hiveApiary[r.hive_id] || '',
    cols: [
      'id',
      'hive_id',
      'date',
      'weather',
      'queen_seen',
      'eggs_seen',
      'temperament',
      'frames',
      'stores',
      'queen_cells',
      'varroa',
      'honey_kg',
      'brood_frames',
      'calmness',
      'fed_kg',
      'frames_added',
      'frames_removed',
      'drone_frame_cut',
      'super_added',
      'weight_kg',
      'youngest_larva',
      'covered_larva',
      'temp_hive',
      'temp_outside',
      'humidity_hive',
      'humidity_outside',
      'note',
      'created_at'
    ]
  },
  {
    table: 'harvest',
    scope: (r) => r.apiary_id,
    cols: [
      'id',
      'apiary_id',
      'hive_id',
      'queen_id',
      'date',
      'variety',
      'amount_kg',
      'water_content',
      'batch_number',
      'best_before',
      'note'
    ]
  },
  {
    table: 'treatment',
    scope: (r) => r.apiary_id,
    cols: [
      'id',
      'apiary_id',
      'hive_id',
      'queen_id',
      'date',
      'product',
      'active_ingredient',
      'dose',
      'method',
      'batch_number',
      'withdrawal_until',
      'reason',
      'note'
    ]
  },
  {
    table: 'task',
    scope: (r) => r.apiary_id || 'user:' + userId(),
    cols: [
      'id',
      'title',
      'apiary_id',
      'hive_id',
      'due_at',
      'done',
      'priority',
      'note',
      'recurrence',
      'assigned_to',
      'created_at'
    ]
  },
  {
    table: 'event',
    scope: (r) => r.scope_id,
    cols: [
      'id',
      'scope_id',
      'type',
      'date',
      'apiary_id',
      'hive_id',
      'queen_id',
      'ref_entity',
      'ref_id',
      'title',
      'amount_kg',
      'detail',
      'author_id'
    ]
  }
];

const INTERNAL = new Set(['field_hlc', 'deleted', 'photo_keys']);

// --- gather ---

export type Dataset = Record<string, Row[]>;

export async function gatherAll(): Promise<Dataset> {
  const db = await getDB();
  const out: Dataset = {};
  for (const e of ENTITIES) {
    const rows = await db.all<Row>(`SELECT * FROM ${e.table} WHERE deleted = 0`);
    out[e.table] = rows.map((r) => {
      const o: Row = {};
      for (const k of Object.keys(r)) if (!INTERNAL.has(k)) o[k] = r[k];
      return o;
    });
  }
  return out;
}

function hiveApiaryMap(data: Dataset): Record<string, string> {
  const m: Record<string, string> = {};
  for (const h of data.hive ?? []) m[h.id] = h.apiary_id;
  return m;
}

// --- download helper ---

export function downloadBlob(filename: string, blob: Blob) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

const stamp = () => new Date().toISOString().slice(0, 10);

// --- CSV ---

function csvCell(v: any): string {
  if (v === null || v === undefined) return '';
  const s = String(v);
  return /[",\n\r]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
}

export function toCSV(cols: string[], rows: Row[]): string {
  const head = cols.join(',');
  const body = rows.map((r) => cols.map((c) => csvCell(r[c])).join(',')).join('\n');
  return head + '\n' + body + '\n';
}

// --- exporters ---

export async function exportJSON() {
  const data = await gatherAll();
  const doc = {
    openbeehive_export: true,
    schema_version: '0.1.0',
    exported_at: new Date().toISOString(),
    data
  };
  downloadBlob(
    `openbeehive-backup-${stamp()}.json`,
    new Blob([JSON.stringify(doc, null, 2)], { type: 'application/json' })
  );
}

export async function exportCSVZip() {
  const data = await gatherAll();
  const entries = ENTITIES.filter((e) => (data[e.table] ?? []).length).map((e) => ({
    name: `${e.table}.csv`,
    data: toCSV(e.cols, data[e.table] ?? [])
  }));
  if (!entries.length)
    entries.push({ name: 'apiary.csv', data: toCSV(ENTITIES[0]?.cols ?? [], []) });
  downloadBlob(`openbeehive-csv-${stamp()}.zip`, buildZip(entries));
}

export async function exportXLSX() {
  const data = await gatherAll();
  const sheets = ENTITIES.map((e) => ({
    name: e.table,
    rows: [e.cols, ...(data[e.table] ?? []).map((r) => e.cols.map((c) => r[c] ?? ''))]
  }));
  downloadBlob(`openbeehive-${stamp()}.xlsx`, buildXlsx(sheets));
}

const xmlEsc = (s: any) =>
  String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');

export async function exportBeeXML() {
  const data = await gatherAll();
  const hivesByApiary: Record<string, Row[]> = {};
  for (const h of data.hive ?? []) (hivesByApiary[h.apiary_id] ??= []).push(h);
  const queensByHive: Record<string, Row[]> = {};
  for (const q of data.queen ?? []) (queensByHive[q.hive_id] ??= []).push(q);
  const inspByHive: Record<string, Row[]> = {};
  for (const i of data.inspection ?? []) (inspByHive[i.hive_id] ??= []).push(i);

  const field = (name: string, v: any) =>
    v === null || v === undefined || v === '' ? '' : `<${name}>${xmlEsc(v)}</${name}>`;

  const lines: string[] = [
    '<?xml version="1.0" encoding="UTF-8"?>',
    '<!-- BeeXML-style interchange export from Openbeehive. -->',
    `<beexml generator="Openbeehive" version="0.1.0" exported="${new Date().toISOString()}">`
  ];
  for (const a of data.apiary ?? []) {
    lines.push(`  <apiary id="${xmlEsc(a.id)}">`);
    lines.push(
      `    ${field('name', a.name)}${field('latitude', a.lat)}${field('longitude', a.lng)}${field('address', a.address)}${field('note', a.note)}`
    );
    for (const h of hivesByApiary[a.id] ?? []) {
      lines.push(`    <hive id="${xmlEsc(h.id)}">`);
      lines.push(
        `      ${field('name', h.name)}${field('type', h.type)}${field('status', h.status)}${field('note', h.note)}`
      );
      for (const q of queensByHive[h.id] ?? [])
        lines.push(
          `      <queen id="${xmlEsc(q.id)}">${field('year', q.year)}${field('marking', q.marking)}${field('origin', q.origin)}${field('active', q.active)}</queen>`
        );
      for (const i of inspByHive[h.id] ?? [])
        lines.push(
          `      <inspection id="${xmlEsc(i.id)}">${field('date', i.date)}${field('tempHive', i.temp_hive)}${field('tempOutside', i.temp_outside)}${field('humidityHive', i.humidity_hive)}${field('humidityOutside', i.humidity_outside)}${field('varroa', i.varroa)}${field('note', i.note)}</inspection>`
        );
      lines.push('    </hive>');
    }
    lines.push('  </apiary>');
  }
  lines.push('</beexml>');
  downloadBlob(
    `openbeehive-beexml-${stamp()}.xml`,
    new Blob([lines.join('\n')], { type: 'application/xml' })
  );
}

// --- PDF report (via the browser's print / "Save as PDF") ---

const HTML_ESC = (s: any) =>
  String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');

const MARKING = ['', 'White', 'Yellow', 'Red', 'Green', 'Blue'];

// Build a clean, branded report of apiaries → hives (current queen, latest
// reading) and open it in a new window for printing or saving as PDF.
export async function printReport() {
  const data = await gatherAll();
  const hivesByApiary: Record<string, Row[]> = {};
  for (const h of data.hive ?? []) (hivesByApiary[h.apiary_id] ??= []).push(h);
  const queenByHive: Record<string, Row> = {};
  for (const q of data.queen ?? []) if (q.active) queenByHive[q.hive_id] = q;
  const lastInspByHive: Record<string, Row> = {};
  for (const i of data.inspection ?? []) {
    const cur = lastInspByHive[i.hive_id];
    if (!cur || String(i.date) > String(cur.date)) lastInspByHive[i.hive_id] = i;
  }

  const rows = (apiaryId: string) =>
    (hivesByApiary[apiaryId] ?? [])
      .map((h) => {
        const q = queenByHive[h.id];
        const i = lastInspByHive[h.id] ?? {};
        const climate = [
          i.temp_hive != null ? `${i.temp_hive}°C` : '',
          i.humidity_hive != null ? `${i.humidity_hive}%` : '',
          i.weight_kg ? `${i.weight_kg}kg` : ''
        ]
          .filter(Boolean)
          .join(' · ');
        return `<tr><td>${HTML_ESC(h.name)}</td>
      <td>${q ? HTML_ESC((q.year || '') + ' ' + (MARKING[q.marking] || '')) : '—'}</td>
      <td>${HTML_ESC(i.date ? String(i.date).slice(0, 10) : '—')}</td>
      <td>${HTML_ESC(climate || '—')}</td>
      <td>${HTML_ESC(i.note ?? '')}</td></tr>`;
      })
      .join('');

  const sections = (data.apiary ?? [])
    .map(
      (a) => `
    <section>
      <h2>${HTML_ESC(a.name)}</h2>
      ${a.address ? `<p class="addr">${HTML_ESC(a.address)}</p>` : ''}
      <table>
        <thead><tr><th>Hive</th><th>Queen</th><th>Last visit</th><th>Climate / weight</th><th>Note</th></tr></thead>
        <tbody>${rows(a.id) || '<tr><td colspan="5">No hives.</td></tr>'}</tbody>
      </table>
    </section>`
    )
    .join('');

  const html = `<!doctype html><html><head><meta charset="utf-8">
    <title>Openbeehive report</title>
    <style>
      /* System fonts only - the exported file must not load remote resources. */
      body{font-family:system-ui,-apple-system,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif;color:#2c2316;margin:32px;}
      h1{font-family:Georgia,'Times New Roman',serif;color:#a4641a;margin:0 0 2px;}
      h2{font-family:Georgia,'Times New Roman',serif;font-size:1.2rem;margin:22px 0 6px;border-bottom:2px solid #e5dcc6;padding-bottom:3px;}
      .meta{color:#6b5e48;font-size:.85rem;margin-bottom:8px;}
      .addr{color:#6b5e48;font-size:.85rem;margin:0 0 6px;}
      table{width:100%;border-collapse:collapse;font-size:.84rem;}
      th{text-align:left;color:#6b5e48;border-bottom:1px solid #e5dcc6;padding:6px 8px;}
      td{padding:6px 8px;border-bottom:1px solid #f0ead9;vertical-align:top;}
      @media print{body{margin:12mm;} section{break-inside:avoid;}}
    </style></head>
    <body>
      <h1>⬡ Openbeehive</h1>
      <div class="meta">Apiary report · ${new Date().toLocaleDateString()} · ${(data.apiary ?? []).length} apiaries, ${(data.hive ?? []).length} hives</div>
      ${sections || '<p>No apiaries yet.</p>'}
      <script>window.onload=function(){window.print();}</script>
    </body></html>`;

  const w = window.open('', '_blank');
  if (!w) throw new Error('Pop-up blocked — allow pop-ups to print the report.');
  w.document.write(html);
  w.document.close();
}

// --- import: write a dataset into the local store ---

export type ImportResult = Record<string, number>;

// Write an Openbeehive-shaped dataset. Rows keep their ids (round-trip /
// idempotent restore); scope is resolved per entity. organization_id is set to
// the importing account so the data becomes yours.
export async function importDataset(data: Dataset): Promise<ImportResult> {
  const ctx = { hiveApiary: hiveApiaryMap(data) };
  const result: ImportResult = {};
  for (const e of ENTITIES) {
    const rows = data[e.table] ?? [];
    let n = 0;
    for (const r0 of rows) {
      const r = { ...r0 };
      const id = r.id || crypto.randomUUID();
      delete r.id;
      for (const k of Object.keys(r)) if (INTERNAL.has(k)) delete r[k];
      const fields: Row = { ...r, organization_id: orgId(), deleted: 0 };
      if (e.table === 'inspection') fields.photo_keys = '{}';
      await patch(e.table, id, e.scope({ ...r0, id }, ctx), fields);
      n++;
    }
    if (n) result[e.table] = n;
  }
  return result;
}

export async function importJSON(text: string): Promise<ImportResult> {
  const doc = JSON.parse(text);
  const data: Dataset = doc?.data ?? doc;
  if (!data || typeof data !== 'object') throw new Error('Not a valid Openbeehive backup.');
  return importDataset(data);
}

// --- BeeXML import ---

export async function importBeeXML(text: string): Promise<ImportResult> {
  const doc = new DOMParser().parseFromString(text, 'application/xml');
  if (doc.querySelector('parsererror')) throw new Error('Invalid XML.');
  const t = (el: Element | null, tag: string) =>
    el?.querySelector(':scope > ' + tag)?.textContent?.trim() ?? '';
  const data = {
    apiary: [] as Row[],
    hive: [] as Row[],
    queen: [] as Row[],
    inspection: [] as Row[]
  } satisfies Dataset;

  for (const a of Array.from(doc.querySelectorAll('beexml > apiary, apiary'))) {
    const aid = a.getAttribute('id') || crypto.randomUUID();
    data.apiary.push({
      id: aid,
      name: t(a, 'name'),
      lat: +t(a, 'latitude') || 0,
      lng: +t(a, 'longitude') || 0,
      address: t(a, 'address'),
      note: t(a, 'note')
    });
    for (const h of Array.from(a.querySelectorAll(':scope > hive'))) {
      const hid = h.getAttribute('id') || crypto.randomUUID();
      data.hive.push({
        id: hid,
        apiary_id: aid,
        name: t(h, 'name'),
        type: +t(h, 'type') || 0,
        status: +t(h, 'status') || 0,
        note: t(h, 'note')
      });
      for (const q of Array.from(h.querySelectorAll(':scope > queen')))
        data.queen.push({
          id: q.getAttribute('id') || crypto.randomUUID(),
          hive_id: hid,
          year: +t(q, 'year') || 0,
          marking: +t(q, 'marking') || 0,
          origin: t(q, 'origin'),
          active: +t(q, 'active') || 0
        });
      for (const i of Array.from(h.querySelectorAll(':scope > inspection')))
        data.inspection.push({
          id: i.getAttribute('id') || crypto.randomUUID(),
          hive_id: hid,
          date: t(i, 'date'),
          temp_hive: num(t(i, 'tempHive')),
          temp_outside: num(t(i, 'tempOutside')),
          humidity_hive: num(t(i, 'humidityHive')),
          humidity_outside: num(t(i, 'humidityOutside')),
          varroa: t(i, 'varroa'),
          note: t(i, 'note')
        });
    }
  }
  return importDataset(data);
}

const num = (s: string) => (s === '' || s == null ? null : Number(s));

// --- generic beekeeping CSV import (migration from other apps) ---

function parseCSV(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let cur = '';
  let q = false;
  text = text.replace(/^\uFEFF/, '');
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (q) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          cur += '"';
          i++;
        } else q = false;
      } else cur += ch;
    } else if (ch === '"') q = true;
    else if (ch === ',') {
      row.push(cur);
      cur = '';
    } else if (ch === '\n') {
      row.push(cur);
      rows.push(row);
      row = [];
      cur = '';
    } else if (ch === '\r') {
      /* skip */
    } else cur += ch;
  }
  if (cur !== '' || row.length) {
    row.push(cur);
    rows.push(row);
  }
  return rows.filter((r) => r.some((c) => c.trim() !== ''));
}

const norm = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, '');

// Column aliases recognised across common apps (Apiary Book, HiveBook, BeeKeeperPal,
// spreadsheets). Header matching is case/space/punctuation-insensitive.
const ALIASES: Record<string, string[]> = {
  apiary: ['apiary', 'apiaryname', 'yard', 'location', 'standort', 'rucher', 'colmenar', 'apiario'],
  hive: ['hive', 'hivename', 'colony', 'colonyname', 'beute', 'volk', 'ruche', 'colmena', 'arnia'],
  date: ['date', 'inspectiondate', 'visitdate', 'datum', 'fecha', 'data'],
  weather: ['weather', 'wetter', 'meteo', 'tiempo'],
  note: ['note', 'notes', 'comment', 'comments', 'remarks', 'notiz', 'bemerkung'],
  varroa: ['varroa', 'mites', 'mitecount', 'varroacount'],
  temp_hive: ['hivetemp', 'hivetemperature', 'temphive', 'broodtemp'],
  temp_outside: ['temperature', 'temp', 'outsidetemp', 'ambienttemp', 'tempoutside'],
  humidity_hive: ['hivehumidity', 'humidityhive'],
  humidity_outside: ['humidity', 'outsidehumidity', 'humidityoutside'],
  weight_kg: ['weight', 'hiveweight', 'weightkg', 'gewicht'],
  honey_kg: ['honey', 'honeykg', 'harvest', 'yield', 'honig', 'ernte']
};

function resolveHeaders(headers: string[]): Record<number, string> {
  const map: Record<number, string> = {};
  headers.forEach((h, idx) => {
    const n = norm(h);
    for (const [field, names] of Object.entries(ALIASES))
      if (names.includes(n)) {
        map[idx] = field;
        break;
      }
  });
  return map;
}

// Build apiaries / hives / inspections from a flat CSV of visits.
export async function importBeekeepingCSV(text: string): Promise<ImportResult> {
  const rows = parseCSV(text);
  if (rows.length < 2) throw new Error('CSV has no data rows.');
  const header = rows[0];
  if (!header) throw new Error('CSV has no header row.');
  const colMap = resolveHeaders(header);
  const fields = Object.values(colMap);
  if (!fields.includes('hive') && !fields.includes('date'))
    throw new Error('Could not recognise this CSV. Expected at least a hive or date column.');

  const data = {
    apiary: [] as Row[],
    hive: [] as Row[],
    queen: [] as Row[],
    placement: [] as Row[],
    inspection: [] as Row[],
    harvest: [] as Row[],
    task: [] as Row[],
    event: [] as Row[]
  } satisfies Dataset;
  const apiaryByName: Record<string, string> = {};
  const hiveByName: Record<string, string> = {};
  const DEFAULT_APIARY = 'Imported';

  const ensureApiary = (name: string): string => {
    name = name || DEFAULT_APIARY;
    let id = apiaryByName[name];
    if (!id) {
      id = crypto.randomUUID();
      apiaryByName[name] = id;
      data.apiary.push({ id, name });
    }
    return id;
  };
  const ensureHive = (name: string, apiaryId: string): string => {
    const key = apiaryId + '/' + name;
    let id = hiveByName[key];
    if (!id) {
      id = crypto.randomUUID();
      hiveByName[key] = id;
      data.hive.push({ id, apiary_id: apiaryId, name: name || 'Hive' });
    }
    return id;
  };

  for (let r = 1; r < rows.length; r++) {
    const get = (f: string) => {
      const idx = Object.keys(colMap).find((k) => colMap[+k] === f);
      return idx !== undefined ? (rows[r]?.[+idx]?.trim() ?? '') : '';
    };
    const apiaryId = ensureApiary(get('apiary'));
    const hiveId = ensureHive(get('hive') || 'Hive ' + r, apiaryId);
    const insp: Row = { id: crypto.randomUUID(), hive_id: hiveId };
    let any = false;
    for (const f of ['date', 'weather', 'note', 'varroa']) {
      const v = get(f);
      if (v) {
        insp[f] = v;
        any = true;
      }
    }
    for (const f of [
      'temp_hive',
      'temp_outside',
      'humidity_hive',
      'humidity_outside',
      'weight_kg',
      'honey_kg'
    ]) {
      const v = get(f);
      if (v !== '' && !isNaN(+v)) {
        insp[f] = +v;
        any = true;
      }
    }
    if (any) data.inspection.push(insp);
  }
  return importDataset(data);
}

// --- dispatch by file ---

export type ImportMode = 'auto' | 'json' | 'beexml' | 'csv';

export async function importFile(file: File, mode: ImportMode = 'auto'): Promise<ImportResult> {
  const text = await file.text();
  const ext = file.name.toLowerCase().split('.').pop() ?? '';
  let m = mode;
  if (m === 'auto') {
    if (ext === 'json' || text.trimStart().startsWith('{')) m = 'json';
    else if (ext === 'xml' || text.trimStart().startsWith('<')) m = 'beexml';
    else m = 'csv';
  }
  if (m === 'json') return importJSON(text);
  if (m === 'beexml') return importBeeXML(text);
  return importBeekeepingCSV(text);
}
