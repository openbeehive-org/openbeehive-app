// Import into the active workspace, in three steps the user can follow:
//
//   1. readImportFile: the file becomes a dataset (an Openbeehive export as
//      JSON or ZIP with photos, a BeeXML file, or a CSV of visits from
//      another app). Nothing is written.
//   2. planImport: what writing it would do, record by record, shown to the
//      user as a preview. By default only records whose id is not on the
//      device are created; existing ones (live or deleted) are left alone.
//      "Overwrite" writes the fields of the file that differ; deleted
//      records stay deleted unless "restore deleted" is ticked too. A file
//      from another workspace gets new ids (derived from the old ones, so
//      importing it twice creates nothing the second time) with every
//      reference between its records changed to match.
//   3. applyImport: everything in one local transaction, synced like any
//      other change. What it created is remembered so that undoLastImport
//      can delete exactly that again, also offline.
import { getDB } from '../local/db';
import { unit } from '../local/write';
import { columnsOf, toScalar } from '../local/tables';
import { photoRecords } from '../local/repo';
import { ENTITIES, INTERNAL, orgId, userId, type Dataset, type Row } from './portability';
import { looksLikeZip, readZip } from './unzip';
import { EXPORT_DOCUMENT, PHOTO_DIR } from './backup';

export type ImportMode = 'auto' | 'json' | 'beexml' | 'csv';
export type SourceKind = 'json' | 'zip' | 'beexml' | 'csv';

export type ParsedImport = {
  kind: SourceKind;
  data: Dataset;
  /** The workspace the records come from; null when the file does not say. */
  fromWorkspace: string | null;
  /** Full-size images by photo id as the file names them. */
  images: Map<string, Blob>;
  /** The ids are this workspace's own (a CSV matched against the device). */
  local?: boolean;
};

export type ImportOptions = {
  /** Write the file's values over existing records. */
  overwrite?: boolean;
  /** With overwrite: bring deleted records back. */
  restoreDeleted?: boolean;
};

export type TableCounts = { create: number; update: number; restore: number; skip: number };

type OpKind = 'create' | 'update' | 'restore';
type Op = { kind: OpKind; entity: string; id: string; scope: string; fields: Row };

export type ImportPlan = {
  kind: SourceKind;
  /** The workspace the records go into. */
  workspace: string;
  /** The file comes from another workspace: its records get new ids. */
  foreign: boolean;
  counts: Record<string, TableCounts>;
  total: TableCounts;
  ops: Op[];
  /** Images of the photos the import creates, by their (new) id. */
  images: Map<string, Blob>;
  /** Photos created with their thumbnail only: the file has no image. */
  photosWithoutImage: number;
};

/** Where the images of imported photos go: lib/photos, or a fake in tests. */
export interface ImageSink {
  keep(id: string, image: Blob): Promise<void>;
  drop(id: string): Promise<void>;
}

const defaultSink = async (): Promise<ImageSink> => (await import('../photos/index')).photoImages;

// Tables in the order they are written: what is referred to comes first.
const TABLES = [...ENTITIES.map((e) => e.table), 'photo'];

// Columns that hold the id of another record.
const REFS: Record<string, string[]> = {
  hive: ['apiary_id'],
  queen: ['hive_id'],
  placement: ['hive_id', 'apiary_id'],
  inspection: ['hive_id'],
  harvest: ['apiary_id', 'hive_id', 'queen_id'],
  treatment: ['apiary_id', 'hive_id', 'queen_id'],
  task: ['apiary_id', 'hive_id'],
  event: ['apiary_id', 'hive_id', 'queen_id', 'ref_id', 'scope_id'],
  photo: ['owner_id']
};

const MAX_IMAGE_BYTES = 5 * 1024 * 1024;
const MAX_THUMB_CHARS = 48 * 1024;

// ---------------------------------------------------------------------------
// 1. Reading files

export class ImportFormatError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ImportFormatError';
  }
}

/** Turns a picked file into a dataset. Nothing is written. */
export async function readImportFile(file: Blob, mode: ImportMode = 'auto'): Promise<ParsedImport> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  if (looksLikeZip(bytes)) return parseArchive(bytes);
  const text = new TextDecoder().decode(bytes).replace(/^\uFEFF/, '');
  const name = (file as File).name?.toLowerCase() ?? '';
  const ext = name.split('.').pop() ?? '';
  let m = mode;
  if (m === 'auto') {
    if (ext === 'json' || text.trimStart().startsWith('{')) m = 'json';
    else if (ext === 'xml' || text.trimStart().startsWith('<')) m = 'beexml';
    else m = 'csv';
  }
  if (m === 'json') return parseExportDocument(text, new Map());
  if (m === 'beexml') return parseBeeXML(text);
  return parseBeekeepingCSV(text);
}

/** An export ZIP: the document plus photos/<id>.<ext>. */
export async function parseArchive(bytes: Uint8Array): Promise<ParsedImport> {
  const files = await readZip(bytes);
  const docName = files.has(EXPORT_DOCUMENT)
    ? EXPORT_DOCUMENT
    : [...files.keys()].find((n) => n.endsWith('.json') && !n.includes('/'));
  if (!docName) throw new ImportFormatError('The archive contains no Openbeehive export.');
  const images = new Map<string, Blob>();
  for (const [name, data] of files) {
    if (!name.startsWith(PHOTO_DIR)) continue;
    const base = name.slice(PHOTO_DIR.length).replace(/\.[a-z0-9]+$/i, '');
    const type = imageType(data);
    if (!base || !type || data.length > MAX_IMAGE_BYTES) continue;
    let id: string;
    try {
      id = decodeURIComponent(base);
    } catch {
      continue;
    }
    images.set(id, new Blob([data as BlobPart], { type }));
  }
  const parsed = parseExportDocument(new TextDecoder().decode(files.get(docName)!), images);
  return { ...parsed, kind: 'zip' };
}

// The image formats the server takes, told by their first bytes.
function imageType(b: Uint8Array): string {
  if (b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return 'image/jpeg';
  if (b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e && b[3] === 0x47) return 'image/png';
  const ascii = (from: number, to: number) => String.fromCharCode(...b.subarray(from, to));
  if (b.length > 12 && ascii(0, 4) === 'RIFF' && ascii(8, 12) === 'WEBP') return 'image/webp';
  return '';
}

/** An Openbeehive JSON export (any version), or a bare dataset. */
export function parseExportDocument(text: string, images: Map<string, Blob>): ParsedImport {
  let doc: unknown;
  try {
    doc = JSON.parse(text);
  } catch {
    throw new ImportFormatError('The file is not valid JSON.');
  }
  const obj = isObject(doc) ? doc : null;
  const data = obj && isObject(obj.data) ? obj.data : obj;
  if (!data || !TABLES.some((t) => Array.isArray(data[t]))) {
    throw new ImportFormatError('The file is not an Openbeehive export.');
  }
  const clean: Dataset = {};
  for (const t of TABLES) {
    const rows = data[t];
    if (Array.isArray(rows)) clean[t] = rows.filter(isObject) as Row[];
  }
  const declared = obj && typeof obj.workspace_id === 'string' ? obj.workspace_id : '';
  return { kind: 'json', data: clean, fromWorkspace: declared || workspaceOfRows(clean), images };
}

const isObject = (v: unknown): v is Record<string, unknown> =>
  !!v && typeof v === 'object' && !Array.isArray(v);

// Exports before 0.5 do not name their workspace; their rows do.
function workspaceOfRows(data: Dataset): string | null {
  const votes = new Map<string, number>();
  for (const rows of Object.values(data))
    for (const r of rows) {
      const o = r.organization_id;
      if (typeof o === 'string' && o) votes.set(o, (votes.get(o) ?? 0) + 1);
    }
  let best: string | null = null;
  for (const [o, n] of votes) if (best === null || n > votes.get(best)!) best = o;
  return best;
}

// --- BeeXML ---

const num = (s: string) => (s === '' || s == null ? null : Number(s));

export function parseBeeXML(text: string): ParsedImport {
  const doc = new DOMParser().parseFromString(text, 'application/xml');
  if (doc.querySelector('parsererror')) throw new ImportFormatError('Invalid XML.');
  const t = (el: Element | null, tag: string) =>
    el?.querySelector(':scope > ' + tag)?.textContent?.trim() ?? '';
  const data = {
    apiary: [] as Row[],
    hive: [] as Row[],
    queen: [] as Row[],
    inspection: [] as Row[]
  } satisfies Dataset;

  // Elements without an id get one derived from their position, so the same
  // file always yields the same records.
  let n = 0;
  const idOf = (el: Element) => el.getAttribute('id') || `beexml-${++n}`;
  for (const a of Array.from(doc.querySelectorAll('apiary'))) {
    const aid = idOf(a);
    data.apiary.push({
      id: aid,
      name: t(a, 'name'),
      lat: +t(a, 'latitude') || 0,
      lng: +t(a, 'longitude') || 0,
      address: t(a, 'address'),
      note: t(a, 'note')
    });
    for (const h of Array.from(a.querySelectorAll(':scope > hive'))) {
      const hid = idOf(h);
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
          id: idOf(q),
          hive_id: hid,
          year: +t(q, 'year') || 0,
          marking: +t(q, 'marking') || 0,
          origin: t(q, 'origin'),
          active: +t(q, 'active') || 0
        });
      for (const i of Array.from(h.querySelectorAll(':scope > inspection')))
        data.inspection.push({
          id: idOf(i),
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
  const ws = doc.documentElement.getAttribute('workspace');
  return { kind: 'beexml', data, fromWorkspace: ws || null, images: new Map() };
}

// --- CSV of visits from another app ---

export function parseCSV(text: string): string[][] {
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

const TEXT_FIELDS = ['date', 'weather', 'note', 'varroa'];
const NUMBER_FIELDS = [
  'temp_hive',
  'temp_outside',
  'humidity_hive',
  'humidity_outside',
  'weight_kg',
  'honey_kg'
];

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

/** Names are matched trimmed and case-insensitively. */
const nameKey = (s: unknown) =>
  String(s ?? '')
    .trim()
    .toLowerCase();

const dayOf = (s: unknown) => {
  const v = String(s ?? '').trim();
  return /^\d{4}-\d{2}-\d{2}/.test(v) ? v.slice(0, 10) : v;
};

// A visit of the file is the same as a recorded one when the hive and day
// match and every value the file has for it is the same.
function sameVisit(file: Row, row: Row): boolean {
  if (file.hive_id !== row.hive_id || dayOf(file.date) !== dayOf(row.date)) return false;
  for (const f of TEXT_FIELDS)
    if (f !== 'date' && f in file && nameKey(file[f]) !== nameKey(row[f])) return false;
  for (const f of NUMBER_FIELDS) if (f in file && Number(file[f]) !== Number(row[f])) return false;
  return true;
}

/**
 * Apiaries, hives and visits from a flat CSV of visits. Apiaries and hives
 * that exist (by name) are used instead of creating new ones, and a visit
 * that is already recorded is not recorded again, so importing the same
 * file twice changes nothing.
 */
export async function parseBeekeepingCSV(text: string): Promise<ParsedImport> {
  const rows = parseCSV(text);
  if (rows.length < 2) throw new ImportFormatError('CSV has no data rows.');
  const colMap = resolveHeaders(rows[0]!);
  const fields = Object.values(colMap);
  if (!fields.includes('hive') && !fields.includes('date'))
    throw new ImportFormatError(
      'Could not recognise this CSV. Expected at least a hive or date column.'
    );

  const db = await getDB();
  const data = {
    apiary: [] as Row[],
    hive: [] as Row[],
    inspection: [] as Row[]
  } satisfies Dataset;
  const listed = new Set<string>();
  const list = (table: 'apiary' | 'hive' | 'inspection', row: Row) => {
    if (listed.has(row.id)) return;
    listed.add(row.id);
    data[table].push(row);
  };

  const apiaries = await db.all<Row>(
    `SELECT id, name FROM apiary WHERE deleted = 0 ORDER BY created_at, id`
  );
  const hives = await db.all<Row>(
    `SELECT id, name, apiary_id FROM hive WHERE deleted = 0 ORDER BY created_at, id`
  );
  const DEFAULT_APIARY = 'Imported';

  const ensureApiary = (name: string): string => {
    name = name.trim() || DEFAULT_APIARY;
    let a = apiaries.find((x) => nameKey(x.name) === nameKey(name));
    if (!a) {
      a = { id: crypto.randomUUID(), name };
      apiaries.push(a);
    }
    // Existing apiaries are listed with their own name, so that even
    // "overwrite" leaves them as they are.
    list('apiary', { id: a.id, name: a.name });
    return a.id;
  };
  const ensureHive = (name: string, apiaryName: string | null): string => {
    const apiaryId = apiaryName === null ? null : ensureApiary(apiaryName);
    let h = hives.find(
      (x) => nameKey(x.name) === nameKey(name) && (apiaryId === null || x.apiary_id === apiaryId)
    );
    if (!h) {
      h = { id: crypto.randomUUID(), name: name.trim(), apiary_id: apiaryId ?? ensureApiary('') };
      hives.push(h);
    }
    list('hive', { id: h.id, apiary_id: h.apiary_id, name: h.name });
    return h.id;
  };

  const visitsOf = new Map<string, Row[]>();
  const visits = async (hiveId: string) => {
    let v = visitsOf.get(hiveId);
    if (!v) {
      v = await db.all<Row>(`SELECT * FROM inspection WHERE deleted = 0 AND hive_id = ?`, [hiveId]);
      visitsOf.set(hiveId, v);
    }
    return v;
  };

  const hasApiary = fields.includes('apiary');
  for (let r = 1; r < rows.length; r++) {
    const get = (f: string) => {
      const idx = Object.keys(colMap).find((k) => colMap[+k] === f);
      return idx !== undefined ? (rows[r]?.[+idx]?.trim() ?? '') : '';
    };
    const hiveId = ensureHive(get('hive') || 'Hive ' + r, hasApiary ? get('apiary') : null);
    const insp: Row = { hive_id: hiveId };
    let any = false;
    for (const f of TEXT_FIELDS) {
      const v = get(f);
      if (v) {
        insp[f] = v;
        any = true;
      }
    }
    for (const f of NUMBER_FIELDS) {
      const v = get(f);
      if (v !== '' && !isNaN(+v)) {
        insp[f] = +v;
        any = true;
      }
    }
    if (!any) continue;
    const known = (await visits(hiveId)).find((row) => sameVisit(insp, row));
    if (known) {
      // Listed under its own id: the preview counts it as already there.
      list('inspection', { ...insp, id: known.id });
      continue;
    }
    const row = { ...insp, id: crypto.randomUUID() };
    (await visits(hiveId)).push(row);
    list('inspection', row);
  }
  return { kind: 'csv', data, fromWorkspace: orgId(), images: new Map(), local: true };
}

// ---------------------------------------------------------------------------
// 2. Planning

// A new id for a record of another workspace: the same file imported into
// the same workspace always gives the same ids. Shaped like a UUID (v8).
async function derivedId(...parts: string[]): Promise<string> {
  const d = new Uint8Array(
    await crypto.subtle.digest('SHA-256', new TextEncoder().encode(parts.join('\n')))
  );
  d[6] = (d[6]! & 0x0f) | 0x80;
  d[8] = (d[8]! & 0x3f) | 0x80;
  const h = [...d.subarray(0, 16)].map((b) => b.toString(16).padStart(2, '0')).join('');
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20, 32)}`;
}

function same(a: unknown, b: unknown): boolean {
  if (a === null || a === undefined || b === null || b === undefined)
    return (a ?? null) === (b ?? null);
  if (typeof a === 'number' || typeof b === 'number') return Number(a) === Number(b);
  return String(a) === String(b);
}

async function existingRows(table: string, ids: string[]): Promise<Map<string, Row>> {
  const db = await getDB();
  const out = new Map<string, Row>();
  for (let i = 0; i < ids.length; i += 400) {
    const part = ids.slice(i, i + 400);
    const rows = await db.all<Row>(
      `SELECT * FROM ${table} WHERE id IN (${part.map(() => '?').join(', ')})`,
      part
    );
    for (const r of rows) out.set(r.id, r);
  }
  return out;
}

const zero = (): TableCounts => ({ create: 0, update: 0, restore: 0, skip: 0 });

/** What importing `src` into the active workspace would write. Nothing is written. */
export async function planImport(src: ParsedImport, opts: ImportOptions = {}): Promise<ImportPlan> {
  const db = await getDB();
  const workspace = orgId();
  const me = userId();
  const foreign = !src.local && src.fromWorkspace !== workspace;
  const from = src.fromWorkspace ?? '';
  const mapped = new Map<string, string>();
  const mapId = async (v: unknown): Promise<unknown> => {
    if (!foreign || typeof v !== 'string' || v === '') return v;
    let m = mapped.get(v);
    if (!m) mapped.set(v, (m = await derivedId(workspace, from, v)));
    return m;
  };

  const plan: ImportPlan = {
    kind: src.kind,
    workspace,
    foreign,
    counts: {},
    total: zero(),
    ops: [],
    images: new Map(),
    photosWithoutImage: 0
  };

  // Where hives stand and which hive a visit belongs to, after the import:
  // the scopes of the records that hang off them follow from these.
  const hiveApiary: Record<string, string> = {};
  for (const h of await db.all<Row>(`SELECT id, apiary_id FROM hive`))
    hiveApiary[h.id] = h.apiary_id;
  const visitHive = new Map<string, string>();

  for (const table of TABLES) {
    const rows = src.data[table] ?? [];
    if (rows.length === 0) continue;
    const cols = (await columnsOf(db, table))!;
    const counts = (plan.counts[table] = zero());

    const prepared: { id: string; source: Row; fields: Row }[] = [];
    for (const r of rows) {
      const original = typeof r.id === 'string' && r.id ? r.id : crypto.randomUUID();
      const id = (await mapId(original)) as string;
      const fields: Row = {};
      for (const [k, v] of Object.entries(r)) {
        if (k === 'id' || k === 'organization_id' || INTERNAL.has(k) || !cols.has(k)) continue;
        const value = toScalar(v);
        if (value === undefined) continue;
        fields[k] = value;
      }
      for (const k of REFS[table] ?? []) {
        if (!(k in fields)) continue;
        const v = fields[k];
        if (k === 'scope_id' && typeof v === 'string' && v.startsWith('user:')) {
          // Personal events become the importing user's.
          fields[k] = foreign ? 'user:' + me : v;
        } else {
          fields[k] = await mapId(v);
        }
      }
      // A member of another workspace is nobody here.
      if (foreign && table === 'task' && 'assigned_to' in fields) fields.assigned_to = '';
      if (table === 'photo' && !validThumb(fields.thumb)) delete fields.thumb;
      prepared.push({ id, source: { ...r, id: original }, fields });
    }

    const existing = await existingRows(
      table,
      prepared.map((p) => p.id)
    );
    for (const { id, source, fields } of prepared) {
      const cur = existing.get(id);
      let op: Op | null = null;
      if (!cur) {
        op = {
          kind: 'create',
          entity: table,
          id,
          scope: '',
          fields: { ...fields, organization_id: workspace, deleted: 0 }
        };
      } else if (opts.overwrite && table !== 'photo' && (!cur.deleted || opts.restoreDeleted)) {
        // Photos never change once taken; everything else gets the values
        // of the file that differ from what is stored.
        const diff: Row = {};
        for (const [k, v] of Object.entries(fields)) if (!same(v, cur[k])) diff[k] = v;
        if (cur.deleted)
          op = { kind: 'restore', entity: table, id, scope: '', fields: { ...diff, deleted: 0 } };
        else if (Object.keys(diff).length)
          op = { kind: 'update', entity: table, id, scope: '', fields: diff };
      }
      const after: Row = { ...cur, ...(op?.fields ?? {}), id };
      if (table === 'hive') hiveApiary[id] = after.apiary_id;
      if (table === 'inspection') visitHive.set(id, after.hive_id);
      if (!op) {
        counts.skip++;
        plan.total.skip++;
        continue;
      }
      op.scope = await scopeOf(table, after, hiveApiary, visitHive);
      counts[op.kind]++;
      plan.total[op.kind]++;
      plan.ops.push(op);
      if (table === 'photo' && op.kind === 'create') {
        const image = src.images.get(source.id);
        if (image) plan.images.set(id, image);
        else plan.photosWithoutImage++;
      }
    }
  }
  return plan;
}

const validThumb = (v: unknown) =>
  typeof v === 'string' && v.startsWith('data:image/') && v.length <= MAX_THUMB_CHARS;

async function scopeOf(
  table: string,
  row: Row,
  hiveApiary: Record<string, string>,
  visitHive: Map<string, string>
): Promise<string> {
  if (table === 'photo') {
    if (row.owner_entity === 'hive') return hiveApiary[row.owner_id] ?? '';
    let hive = visitHive.get(row.owner_id);
    if (hive === undefined) {
      const db = await getDB();
      hive = (await db.get<Row>(`SELECT hive_id FROM inspection WHERE id = ?`, [row.owner_id]))
        ?.hive_id;
    }
    return hive ? (hiveApiary[hive] ?? '') : '';
  }
  const entity = ENTITIES.find((e) => e.table === table)!;
  return entity.scope(row, { hiveApiary }) ?? '';
}

// ---------------------------------------------------------------------------
// 3. Writing and undoing

const UNDO_KEY = 'import_undo';

type UndoRecord = {
  at: string;
  rows: { entity: string; id: string; scope: string }[];
  photos: string[];
};

export type ImportOutcome = { created: number; updated: number; restored: number };

/** Writes the plan in one local transaction and remembers what it created. */
export async function applyImport(plan: ImportPlan, sink?: ImageSink): Promise<ImportOutcome> {
  const images = plan.images.size ? (sink ?? (await defaultSink())) : null;
  const kept: string[] = [];
  try {
    // As when a photo is taken: the image is stored before its record.
    for (const [id, blob] of plan.images) {
      await images!.keep(id, blob);
      kept.push(id);
    }
    const w = unit();
    for (const op of plan.ops) w.patch(op.entity, op.id, op.scope, op.fields);
    await w.commit();
  } catch (e) {
    for (const id of kept) await images!.drop(id).catch(() => undefined);
    throw e;
  }
  const created = plan.ops.filter((o) => o.kind === 'create');
  if (created.length) {
    const record: UndoRecord = {
      at: new Date().toISOString(),
      rows: created.map(({ entity, id, scope }) => ({ entity, id, scope })),
      photos: kept
    };
    await setMeta(UNDO_KEY, JSON.stringify(record));
  }
  return { created: created.length, updated: plan.total.update, restored: plan.total.restore };
}

async function setMeta(k: string, v: string | null) {
  const db = await getDB();
  await db.batch([
    v === null
      ? { sql: `DELETE FROM sync_meta WHERE k = ?`, params: [k] }
      : {
          sql: `INSERT INTO sync_meta (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
          params: [k, v]
        }
  ]);
}

async function undoRecord(): Promise<UndoRecord | null> {
  const db = await getDB();
  const r = await db.get<{ v: string }>(`SELECT v FROM sync_meta WHERE k = ?`, [UNDO_KEY]);
  if (!r) return null;
  try {
    return JSON.parse(r.v) as UndoRecord;
  } catch {
    return null;
  }
}

/** The last import that can be undone: when it ran and how many records it created. */
export async function lastImport(): Promise<{ at: string; created: number } | null> {
  const r = await undoRecord();
  return r ? { at: r.at, created: r.rows.length } : null;
}

// Records added since the import that hang off a record it created.
const CHILDREN: Record<string, { table: string; col: string }[]> = {
  apiary: [{ table: 'hive', col: 'apiary_id' }],
  hive: ['inspection', 'queen', 'harvest', 'treatment', 'placement'].map((table) => ({
    table,
    col: 'hive_id'
  }))
};

/**
 * Deletes what the last import created, in one local transaction. An
 * apiary or hive that has since got records of its own (a hive moved
 * there, a visit recorded) stays. Returns how many records were deleted
 * and how many were kept for that reason.
 */
export async function undoLastImport(sink?: ImageSink): Promise<{ removed: number; kept: number }> {
  const record = await undoRecord();
  if (!record) return { removed: 0, kept: 0 };
  const db = await getDB();
  const created = new Set(record.rows.map((r) => r.id));
  const w = unit();
  let removed = 0;
  let kept = 0;
  const removedPhotos: string[] = [];
  const keptIds = new Set<string>();
  // Children first: a hive that stays keeps its apiary too.
  for (const r of [...record.rows].reverse()) {
    const live = await db.get<Row>(`SELECT 1 AS ok FROM ${r.entity} WHERE id = ? AND deleted = 0`, [
      r.id
    ]);
    if (!live) continue;
    let used = false;
    for (const c of CHILDREN[r.entity] ?? []) {
      const kids = await db.all<Row>(
        `SELECT id FROM ${c.table} WHERE ${c.col} = ? AND deleted = 0`,
        [r.id]
      );
      if (kids.some((k) => !created.has(k.id) || keptIds.has(k.id))) used = true;
    }
    if (used) {
      keptIds.add(r.id);
      kept++;
      continue;
    }
    w.remove(r.entity, r.id, r.scope);
    removed++;
    if (r.entity === 'photo') removedPhotos.push(r.id);
  }
  await w.commit();
  await setMeta(UNDO_KEY, null);
  if (removedPhotos.length) {
    const images = sink ?? (await defaultSink());
    for (const id of removedPhotos) await images.drop(id).catch(() => undefined);
    await photoRecords.eraseDeleted();
  }
  return { removed, kept };
}
