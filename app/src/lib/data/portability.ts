// Data portability: export all local data.
//
// Formats
//   - JSON  : every record of the workspace (backup.ts; with photos as a ZIP)
//   - CSV   : one file per entity (bundled as a .zip)
//   - XLSX  : one spreadsheet, one sheet per entity
//   - BeeXML: a structured, BeeXML-style XML interchange (apiary > hive > ...)
//
// Import (importer.ts) accepts an Openbeehive export, a BeeXML file, or a
// generic beekeeping CSV (migration from other apps, matched by column name).
//
// Everything runs locally against the offline-first store. Values are
// exported as stored: metric, whatever units the user displays.

import { getDB } from '../local/db';
import { buildZip } from './zip';
import { buildXlsx } from './xlsx';
import { todayLocal } from '../dates';

export const orgId = () => localStorage.getItem('obh.orgId') ?? 'local';
export const userId = () => localStorage.getItem('obh.userId') ?? 'local';

export type Row = Record<string, any>;
type ScopeFn = (r: Row, ctx: { hiveApiary: Record<string, string> }) => string;

// Entities in import dependency order. `cols` is the human-facing column set for
// CSV/XLSX (internal columns are never exported, see INTERNAL).
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

// Bookkeeping columns, and the inline photo columns of 0.4.x (photos are
// records of their own now), are neither exported nor imported.
export const INTERNAL = new Set(['field_hlc', 'deleted', 'photo_keys', 'photo']);

// --- gather ---

export type Dataset = Record<string, Row[]>;

// Personal tasks of other workspaces never reach this database, but rows
// written by an older version may still be there.
const IN_WORKSPACE = `(COALESCE(apiary_id, '') <> '' OR organization_id = ?)`;

/** The live records of the workspace; with `photos` also the photo records. */
export async function gatherAll(opts: { photos?: boolean } = {}): Promise<Dataset> {
  const db = await getDB();
  const out: Dataset = {};
  const tables = ENTITIES.map((e) => e.table);
  if (opts.photos) tables.push('photo');
  for (const table of tables) {
    const rows =
      table === 'task'
        ? await db.all<Row>(`SELECT * FROM task WHERE deleted = 0 AND ${IN_WORKSPACE}`, [orgId()])
        : await db.all<Row>(`SELECT * FROM ${table} WHERE deleted = 0`);
    out[table] = rows.map((r) => {
      const o: Row = {};
      for (const k of Object.keys(r)) if (!INTERNAL.has(k)) o[k] = r[k];
      return o;
    });
  }
  return out;
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

export const stamp = () => todayLocal();

// --- CSV ---

function csvCell(v: unknown): string {
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

const xmlEsc = (s: unknown) =>
  String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');

export async function exportBeeXML() {
  const data = await gatherAll();
  const hivesByApiary: Record<string, Row[]> = {};
  for (const h of data.hive ?? []) (hivesByApiary[h.apiary_id] ??= []).push(h);
  const queensByHive: Record<string, Row[]> = {};
  for (const q of data.queen ?? []) (queensByHive[q.hive_id] ??= []).push(q);
  const inspByHive: Record<string, Row[]> = {};
  for (const i of data.inspection ?? []) (inspByHive[i.hive_id] ??= []).push(i);

  const field = (name: string, v: unknown) =>
    v === null || v === undefined || v === '' ? '' : `<${name}>${xmlEsc(v)}</${name}>`;

  const lines: string[] = [
    '<?xml version="1.0" encoding="UTF-8"?>',
    '<!-- BeeXML-style interchange export from Openbeehive. -->',
    `<beexml generator="Openbeehive" version="0.1.0" exported="${new Date().toISOString()}" workspace="${xmlEsc(orgId())}">`
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
