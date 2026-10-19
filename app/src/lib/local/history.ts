// History & statistics. All operations that write history run here:
// they freeze the context (apiary/hive/queen) at the time and create
// an immutable event. The event log doubles as the fact table
// for statistics (no joins, permanently correct).

import { getDB } from './db';
import { patch } from './repo';

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

async function emit(
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
  await patch('event', id, scopeId, {
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

// Create a hive: hive row + open placement interval + event.
export async function createHive(b: { name: string; apiary_id: string; type?: number }) {
  const id = crypto.randomUUID();
  const ts = now();
  await patch('hive', id, b.apiary_id, {
    organization_id: orgId(),
    apiary_id: b.apiary_id,
    name: b.name,
    type: b.type ?? 0,
    status: 1,
    created_at: ts,
    updated_at: ts,
    deleted: 0
  });
  await patch('placement', crypto.randomUUID(), b.apiary_id, {
    organization_id: orgId(),
    hive_id: id,
    apiary_id: b.apiary_id,
    start_at: ts,
    end_at: null,
    deleted: 0
  });
  await emit(EventType.CREATED, b.apiary_id, {
    date: ts,
    apiary_id: b.apiary_id,
    hive_id: id,
    title: b.name
  });
  return id;
}

// Queen change: close the old (replaced_at), introduce the new, 2 events.
export async function setQueen(
  hiveId: string,
  scopeApiaryId: string,
  k: { year: number; origin?: string; marking?: number; breeder_number?: string; note?: string }
) {
  const db = await getDB();
  const ts = now();
  // Marking colour follows the year unless overridden (international scheme).
  const marking = k.marking ?? (((k.year % 10) + 4) % 5) + 1;
  const alt = await db.get<any>(
    `SELECT id FROM queen WHERE hive_id = ? AND active = 1 AND deleted = 0 LIMIT 1`,
    [hiveId]
  );
  if (alt) {
    await patch('queen', alt.id, scopeApiaryId, { active: 0, replaced_at: ts });
    await emit(EventType.QUEEN_REPLACED, scopeApiaryId, {
      date: ts,
      hive_id: hiveId,
      queen_id: alt.id
    });
  }
  const neu = crypto.randomUUID();
  await patch('queen', neu, scopeApiaryId, {
    organization_id: orgId(),
    hive_id: hiveId,
    year: k.year,
    marking,
    origin: k.origin ?? '',
    breeder_number: k.breeder_number ?? '',
    note: k.note ?? '',
    introduced_at: ts,
    replaced_at: null,
    active: 1,
    created_at: ts,
    updated_at: ts,
    deleted: 0
  });
  await emit(EventType.QUEEN_INTRODUCED, scopeApiaryId, {
    date: ts,
    hive_id: hiveId,
    queen_id: neu,
    title: `Queen ${k.year}`
  });
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
  if (open) await patch('placement', open.id, fromApiary, { end_at: ts });
  await patch('placement', crypto.randomUUID(), toApiary, {
    organization_id: orgId(),
    hive_id: hiveId,
    apiary_id: toApiary,
    start_at: ts,
    end_at: null,
    deleted: 0
  });
  await patch('hive', hiveId, toApiary, { apiary_id: toApiary, updated_at: ts });
  await emit(EventType.MOVED, toApiary, {
    date: ts,
    apiary_id: toApiary,
    hive_id: hiveId,
    detail: { from: fromApiary, to: toApiary }
  });
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
  await patch('harvest', harvestId, ctx.apiaryId, {
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
  await emit(EventType.HARVEST, ctx.apiaryId, {
    date,
    apiary_id: ctx.apiaryId,
    hive_id: input.hiveId,
    queen_id: ctx.queenId,
    ref_entity: 'harvest',
    ref_id: harvestId,
    amount_kg: input.amount_kg,
    title: `${input.amount_kg} kg ${input.variety ?? 'Honey'}`
  });
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
  await patch('treatment', treatmentId, ctx.apiaryId, {
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
  await emit(EventType.TREATMENT, ctx.apiaryId, {
    date,
    apiary_id: ctx.apiaryId,
    hive_id: input.hiveId,
    queen_id: ctx.queenId,
    ref_entity: 'treatment',
    ref_id: treatmentId,
    title: input.product
  });
  return treatmentId;
}

// Inspection with a context event (completes the history).
export async function recordInspection(hiveId: string, d: any) {
  const date = d.date ?? now();
  const ctx = await resolveContext(hiveId, date);
  const id = crypto.randomUUID();
  await patch('inspection', id, ctx.apiaryId, {
    organization_id: orgId(),
    ...d,
    hive_id: hiveId,
    date,
    created_at: now(),
    deleted: 0
  });
  await emit(EventType.INSPECTION, ctx.apiaryId, {
    date,
    apiary_id: ctx.apiaryId,
    hive_id: hiveId,
    queen_id: ctx.queenId,
    ref_entity: 'inspection',
    ref_id: id,
    title: 'Inspection'
  });
  return id;
}

// --- Statistics straight from the fact table (type = HARVEST) ---

export async function honeyByYear() {
  const db = await getDB();
  return db.all(`SELECT substr(date,1,4) AS year, SUM(amount_kg) AS kg
                 FROM event WHERE type = 7 AND deleted = 0 GROUP BY year ORDER BY year`);
}
