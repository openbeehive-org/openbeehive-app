// Local-first repository. The UI uses only these functions.
// Write = set only the changed fields locally, update the field clock
// and put a partial delta into the outbox (per-field LWW).
// List fields (e.g. photos) go through OR-Set operations.

import { getDB } from './db';
import { hlc } from './hlc';
import { syncOnce } from './sync';
import { parseFieldClock, parseORSet, orAdd, orRemove, orValues } from './merge';

const authorId = () => localStorage.getItem('obh.userId') ?? 'local';

async function enqueue(
  entity: string,
  id: string,
  scopeId: string,
  op: 1 | 2,
  payload: unknown,
  stamp: string
) {
  const db = await getDB();
  await db.exec(
    `INSERT INTO outbox (id, entity, entity_id, scope_id, op, payload, hlc, author_id)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
    [crypto.randomUUID(), entity, id, scopeId, op, JSON.stringify(payload), stamp, authorId()]
  );
  void syncOnce();
}

// patch sets only the given scalar fields (partial per-field delta).
export async function patch(
  entity: string,
  id: string,
  scopeId: string,
  fields: Record<string, unknown>
) {
  const db = await getDB();
  const stamp = hlc.now();
  const cur = await db.get<any>(`SELECT field_hlc FROM ${entity} WHERE id = ?`, [id]);
  const fc = parseFieldClock(cur?.field_hlc);
  for (const k of Object.keys(fields)) fc[k] = stamp;

  const cols = ['id', ...Object.keys(fields), 'field_hlc'];
  const vals = [id, ...Object.values(fields), JSON.stringify(fc)];
  const ph = cols.map(() => '?').join(', ');
  const upd = cols
    .filter((c) => c !== 'id')
    .map((c) => `${c} = excluded.${c}`)
    .join(', ');
  await db.exec(
    `INSERT INTO ${entity} (${cols.join(', ')}) VALUES (${ph})
     ON CONFLICT(id) DO UPDATE SET ${upd}`,
    vals
  );

  await enqueue(entity, id, scopeId, 1, fields, stamp);
}

export async function remove(entity: string, id: string, scopeId: string) {
  const db = await getDB();
  const stamp = hlc.now();
  const cur = await db.get<any>(`SELECT field_hlc FROM ${entity} WHERE id = ?`, [id]);
  const fc = parseFieldClock(cur?.field_hlc);
  fc['deleted'] = stamp;
  await db.exec(`UPDATE ${entity} SET deleted = 1, field_hlc = ? WHERE id = ?`, [
    JSON.stringify(fc),
    id
  ]);
  await enqueue(entity, id, scopeId, 2, { deleted: 1 }, stamp);
}

// --- OR-Set operations (list fields, e.g. inspection.photo_keys) ---

export async function setAdd(
  entity: string,
  id: string,
  scopeId: string,
  field: string,
  elem: string
) {
  const db = await getDB();
  const stamp = hlc.now();
  const cur = await db.get<any>(`SELECT ${field} FROM ${entity} WHERE id = ?`, [id]);
  const os = parseORSet(cur?.[field]);
  orAdd(os, elem, stamp); // tag = HLC of this add
  await db.exec(`UPDATE ${entity} SET ${field} = ? WHERE id = ?`, [JSON.stringify(os), id]);
  await enqueue(entity, id, scopeId, 1, { [field]: { add: [elem] } }, stamp);
}

export async function setRemove(
  entity: string,
  id: string,
  scopeId: string,
  field: string,
  elem: string
) {
  const db = await getDB();
  const stamp = hlc.now();
  const cur = await db.get<any>(`SELECT ${field} FROM ${entity} WHERE id = ?`, [id]);
  const os = parseORSet(cur?.[field]);
  // The delta carries WHICH add tags this device observed; receivers tombstone
  // only those, so an add another device made concurrently survives (add-wins).
  const observed = [...(os[elem]?.a ?? [])];
  orRemove(os, elem);
  await db.exec(`UPDATE ${entity} SET ${field} = ? WHERE id = ?`, [JSON.stringify(os), id]);
  await enqueue(
    entity,
    id,
    scopeId,
    1,
    { [field]: { remove: [elem], removed_tags: { [elem]: observed } } },
    stamp
  );
}

// --- Public, typed API for the UI ---

export const apiaries = {
  async list() {
    const db = await getDB();
    return db.all(`SELECT * FROM apiary WHERE deleted = 0 ORDER BY name`);
  },
  // create = full delta; edit = only changed fields via patch().
  async create(s: {
    organization_id: string;
    name: string;
    address?: string;
    lat?: number;
    lng?: number;
    note?: string;
  }) {
    const id = crypto.randomUUID();
    const now = new Date().toISOString();
    await patch('apiary', id, id /* scope = own id */, {
      organization_id: s.organization_id,
      name: s.name,
      address: s.address ?? '',
      lat: s.lat ?? 0,
      lng: s.lng ?? 0,
      note: s.note ?? '',
      created_at: now,
      updated_at: now,
      deleted: 0
    });
    return id;
  },
  async get(id: string) {
    const db = await getDB();
    return db.get<any>(`SELECT * FROM apiary WHERE id = ? AND deleted = 0`, [id]);
  },
  rename: (id: string, name: string) => patch('apiary', id, id, { name }),
  setNote: (id: string, note: string) => patch('apiary', id, id, { note }),
  // edit = only the changed scalar fields (per-field LWW); scope = own id.
  update: (id: string, fields: Record<string, unknown>) =>
    patch('apiary', id, id, { ...fields, updated_at: new Date().toISOString() }),
  remove: (id: string) => remove('apiary', id, id)
};

export const inspections = {
  async listByHive(hiveId: string) {
    const db = await getDB();
    const rows = await db.all<any>(
      `SELECT * FROM inspection WHERE deleted = 0 AND hive_id = ? ORDER BY date DESC`,
      [hiveId]
    );
    // Resolve the OR-Set column to the list of visible photos.
    return rows.map((r) => ({ ...r, photos: orValues(parseORSet(r.photo_keys)) }));
  },
  async add(d: any, scopeApiaryId: string) {
    const id = crypto.randomUUID();
    await patch('inspection', id, scopeApiaryId, {
      ...d,
      created_at: new Date().toISOString(),
      deleted: 0
    });
    return id;
  },
  addPhoto: (id: string, scope: string, key: string) =>
    setAdd('inspection', id, scope, 'photo_keys', key),
  removePhoto: (id: string, scope: string, key: string) =>
    setRemove('inspection', id, scope, 'photo_keys', key)
};

export const hives = {
  async get(id: string) {
    const db = await getDB();
    return db.get<any>(`SELECT * FROM hive WHERE id = ? AND deleted = 0`, [id]);
  },
  async list(apiaryId?: string) {
    const db = await getDB();
    return apiaryId
      ? db.all(`SELECT * FROM hive WHERE deleted = 0 AND apiary_id = ? ORDER BY name`, [apiaryId])
      : db.all(`SELECT * FROM hive WHERE deleted = 0 ORDER BY name`);
  },
  async count(apiaryId: string) {
    const db = await getDB();
    const r = await db.get<{ n: number }>(
      `SELECT COUNT(*) AS n FROM hive WHERE deleted = 0 AND apiary_id = ?`,
      [apiaryId]
    );
    return r?.n ?? 0;
  },
  // edit = only the changed scalar fields (per-field LWW). scope = apiary id.
  update: (id: string, apiaryId: string, fields: Record<string, unknown>) =>
    patch('hive', id, apiaryId, { ...fields, updated_at: new Date().toISOString() }),
  remove: (id: string, apiaryId: string) => remove('hive', id, apiaryId),
  setPhoto: (id: string, apiaryId: string, photo: string) => patch('hive', id, apiaryId, { photo }),
  // Location history: every move appends a placement interval, so the full
  // residence timeline (which apiary, when) can be followed across the years.
  async locationHistory(hiveId: string) {
    const db = await getDB();
    return db.all<any>(
      `SELECT p.id, p.apiary_id, p.start_at, p.end_at, a.name AS apiary_name
       FROM placement p LEFT JOIN apiary a ON a.id = p.apiary_id
       WHERE p.hive_id = ? AND p.deleted = 0
       ORDER BY p.start_at DESC`,
      [hiveId]
    );
  },
  // Store/refresh the printed short code on the hive (optional, display only).
  setCode: (id: string, apiaryId: string, code: string) =>
    patch('hive', id, apiaryId, { qr_code: code })
};

// Honey harvests (read side; recording goes through history.recordHarvest,
// which also writes the HARVEST fact row that feeds the stats).
export const harvests = {
  async listByHive(hiveId: string) {
    const db = await getDB();
    return db.all<any>(
      `SELECT * FROM harvest WHERE deleted = 0 AND hive_id = ? ORDER BY date DESC`,
      [hiveId]
    );
  },
  async totalByHive(hiveId: string) {
    const db = await getDB();
    const r = await db.get<{ kg: number }>(
      `SELECT COALESCE(SUM(amount_kg),0) AS kg FROM harvest WHERE deleted = 0 AND hive_id = ?`,
      [hiveId]
    );
    return r?.kg ?? 0;
  },
  remove: (id: string, apiaryId: string) => remove('harvest', id, apiaryId)
};

// Treatments (Bestandsbuch). Recording goes through history.recordTreatment,
// which also writes the TREATMENT fact row that feeds the history timeline.
export const treatments = {
  async listByHive(hiveId: string) {
    const db = await getDB();
    return db.all<any>(
      `SELECT * FROM treatment WHERE deleted = 0 AND hive_id = ? ORDER BY date DESC`,
      [hiveId]
    );
  },
  remove: (id: string, apiaryId: string) => remove('treatment', id, apiaryId)
};

// International queen marking colour by the year's last digit (1/6 white,
// 2/7 yellow, 3/8 red, 4/9 green, 5/0 blue). Matches the MarkingColor enum
// (1=white … 5=blue) in proto/openbeehive/v1/common.proto.
export function markingForYear(year: number): number {
  return (((year % 10) + 4) % 5) + 1;
}
export const MARKING_COLORS = ['', '#f4f1ea', '#e9c33b', '#c8402f', '#3f8c4f', '#3566b5'];
export const MARKING_NAMES = ['', 'White', 'Yellow', 'Red', 'Green', 'Blue'];

// Queens. Reads are local; set/replace go through history.setQueen (which keeps
// the reign intervals and emits events). Edit/remove are plain per-field LWW.
export const queens = {
  async listByHive(hiveId: string) {
    const db = await getDB();
    return db.all<any>(
      `SELECT * FROM queen WHERE deleted = 0 AND hive_id = ? ORDER BY introduced_at DESC`,
      [hiveId]
    );
  },
  async current(hiveId: string) {
    const db = await getDB();
    return db.get<any>(
      `SELECT * FROM queen WHERE deleted = 0 AND hive_id = ? AND active = 1 ORDER BY introduced_at DESC LIMIT 1`,
      [hiveId]
    );
  },
  update: (id: string, scopeApiaryId: string, fields: Record<string, unknown>) =>
    patch('queen', id, scopeApiaryId, { ...fields, updated_at: new Date().toISOString() }),
  remove: (id: string, scopeApiaryId: string) => remove('queen', id, scopeApiaryId)
};

// Tasks live in the personal scope unless tied to an apiary. They are plain
// scalars (per-field LWW), so add / toggle / remove all go through patch().
const taskScope = (apiaryId?: string) =>
  apiaryId || 'user:' + (localStorage.getItem('obh.userId') ?? 'local');

export const tasks = {
  async list() {
    const db = await getDB();
    return db.all<any>(`SELECT * FROM task WHERE deleted = 0 ORDER BY done ASC, due_at ASC`);
  },
  async openCount() {
    const db = await getDB();
    const r = await db.get<{ n: number }>(
      `SELECT COUNT(*) AS n FROM task WHERE deleted = 0 AND done = 0`
    );
    return r?.n ?? 0;
  },
  async create(s: {
    title: string;
    due_at?: string;
    apiary_id?: string;
    hive_id?: string;
    priority?: number;
  }) {
    const id = crypto.randomUUID();
    await patch('task', id, taskScope(s.apiary_id), {
      organization_id: localStorage.getItem('obh.orgId') ?? 'local',
      title: s.title,
      apiary_id: s.apiary_id ?? '',
      hive_id: s.hive_id ?? '',
      due_at: s.due_at ?? '',
      done: 0,
      priority: s.priority ?? 0,
      created_at: new Date().toISOString(),
      deleted: 0
    });
    return id;
  },
  toggle: (t: { id: string; apiary_id?: string; done: number }) =>
    patch('task', t.id, taskScope(t.apiary_id), { done: t.done ? 0 : 1 }),
  update: (t: { id: string; apiary_id?: string }, fields: Record<string, unknown>) =>
    patch('task', t.id, taskScope(t.apiary_id), fields),
  remove: (t: { id: string; apiary_id?: string }) => remove('task', t.id, taskScope(t.apiary_id))
};
