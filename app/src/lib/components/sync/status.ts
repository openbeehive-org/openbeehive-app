// What the status chip and the problems list show, derived from the sync
// status. Kept apart from the components so it can be tested without a DOM.
import type { SyncStatus } from '#lib/local/sync.js';

// ok: everything is on the server. busy: work in progress, nothing to do.
// warn: sync is not happening right now. bad: needs the user.
export type ChipTone = 'ok' | 'busy' | 'warn' | 'bad';

export interface ChipView {
  tone: ChipTone;
  key: string; // translation key of the label
  n: number; // count the label refers to (0 when it has none)
}

// One line for the chip. What needs the user comes first, then what explains
// why nothing moves, then progress.
export function chipView(s: SyncStatus): ChipView {
  if (s.state === 'signed_out') return { tone: 'bad', key: 'sync.chip_signed_out', n: 0 };
  if (s.failed > 0) return { tone: 'bad', key: 'sync.chip_problems', n: s.failed };
  if (s.state === 'offline') {
    return s.pending > 0
      ? { tone: 'warn', key: 'sync.chip_offline_pending', n: s.pending }
      : { tone: 'warn', key: 'sync.chip_offline', n: 0 };
  }
  if (s.state === 'error') return { tone: 'warn', key: 'sync.chip_error', n: s.pending };
  if (s.state === 'syncing') return { tone: 'busy', key: 'sync.chip_syncing', n: s.pending };
  if (s.pending > 0) return { tone: 'busy', key: 'sync.chip_pending', n: s.pending };
  if (s.lastSyncedAt === null) return { tone: 'busy', key: 'sync.never_synced', n: 0 };
  return { tone: 'ok', key: 'sync.chip_synced', n: 0 };
}

// How long ago `at` was, in the UI language ("5 min. ago", "yesterday").
// Returns null within the first minute, where callers say "just now".
export function ago(at: number, now: number, locale: string | null | undefined): string | null {
  const sec = Math.max(0, Math.round((now - at) / 1000));
  if (sec < 60) return null;
  const rtf = new Intl.RelativeTimeFormat(locale || undefined, { numeric: 'auto', style: 'short' });
  if (sec < 3600) return rtf.format(-Math.floor(sec / 60), 'minute');
  if (sec < 86_400) return rtf.format(-Math.floor(sec / 3600), 'hour');
  return rtf.format(-Math.floor(sec / 86_400), 'day');
}

const CODES = [
  'wrong_tenant',
  'forbidden_scope',
  'invalid',
  'clock_ahead',
  'too_large',
  'unknown_entity'
];
const ENTITIES = [
  'apiary',
  'hive',
  'queen',
  'inspection',
  'task',
  'placement',
  'harvest',
  'treatment',
  'event'
];

// Translation key explaining a rejection code; codes this app version does
// not know get the generic text.
export const codeKey = (code: string) =>
  CODES.includes(code) ? `sync.code_${code}` : 'sync.code_other';

export const entityKey = (entity: string) =>
  ENTITIES.includes(entity) ? `sync.entity_${entity}` : 'sync.entity_other';
