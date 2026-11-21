// History & statistics. All operations that write history run here:
// they freeze the context (apiary/hive/queen) at the time and create
// an immutable event. The event log doubles as the fact table
// for statistics (no joins, permanently correct).

import { getDB } from './db';
import { toDateInput } from '../dates';
import { CodedError } from '../errors';
import { markingForYear, InspectionSource, type InspectionInput } from './repo';
import { unit, type Unit } from './write';

export const EventType = {
  CREATED: 1,
  QUEEN_INTRODUCED: 2,
  QUEEN_REPLACED: 3,
  MOVED: 4,
  INSPECTION: 5,
  TREATMENT: 6,
  HARVEST: 7,
  STATUS: 8,
  DISSOLVED: 9
} as const;

const orgId = () => localStorage.getItem('obh.orgId') ?? 'local';
const now = () => new Date().toISOString();

// Event titles as stored in the fact table.
const queenTitle = (year: number) => `Queen ${year}`;
const harvestTitle = (kg: number, variety?: string | null) => `${kg} kg ${variety || 'Honey'}`;

// The HiveStatus enum value for a colony that was given up.
const HIVE_STATUS_DISSOLVED = 5;

// Resolve the context for a (possibly back-dated!) point in time:
// where the hive lived and which queen reigned at that time.
export async function resolveContext(hiveId: string, date: string) {
  const db = await getDB();

  // Apiary: matching placement interval, else current apiary.
  const platz = await db.get<any>(
    `SELECT apiary_id FROM placement
     WHERE hive_id = ? AND deleted = 0 AND start_at <= ?
       AND (end_at IS NULL OR end_at > ?)
     ORDER BY start_at DESC LIMIT 1`,
    [hiveId, date, date]
  );
  let apiaryId = platz?.apiary_id;
  if (!apiaryId) {
    const b = await db.get<any>(`SELECT apiary_id FROM hive WHERE id = ?`, [hiveId]);
    apiaryId = b?.apiary_id ?? '';
  }

  // Queen whose reign [introduced_at, replaced_at) contains the point in time.
  const q = await db.get<any>(
    `SELECT id FROM queen
     WHERE hive_id = ? AND deleted = 0 AND introduced_at <= ?
       AND (replaced_at IS NULL OR replaced_at > ?)
     ORDER BY introduced_at DESC LIMIT 1`,
    [hiveId, date, date]
  );
  let queenId = q?.id;
  if (!queenId) {
    const akt = await db.get<any>(
      `SELECT id FROM queen WHERE hive_id = ? AND active = 1 AND deleted = 0 LIMIT 1`,
      [hiveId]
    );
    queenId = akt?.id ?? '';
  }

  return { apiaryId, queenId };
}

function emit(
  w: Unit,
  type: number,
  scopeId: string,
  e: {
    date: string;
    apiary_id?: string;
    hive_id?: string;
    queen_id?: string;
    ref_entity?: string;
    ref_id?: string;
    title?: string;
    amount_kg?: number;
    detail?: unknown;
  }
) {
  const id = crypto.randomUUID();
  w.patch('event', id, scopeId, {
    organization_id: orgId(),
    scope_id: scopeId,
    type,
    date: e.date,
    apiary_id: e.apiary_id ?? '',
    hive_id: e.hive_id ?? '',
    queen_id: e.queen_id ?? '',
    ref_entity: e.ref_entity ?? '',
    ref_id: e.ref_id ?? '',
    title: e.title ?? '',
    amount_kg: e.amount_kg ?? 0,
    detail: JSON.stringify(e.detail ?? {}),
    author_id: localStorage.getItem('obh.userId') ?? '',
    deleted: 0
  });
  return id;
}

// --- Write flows ---
// Each flow collects its rows in one unit of work and commits them together:
// a hive never exists without its placement and event, an old queen is never
// ended without the new one being introduced.

// Create a hive: hive row + open placement interval + event.
export async function createHive(b: { name: string; apiary_id: string; type?: number }) {
  const id = crypto.randomUUID();
  const ts = now();
  const w = unit();
  w.patch('hive', id, b.apiary_id, {
    organization_id: orgId(),
    apiary_id: b.apiary_id,
    name: b.name,
    type: b.type ?? 0,
    status: 1,
    created_at: ts,
    updated_at: ts,
    deleted: 0
  });
  w.patch('placement', crypto.randomUUID(), b.apiary_id, {
    organization_id: orgId(),
    hive_id: id,
    apiary_id: b.apiary_id,
    start_at: ts,
    end_at: null,
    deleted: 0
  });
  emit(w, EventType.CREATED, b.apiary_id, {
    date: ts,
    apiary_id: b.apiary_id,
    hive_id: id,
    title: b.name
  });
  await w.commit();
  return id;
}

// Queen change: close the old (replaced_at), introduce the new, 2 events.
// introduced_at back-dates the change; it defaults to now.
export async function setQueen(
  hiveId: string,
  scopeApiaryId: string,
  k: {
    year: number;
    origin?: string;
    marking?: number;
    breeder_number?: string;
    note?: string;
    introduced_at?: string;
  }
) {
  const db = await getDB();
  const ts = now();
  const since = k.introduced_at || ts;
  // Marking colour follows the year unless overridden (international scheme).
  const marking = k.marking ?? markingForYear(k.year);
  const alt = await db.get<any>(
    `SELECT id FROM queen WHERE hive_id = ? AND active = 1 AND deleted = 0 LIMIT 1`,
    [hiveId]
  );
  const w = unit();
  if (alt) {
    w.patch('queen', alt.id, scopeApiaryId, { active: 0, replaced_at: since });
    emit(w, EventType.QUEEN_REPLACED, scopeApiaryId, {
      date: since,
      hive_id: hiveId,
      queen_id: alt.id
    });
  }
  const neu = crypto.randomUUID();
  w.patch('queen', neu, scopeApiaryId, {
    organization_id: orgId(),
    hive_id: hiveId,
    year: k.year,
    marking,
    origin: k.origin ?? '',
    breeder_number: k.breeder_number ?? '',
    note: k.note ?? '',
    introduced_at: since,
    replaced_at: null,
    active: 1,
    created_at: ts,
    updated_at: ts,
    deleted: 0
  });
  emit(w, EventType.QUEEN_INTRODUCED, scopeApiaryId, {
    date: since,
    hive_id: hiveId,
    queen_id: neu,
    title: queenTitle(k.year)
  });
  await w.commit();
  return neu;
}

// Move: close the current placement interval, open a new one, emit event.
export async function moveHive(hiveId: string, fromApiary: string, toApiary: string) {
  const db = await getDB();
  const ts = now();
  const open = await db.get<any>(
    `SELECT id FROM placement WHERE hive_id = ? AND end_at IS NULL AND deleted = 0 ORDER BY start_at DESC LIMIT 1`,
    [hiveId]
  );
  const w = unit();
  if (open) w.patch('placement', open.id, fromApiary, { end_at: ts });
  w.patch('placement', crypto.randomUUID(), toApiary, {
    organization_id: orgId(),
    hive_id: hiveId,
    apiary_id: toApiary,
    start_at: ts,
    end_at: null,
    deleted: 0
  });
  w.patch('hive', hiveId, toApiary, { apiary_id: toApiary, updated_at: ts });
  emit(w, EventType.MOVED, toApiary, {
    date: ts,
    apiary_id: toApiary,
    hive_id: hiveId,
    detail: { from: fromApiary, to: toApiary }
  });
  await w.commit();
}

// Record a honey harvest: freeze the context -> harvest record + fact row.
export async function recordHarvest(input: {
  hiveId: string;
  date?: string;
  amount_kg: number;
  variety?: string;
  water_content?: number;
  batch_number?: string;
  best_before?: string;
  note?: string;
}) {
  const date = input.date ?? now();
  const ctx = await resolveContext(input.hiveId, date);
  const harvestId = crypto.randomUUID();
  const w = unit();
  w.patch('harvest', harvestId, ctx.apiaryId, {
    organization_id: orgId(),
    apiary_id: ctx.apiaryId,
    hive_id: input.hiveId,
    queen_id: ctx.queenId,
    date,
    variety: input.variety ?? '',
    amount_kg: input.amount_kg,
    water_content: input.water_content ?? 0,
    batch_number: input.batch_number ?? '',
    best_before: input.best_before ?? '',
    note: input.note ?? '',
    deleted: 0
  });
  emit(w, EventType.HARVEST, ctx.apiaryId, {
    date,
    apiary_id: ctx.apiaryId,
    hive_id: input.hiveId,
    queen_id: ctx.queenId,
    ref_entity: 'harvest',
    ref_id: harvestId,
    amount_kg: input.amount_kg,
    title: harvestTitle(input.amount_kg, input.variety)
  });
  await w.commit();
  return harvestId;
}

// Record a treatment: freeze the context -> treatment record + fact row.
export async function recordTreatment(input: {
  hiveId: string;
  date?: string;
  product: string;
  active_ingredient?: string;
  dose?: string;
  method?: string;
  batch_number?: string;
  withdrawal_until?: string;
  reason?: string;
  note?: string;
}) {
  const date = input.date ?? now();
  const ctx = await resolveContext(input.hiveId, date);
  const treatmentId = crypto.randomUUID();
  const w = unit();
  w.patch('treatment', treatmentId, ctx.apiaryId, {
    organization_id: orgId(),
    apiary_id: ctx.apiaryId,
    hive_id: input.hiveId,
    queen_id: ctx.queenId,
    date,
    product: input.product,
    active_ingredient: input.active_ingredient ?? '',
    dose: input.dose ?? '',
    method: input.method ?? '',
    batch_number: input.batch_number ?? '',
    withdrawal_until: input.withdrawal_until ?? '',
    reason: input.reason ?? 'varroa',
    note: input.note ?? '',
    deleted: 0
  });
  emit(w, EventType.TREATMENT, ctx.apiaryId, {
    date,
    apiary_id: ctx.apiaryId,
    hive_id: input.hiveId,
    queen_id: ctx.queenId,
    ref_entity: 'treatment',
    ref_id: treatmentId,
    title: input.product
  });
  await w.commit();
  return treatmentId;
}

// Inspection with a context event (completes the history).
export async function recordInspection(hiveId: string, d: InspectionInput) {
  const date = d.date ?? now();
  const ctx = await resolveContext(hiveId, date);
  const id = crypto.randomUUID();
  const w = unit();
  w.patch('inspection', id, ctx.apiaryId, {
    organization_id: orgId(),
    source: InspectionSource.MANUAL,
    ...d,
    hive_id: hiveId,
    date,
    created_at: now(),
    deleted: 0
  });
  emit(w, EventType.INSPECTION, ctx.apiaryId, {
    date,
    apiary_id: ctx.apiaryId,
    hive_id: hiveId,
    queen_id: ctx.queenId,
    ref_entity: 'inspection',
    ref_id: id,
    title: 'Inspection'
  });
  await w.commit();
  return id;
}

// --- Corrections: edit and delete with the related events kept in step ---
// Like the flows above, each correction is one unit of work: a record is
// never changed or deleted without its events following in the same local
// transaction, and an undo restores all of it or nothing.

/** Restores exactly what the matching delete removed. */
export type Undo = () => Promise<void>;

type Target = { entity: string; id: string; scope: string };
type EventRef = { id: string; scope_id: string | null };

// The fields of `next` whose value differs from the stored row, so an edit
// sends (and stamps) only what the user actually changed.
function changedFields(row: Record<string, unknown>, next: Record<string, unknown>) {
  const out: Record<string, unknown> = {};
  // An unset text column and an empty string mean the same thing.
  const norm = (v: unknown) => (v === '' ? null : (v ?? null));
  for (const [k, v] of Object.entries(next)) {
    if (v === undefined) continue;
    if (norm(row[k]) !== norm(v)) out[k] = v;
  }
  return out;
}

async function getRow(entity: string, id: string) {
  const db = await getDB();
  return db.get<any>(`SELECT * FROM ${entity} WHERE id = ? AND deleted = 0`, [id]);
}

// The timeline / fact rows that describe one record.
async function eventsOf(refEntity: string, refId: string) {
  const db = await getDB();
  return db.all<EventRef>(
    `SELECT id, scope_id FROM event WHERE ref_entity = ? AND ref_id = ? AND deleted = 0`,
    [refEntity, refId]
  );
}

async function queenEvents(queenId: string, type: number) {
  const db = await getDB();
  return db.all<EventRef>(
    `SELECT id, scope_id FROM event WHERE queen_id = ? AND type = ? AND deleted = 0`,
    [queenId, type]
  );
}

async function hiveApiary(hiveId: string): Promise<string> {
  const db = await getDB();
  const h = await db.get<any>(`SELECT apiary_id FROM hive WHERE id = ?`, [hiveId]);
  return h?.apiary_id ?? '';
}

// Records sync in the scope they were created in: the one stored on the
// record or on its event, else the hive's current apiary.
async function scopeOf(row: any, events: EventRef[]): Promise<string> {
  return row.apiary_id || events[0]?.scope_id || (await hiveApiary(row.hive_id));
}

function patchEvents(w: Unit, events: EventRef[], scope: string, fields: Record<string, unknown>) {
  if (Object.keys(fields).length === 0) return;
  for (const e of events) w.patch('event', e.id, e.scope_id || scope, fields);
}

// Writes that belong to a delete besides the tombstones themselves, and how
// the undo takes them back.
type Along = { apply: (w: Unit) => void; revert: (w: Unit) => void };

// Tombstones the targets (and applies what goes along) in one unit; the
// returned undo reverses all of it in one unit as well.
async function tombstone(targets: Target[], along?: Along): Promise<Undo> {
  const w = unit();
  for (const t of targets) w.remove(t.entity, t.id, t.scope);
  along?.apply(w);
  await w.commit();
  return async () => {
    const back = unit();
    along?.revert(back);
    for (const t of targets) back.patch(t.entity, t.id, t.scope, { deleted: 0 });
    await back.commit();
  };
}

const noUndo: Undo = async () => {};

// Delete a record together with the events that reference it, so the
// timeline and the honey totals no longer count it.
async function deleteRecord(entity: 'inspection' | 'harvest' | 'treatment', id: string) {
  const row = await getRow(entity, id);
  if (!row) return noUndo;
  const events = await eventsOf(entity, id);
  const scope = await scopeOf(row, events);
  return tombstone([
    { entity, id, scope },
    ...events.map((e) => ({ entity: 'event', id: e.id, scope: e.scope_id || scope }))
  ]);
}

export const deleteInspection = (id: string) => deleteRecord('inspection', id);
export const deleteHarvest = (id: string) => deleteRecord('harvest', id);
export const deleteTreatment = (id: string) => deleteRecord('treatment', id);

// A changed date moves the record to the apiary and queen of that day.
async function contextChange(row: any, changes: Record<string, unknown>) {
  if (typeof changes.date !== 'string') return {};
  const ctx = await resolveContext(row.hive_id, changes.date);
  return { date: changes.date, apiary_id: ctx.apiaryId, queen_id: ctx.queenId };
}

// Correct an inspection. Returns false when nothing differed.
export async function editInspection(id: string, fields: InspectionInput) {
  const row = await getRow('inspection', id);
  if (!row) throw new CodedError('not_found', 'inspection not found');
  const changes = changedFields(row, fields);
  if (Object.keys(changes).length === 0) return false;
  const events = await eventsOf('inspection', id);
  const scope = await scopeOf(row, events);
  const ctx = await contextChange(row, changes);
  const w = unit();
  w.patch('inspection', id, scope, changes);
  patchEvents(w, events, scope, ctx);
  await w.commit();
  return true;
}

export type HarvestFields = {
  date?: string;
  amount_kg?: number;
  variety?: string;
  water_content?: number | null;
  batch_number?: string;
  best_before?: string;
  note?: string;
};

// Correct a harvest: the record is marked as amended and its fact row
// follows the new date and amount. Returns false when nothing differed.
export async function editHarvest(id: string, fields: HarvestFields) {
  const row = await getRow('harvest', id);
  if (!row) throw new CodedError('not_found', 'harvest not found');
  const changes = changedFields(row, fields);
  if (Object.keys(changes).length === 0) return false;
  const events = await eventsOf('harvest', id);
  const scope = await scopeOf(row, events);
  const ctx = await contextChange(row, changes);
  const amount = (changes.amount_kg ?? row.amount_kg) as number;
  const variety = (changes.variety ?? row.variety) as string | null;
  const w = unit();
  w.patch('harvest', id, scope, { ...changes, ...ctx, amended_at: now() });
  patchEvents(w, events, scope, {
    ...ctx,
    ...('amount_kg' in changes || 'variety' in changes
      ? { amount_kg: amount, title: harvestTitle(amount, variety) }
      : {})
  });
  await w.commit();
  return true;
}

export type TreatmentFields = {
  date?: string;
  product?: string;
  active_ingredient?: string;
  dose?: string;
  method?: string;
  batch_number?: string;
  withdrawal_until?: string;
  reason?: string;
  note?: string;
};

// Correct a treatment: the record is marked as amended and its timeline
// entry follows the new date and product. Returns false when nothing differed.
export async function editTreatment(id: string, fields: TreatmentFields) {
  const row = await getRow('treatment', id);
  if (!row) throw new CodedError('not_found', 'treatment not found');
  const changes = changedFields(row, fields);
  if (Object.keys(changes).length === 0) return false;
  const events = await eventsOf('treatment', id);
  const scope = await scopeOf(row, events);
  const ctx = await contextChange(row, changes);
  const w = unit();
  w.patch('treatment', id, scope, { ...changes, ...ctx, amended_at: now() });
  patchEvents(w, events, scope, {
    ...ctx,
    ...('product' in changes ? { title: changes.product } : {})
  });
  await w.commit();
  return true;
}

export type QueenFields = {
  year?: number;
  marking?: number;
  origin?: string;
  breeder_number?: string;
  note?: string;
  introduced_at?: string;
};

// The queen whose reign ended when `q` was introduced.
async function predecessorOf(q: any) {
  const db = await getDB();
  return db.get<any>(
    `SELECT * FROM queen WHERE hive_id = ? AND deleted = 0 AND id != ? AND replaced_at = ?
     ORDER BY introduced_at DESC LIMIT 1`,
    [q.hive_id, q.id, q.introduced_at]
  );
}

// Correct a queen's details. This is not a re-queening: no replacement event
// is written, the existing events only follow the corrected year and date.
export async function editQueen(id: string, fields: QueenFields) {
  const row = await getRow('queen', id);
  if (!row) throw new CodedError('not_found', 'queen not found');
  const changes = changedFields(row, fields);
  if (Object.keys(changes).length === 0) return false;
  const introduced = await queenEvents(id, EventType.QUEEN_INTRODUCED);
  const scope = await scopeOf(row, introduced);
  // Read before the queen row changes: the lookup matches on its old date.
  const before = 'introduced_at' in changes ? await predecessorOf(row) : undefined;
  const replaced = before ? await queenEvents(before.id, EventType.QUEEN_REPLACED) : [];
  const ts = now();
  const w = unit();
  w.patch('queen', id, scope, { ...changes, updated_at: ts });
  patchEvents(w, introduced, scope, {
    ...('year' in changes ? { title: queenTitle(changes.year as number) } : {}),
    ...('introduced_at' in changes ? { date: changes.introduced_at } : {})
  });
  if (before) {
    // The previous queen reigned until this one arrived.
    w.patch('queen', before.id, scope, { replaced_at: changes.introduced_at, updated_at: ts });
    patchEvents(w, replaced, scope, { date: changes.introduced_at });
  }
  await w.commit();
  return true;
}

// Delete a queen recorded by mistake, with her introduction / replacement
// events. Removing the current queen puts her predecessor back in charge.
export async function deleteQueen(id: string): Promise<Undo> {
  const row = await getRow('queen', id);
  if (!row) return noUndo;
  const own = [
    ...(await queenEvents(id, EventType.QUEEN_INTRODUCED)),
    ...(await queenEvents(id, EventType.QUEEN_REPLACED))
  ];
  const scope = await scopeOf(row, own);
  const targets: Target[] = [
    { entity: 'queen', id, scope },
    ...own.map((e) => ({ entity: 'event', id: e.id, scope: e.scope_id || scope }))
  ];
  const before = row.active ? await predecessorOf(row) : undefined;
  if (before) {
    const replaced = await queenEvents(before.id, EventType.QUEEN_REPLACED);
    targets.push(
      ...replaced.map((e) => ({ entity: 'event', id: e.id, scope: e.scope_id || scope }))
    );
  }
  if (!before) return tombstone(targets);
  return tombstone(targets, {
    apply: (w) => w.patch('queen', before.id, scope, { active: 1, replaced_at: null }),
    revert: (w) =>
      w.patch('queen', before.id, scope, { active: 0, replaced_at: before.replaced_at })
  });
}

// Edit a hive's name, type or status. A status change is part of the hive's
// history, so it also writes an event (DISSOLVED when the colony is given up).
export async function updateHive(
  id: string,
  apiaryId: string,
  fields: { name?: string; type?: number; status?: number }
) {
  const row = await getRow('hive', id);
  if (!row) throw new CodedError('not_found', 'hive not found');
  const changes = changedFields(row, fields);
  if (Object.keys(changes).length === 0) return false;
  const ts = now();
  const w = unit();
  w.patch('hive', id, apiaryId, { ...changes, updated_at: ts });
  if ('status' in changes) {
    const dissolved = changes.status === HIVE_STATUS_DISSOLVED;
    emit(w, dissolved ? EventType.DISSOLVED : EventType.STATUS, apiaryId, {
      date: ts,
      apiary_id: apiaryId,
      hive_id: id,
      detail: { from: row.status ?? 0, to: changes.status }
    });
  }
  await w.commit();
  return true;
}

// --- Statistics straight from the fact table (type = HARVEST) ---

// Honey per local calendar year, oldest first. Harvests of deleted hives no
// longer count. The year is taken in the device's time zone: a harvest on
// 1 January is stored as local noon, which is still 31 December in UTC east
// of +12.
export async function honeyByYear(): Promise<{ year: string; kg: number }[]> {
  const db = await getDB();
  const rows = await db.all<{ date: string; kg: number }>(
    `SELECT e.date AS date, e.amount_kg AS kg
     FROM event e
     WHERE e.type = ${EventType.HARVEST} AND e.deleted = 0
       AND NOT EXISTS (SELECT 1 FROM hive h WHERE h.id = e.hive_id AND h.deleted = 1)`
  );
  const perYear = new Map<string, number>();
  for (const r of rows) {
    const year = toDateInput(String(r.date ?? '')).slice(0, 4);
    if (year) perYear.set(year, (perYear.get(year) ?? 0) + Number(r.kg ?? 0));
  }
  return [...perYear].sort(([a], [b]) => a.localeCompare(b)).map(([year, kg]) => ({ year, kg }));
}
