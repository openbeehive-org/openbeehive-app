// The account settings as this device applies them: the units weights and
// temperatures are shown in, and the language. The account's record
// (lib/local/userSettings.ts) carries them to the user's other devices; this
// module keeps what the device last applied, so the app starts with it before
// any database is open, before sign-in and offline. The colour theme is not
// an account setting: it stays per device (theme.ts).
import { writable, type Readable } from 'svelte/store';
import { compare, hlc } from './local/hlc';
import { defaultUnits, isTempUnit, isWeightUnit, type Units } from './units';

export const SETTING_FIELDS = ['weight_unit', 'temp_unit', 'language'] as const;
export type SettingField = (typeof SETTING_FIELDS)[number];

/**
 * One applied setting: its value, the stamp of the choice it comes from, and
 * the account that made the choice ('' when nobody was signed in).
 */
export type Choice = { v: string; s: string; u: string };
export type Choices = Partial<Record<SettingField, Choice>>;

const KEY = 'obh.settings';

const isChoice = (c: unknown): c is Choice =>
  !!c &&
  typeof c === 'object' &&
  ['v', 's', 'u'].every((k) => typeof (c as Record<string, unknown>)[k] === 'string');

/** What this device last applied; anything unreadable counts as nothing. */
export function loadChoices(): Choices {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(KEY) ?? '{}');
    const out: Choices = {};
    for (const f of SETTING_FIELDS) {
      const c = (raw as Record<string, unknown>)?.[f];
      if (isChoice(c)) out[f] = c;
    }
    return out;
  } catch {
    return {};
  }
}

function storeChoice(field: SettingField, c: Choice) {
  try {
    localStorage.setItem(KEY, JSON.stringify({ ...loadChoices(), [field]: c }));
  } catch {
    // storage blocked: the choice applies until the page is closed
  }
}

function browserLocale(): string | undefined {
  return typeof navigator === 'undefined' ? undefined : navigator.language;
}

// The language the app shows: the account's choice, else the one this
// device last applied (lib/i18n), else none yet (the browser's).
function appLanguage(c: Choices): string {
  if (c.language?.v) return c.language.v;
  try {
    return localStorage.getItem('lang') ?? '';
  } catch {
    return '';
  }
}

/** The units to show: the device's choices, else the defaults for the app's language. */
export function unitsFrom(c: Choices, tag = browserLocale()): Units {
  const d = defaultUnits(appLanguage(c), tag);
  const weight = c.weight_unit?.v;
  const temp = c.temp_unit?.v;
  return {
    weight: isWeightUnit(weight) ? weight : d.weight,
    temp: isTempUnit(temp) ? temp : d.temp
  };
}

const unitStore = writable<Units>(unitsFrom(loadChoices()));
/** The units every screen shows and reads weights and temperatures in. */
export const units: Readable<Units> = { subscribe: unitStore.subscribe };

function apply(field: SettingField, c: Choice) {
  storeChoice(field, c);
  // The language matters too: it sets the units until some are chosen.
  unitStore.set(unitsFrom(loadChoices()));
}

const signedInUser = () => {
  try {
    return localStorage.getItem('obh.userId') ?? '';
  } catch {
    return '';
  }
};

/** A choice the user makes on this device, stamped now. */
export function chooseOnDevice(field: SettingField, value: string): Choice {
  const c = { v: value, s: hlc.now(), u: signedInUser() };
  apply(field, c);
  return c;
}

/** Takes over a value of the account's record. */
export function adoptRecord(field: SettingField, value: string, stamp: string, user: string) {
  apply(field, { v: value, s: stamp, u: user });
}

/** One field of the account's record as stored on this device. */
export type RecordField = { v: string; s: string };

/**
 * What to do with one setting when the account's record is read:
 *   adopt  take the record's value
 *   keep   leave the device as it is
 *   send   keep the device's value and write it into the record, with the
 *          stamp of the original choice
 *
 * The newer stamp wins, not whatever arrived last: a choice the user made on
 * this device after the record's value stays. A choice made by another
 * account on this device never stands against this account's record, and is
 * never written into it. A choice of this account that the record lacks was
 * made in another workspace's database on this device and has not been sent
 * from there yet; it is sent from here too, but only once this database holds
 * everything the server has (`complete`), so a record that is merely still
 * on its way is not mistaken for a missing one.
 */
export function decide(
  local: Choice | undefined,
  record: RecordField | undefined,
  user: string,
  complete: boolean
): 'adopt' | 'keep' | 'send' {
  const mine = !!local && (local.u === user || local.u === '');
  if (record && record.v !== '') {
    if (!mine || compare(local!.s, record.s) <= 0) return 'adopt';
    return local!.u === user && complete ? 'send' : 'keep';
  }
  return mine && local!.u === user && complete ? 'send' : 'keep';
}
