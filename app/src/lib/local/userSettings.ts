// The account's settings record (entity "user_settings"): units and
// language, one row per account with the account id as row id, synced in the
// personal scope. The server serves it in every workspace of the account, so
// each workspace's local database on a device holds its own copy of the same
// row; they all merge by field stamp like any other row.
//
// What the device shows comes from lib/prefs.ts. reconcile() brings the two
// together whenever the record may have changed; choose() is the user
// changing a setting.
import { get } from 'svelte/store';
import { getDB } from './db';
import { compare } from './hlc';
import { exclusive } from './lock';
import { initialSyncPending } from './live';
import { parseFieldClock } from './merge';
import { syncOnce } from './sync';
import { applyLanguage, isLang } from '../i18n';
import {
  adoptRecord,
  chooseOnDevice,
  decide,
  loadChoices,
  SETTING_FIELDS,
  type RecordField,
  type SettingField
} from '../prefs';
import { isTempUnit, isWeightUnit } from '../units';

const signedInUser = () => localStorage.getItem('obh.userId') ?? '';

// A value this app can apply. A newer app may store codes this one does not
// know (another language); they are left alone, not applied.
function usable(field: SettingField, v: unknown): v is string {
  if (field === 'weight_unit') return isWeightUnit(v);
  if (field === 'temp_unit') return isTempUnit(v);
  return isLang(v);
}

/** The account's record in the open database, field by field. */
export async function readRecord(
  user: string
): Promise<Partial<Record<SettingField, RecordField>>> {
  const db = await getDB();
  const row = await db.get<Record<string, string | number | null>>(
    `SELECT weight_unit, temp_unit, language, field_hlc, deleted FROM user_settings WHERE id = ?`,
    [user]
  );
  const out: Partial<Record<SettingField, RecordField>> = {};
  if (!row || row.deleted) return out;
  const clock = parseFieldClock(String(row.field_hlc ?? '{}'));
  for (const f of SETTING_FIELDS) {
    const v = row[f];
    if (usable(f, v) && clock[f]) out[f] = { v, s: clock[f] };
  }
  return out;
}

/**
 * Writes settings into the account's record with the stamp of the choice
 * they come from, and queues them for upload. A field the record already
 * holds a newer (or the same) stamp for is left out. Returns whether
 * anything was written.
 */
export async function writeRecord(
  user: string,
  fields: Partial<Record<SettingField, string>>,
  stamp: string
): Promise<boolean> {
  const wrote = await exclusive(async () => {
    const db = await getDB();
    const cur = await db.get<{ field_hlc: string | null }>(
      `SELECT field_hlc FROM user_settings WHERE id = ?`,
      [user]
    );
    const clock = parseFieldClock(cur?.field_hlc);
    const names = SETTING_FIELDS.filter(
      (f) => fields[f] !== undefined && compare(stamp, clock[f]) > 0
    );
    if (names.length === 0) return false;
    const payload: Record<string, string> = {};
    for (const f of names) {
      payload[f] = fields[f]!;
      clock[f] = stamp;
    }
    const cols = ['id', ...names, 'field_hlc'];
    await db.batch([
      {
        sql: `INSERT INTO user_settings (${cols.join(', ')}) VALUES (${cols.map(() => '?').join(', ')})
              ON CONFLICT(id) DO UPDATE SET ${cols
                .slice(1)
                .map((c) => `${c} = excluded.${c}`)
                .join(', ')}`,
        params: [user, ...names.map((f) => payload[f]), JSON.stringify(clock)]
      },
      {
        sql: `INSERT INTO outbox (id, entity, entity_id, scope_id, op, payload, hlc, author_id)
              VALUES (?, 'user_settings', ?, ?, 1, ?, ?, ?)`,
        params: [crypto.randomUUID(), user, `user:${user}`, JSON.stringify(payload), stamp, user]
      }
    ]);
    return true;
  });
  if (wrote) void syncOnce();
  return wrote;
}

/**
 * The user changes a setting: it applies on this device at once and is
 * written into the account's record when someone is signed in.
 */
export async function choose(field: SettingField, value: string): Promise<void> {
  const c = chooseOnDevice(field, value);
  if (field === 'language' && isLang(value)) applyLanguage(value);
  if (c.u) await writeRecord(c.u, { [field]: value }, c.s);
}

/**
 * Brings the device and the account's record together (see decide in
 * prefs.ts): applies what the record holds newer, and sends what this device
 * chose and the record lacks. Called when the database opens and whenever
 * synced data arrives.
 */
export async function reconcile(): Promise<void> {
  const user = signedInUser();
  if (!user) return;
  const record = await readRecord(user);
  const local = loadChoices();
  const complete = !get(initialSyncPending);
  const send = new Map<string, Partial<Record<SettingField, string>>>(); // stamp -> fields
  for (const f of SETTING_FIELDS) {
    const rec = record[f];
    const action = decide(local[f], rec, user, complete);
    if (action === 'adopt' && rec) {
      if (local[f]?.v !== rec.v || local[f]?.s !== rec.s) adoptRecord(f, rec.v, rec.s, user);
      if (f === 'language' && isLang(rec.v)) applyLanguage(rec.v);
    } else if (action === 'send') {
      const c = local[f]!;
      send.set(c.s, { ...send.get(c.s), [f]: c.v });
    }
  }
  for (const [stamp, fields] of send) await writeRecord(user, fields, stamp);
}
