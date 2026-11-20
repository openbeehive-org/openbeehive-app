// Local writes as units of work. A unit collects what one user action
// changes (a row, its field clock, the outbox entry that will carry it to
// the server; often several rows) and stores all of it in ONE local
// transaction on commit. A tab that dies in between leaves either the whole
// action or nothing: never a saved row that would not sync, nor half of a
// hive-with-placement-and-event.
//
// repo.ts wraps single writes in a unit; history.ts builds the multi-row
// flows from one.
import { getDB, type LocalDB, type Statement } from './db';
import { hlc } from './hlc';
import { exclusive } from './lock';
import { parseFieldClock, parseORSet, orAdd, orRemove, type FieldClock, type ORSet } from './merge';
import { columnsOf, setColsOf, toScalar } from './tables';
import { syncOnce } from './sync';

type Target = { entity: string; id: string; scopeId: string };
type Intent = Target &
  (
    | { kind: 'patch'; fields: Record<string, unknown> }
    | { kind: 'remove' }
    | { kind: 'setAdd' | 'setRemove'; field: string; elem: string }
  );

// What a unit knows about a row it is writing: read once from the database,
// then kept current across the unit's own writes to that row.
type Draft = { clock: FieldClock; sets: Record<string, ORSet> };

type Plan = { db: LocalDB; stmts: Statement[]; drafts: Map<string, Draft> };

const authorId = () => localStorage.getItem('obh.userId') ?? 'local';

async function columns(db: LocalDB, entity: string): Promise<Set<string>> {
  const cols = await columnsOf(db, entity);
  if (!cols) throw new Error(`"${entity}" is not a synced table`);
  return cols;
}

// The caller has checked entity against the synced tables (columns()).
async function draft(p: Plan, t: Target): Promise<Draft> {
  const key = `${t.entity}\n${t.id}`;
  let d = p.drafts.get(key);
  if (!d) {
    const setCols = setColsOf(t.entity);
    const cur = await p.db.get<Record<string, string | null>>(
      `SELECT ${['field_hlc', ...setCols].join(', ')} FROM ${t.entity} WHERE id = ?`,
      [t.id]
    );
    d = {
      clock: parseFieldClock(cur?.field_hlc),
      sets: Object.fromEntries(setCols.map((c) => [c, parseORSet(cur?.[c])]))
    };
    p.drafts.set(key, d);
  }
  return d;
}

function enqueue(p: Plan, t: Target, op: 1 | 2, payload: unknown, stamp: string) {
  p.stmts.push({
    sql: `INSERT INTO outbox (id, entity, entity_id, scope_id, op, payload, hlc, author_id)
          VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
    params: [
      crypto.randomUUID(),
      t.entity,
      t.id,
      t.scopeId,
      op,
      JSON.stringify(payload),
      stamp,
      authorId()
    ]
  });
}

// Scalar fields: only columns the table really has are written and sent, so
// a key from an import file or a newer screen can neither break the
// statement nor become part of it. Set columns have their own operations.
async function planPatch(p: Plan, t: Target, fields: Record<string, unknown>) {
  const cols = await columns(p.db, t.entity);
  const setCols = setColsOf(t.entity);
  const clean: Record<string, string | number | null> = {};
  for (const [k, v] of Object.entries(fields)) {
    if (k === 'id' || k === 'field_hlc' || v === undefined) continue;
    if (!cols.has(k) || setCols.includes(k)) continue;
    const value = toScalar(v);
    if (value === undefined) throw new Error(`${t.entity}.${k}: not a value a column can hold`);
    clean[k] = value;
  }
  const names = Object.keys(clean);
  if (names.length === 0) return;

  const d = await draft(p, t);
  // Above every stamp already on these fields: an edit made on a device
  // whose clock is behind must still win over what it is editing.
  const stamp = hlc.after(names.map((k) => d.clock[k]));
  for (const k of names) d.clock[k] = stamp;

  const all = ['id', ...names, 'field_hlc'];
  p.stmts.push({
    sql: `INSERT INTO ${t.entity} (${all.join(', ')}) VALUES (${all.map(() => '?').join(', ')})
          ON CONFLICT(id) DO UPDATE SET ${all
            .slice(1)
            .map((c) => `${c} = excluded.${c}`)
            .join(', ')}`,
    params: [t.id, ...names.map((k) => clean[k]), JSON.stringify(d.clock)]
  });
  enqueue(p, t, 1, clean, stamp);
}

async function planRemove(p: Plan, t: Target) {
  await columns(p.db, t.entity);
  const d = await draft(p, t);
  const stamp = hlc.after([d.clock.deleted]);
  d.clock.deleted = stamp;
  p.stmts.push({
    sql: `UPDATE ${t.entity} SET deleted = 1, field_hlc = ? WHERE id = ?`,
    params: [JSON.stringify(d.clock), t.id]
  });
  enqueue(p, t, 2, { deleted: 1 }, stamp);
}

async function planSet(
  p: Plan,
  t: Target,
  kind: 'setAdd' | 'setRemove',
  field: string,
  elem: string
) {
  await columns(p.db, t.entity);
  if (!setColsOf(t.entity).includes(field)) {
    throw new Error(`${t.entity}.${field} is not a set column`);
  }
  const d = await draft(p, t);
  const set = (d.sets[field] ??= {});
  const stamp = hlc.now();
  let delta: unknown;
  if (kind === 'setAdd') {
    orAdd(set, elem, stamp); // tag = HLC of this add
    delta = { add: [elem] };
  } else {
    // The delta carries WHICH add tags this device observed; receivers
    // tombstone only those, so an add another device made concurrently
    // survives (add-wins).
    const observed = [...(set[elem]?.a ?? [])];
    orRemove(set, elem);
    delta = { remove: [elem], removed_tags: { [elem]: observed } };
  }
  p.stmts.push({
    sql: `UPDATE ${t.entity} SET ${field} = ? WHERE id = ?`,
    params: [JSON.stringify(set), t.id]
  });
  enqueue(p, t, 1, { [field]: delta }, stamp);
}

export class Unit {
  private intents: Intent[] = [];

  // Set the given scalar fields of one row (partial per-field delta).
  patch(entity: string, id: string, scopeId: string, fields: Record<string, unknown>): this {
    this.intents.push({ kind: 'patch', entity, id, scopeId, fields });
    return this;
  }

  // Soft-delete one row.
  remove(entity: string, id: string, scopeId: string): this {
    this.intents.push({ kind: 'remove', entity, id, scopeId });
    return this;
  }

  // Add / remove one element of a set column (OR-Set).
  setAdd(entity: string, id: string, scopeId: string, field: string, elem: string): this {
    this.intents.push({ kind: 'setAdd', entity, id, scopeId, field, elem });
    return this;
  }
  setRemove(entity: string, id: string, scopeId: string, field: string, elem: string): this {
    this.intents.push({ kind: 'setRemove', entity, id, scopeId, field, elem });
    return this;
  }

  // Store everything collected so far in one transaction, then nudge sync.
  // Nothing is written before this; a unit that is dropped leaves no trace.
  async commit(): Promise<void> {
    const intents = this.intents;
    this.intents = [];
    if (intents.length === 0) return;
    const wrote = await exclusive(async () => {
      const p: Plan = { db: await getDB(), stmts: [], drafts: new Map() };
      for (const i of intents) {
        if (i.kind === 'patch') await planPatch(p, i, i.fields);
        else if (i.kind === 'remove') await planRemove(p, i);
        else await planSet(p, i, i.kind, i.field, i.elem);
      }
      await p.db.batch(p.stmts);
      return p.stmts.length > 0;
    });
    if (wrote) void syncOnce();
  }
}

export const unit = () => new Unit();
