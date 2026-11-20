import { describe, it, expect } from 'vitest';
import { chipView, ago, codeKey, entityKey } from './status';
import type { SyncStatus } from '#lib/local/sync.js';

const base: SyncStatus = { state: 'idle', pending: 0, failed: 0, lastSyncedAt: 1_000 };

describe('status chip', () => {
  it('says synced only when nothing waits and nothing was refused', () => {
    expect(chipView(base)).toEqual({ tone: 'ok', key: 'sync.chip_synced', n: 0 });
    expect(chipView({ ...base, lastSyncedAt: null })).toMatchObject({ key: 'sync.never_synced' });
    expect(chipView({ ...base, pending: 3 })).toEqual({
      tone: 'busy',
      key: 'sync.chip_pending',
      n: 3
    });
    expect(chipView({ ...base, state: 'syncing', pending: 2 })).toMatchObject({
      tone: 'busy',
      key: 'sync.chip_syncing'
    });
  });

  it('explains why nothing moves', () => {
    expect(chipView({ ...base, state: 'offline' })).toMatchObject({
      tone: 'warn',
      key: 'sync.chip_offline'
    });
    expect(chipView({ ...base, state: 'offline', pending: 4 })).toEqual({
      tone: 'warn',
      key: 'sync.chip_offline_pending',
      n: 4
    });
    expect(chipView({ ...base, state: 'error', pending: 1 })).toMatchObject({
      tone: 'warn',
      key: 'sync.chip_error'
    });
  });

  it('puts what needs the user first', () => {
    expect(chipView({ ...base, state: 'offline', pending: 4, failed: 2 })).toEqual({
      tone: 'bad',
      key: 'sync.chip_problems',
      n: 2
    });
    expect(chipView({ ...base, state: 'signed_out', failed: 2 })).toMatchObject({
      tone: 'bad',
      key: 'sync.chip_signed_out'
    });
  });
});

describe('relative time', () => {
  const now = 10 * 86_400_000;
  it('is empty within the first minute, then counts minutes, hours and days', () => {
    expect(ago(now - 59_000, now, 'en')).toBeNull();
    expect(ago(now + 5_000, now, 'en')).toBeNull(); // clock skew: never "in 5 seconds"
    expect(ago(now - 5 * 60_000, now, 'en')).toBe('5 min. ago');
    expect(ago(now - 3 * 3_600_000, now, 'en')).toBe('3 hr. ago');
    expect(ago(now - 86_400_000, now, 'en')).toBe('yesterday');
    expect(ago(now - 4 * 86_400_000, now, 'de')).toBe('vor 4 Tagen');
    expect(ago(now - 120_000, now, null)).toBeTruthy();
  });
});

describe('problem texts', () => {
  it('map known codes and entities, and fall back for unknown ones', () => {
    expect(codeKey('wrong_tenant')).toBe('sync.code_wrong_tenant');
    expect(codeKey('too_large')).toBe('sync.code_too_large');
    expect(codeKey('something_new')).toBe('sync.code_other');
    expect(entityKey('inspection')).toBe('sync.entity_inspection');
    expect(entityKey('gadget')).toBe('sync.entity_other');
  });

  it('every key the helpers can return exists in every language', async () => {
    const keys = [
      ...[
        'wrong_tenant',
        'forbidden_scope',
        'invalid',
        'clock_ahead',
        'too_large',
        'unknown_entity',
        'x'
      ].map(codeKey),
      ...[
        'apiary',
        'hive',
        'queen',
        'inspection',
        'task',
        'placement',
        'harvest',
        'treatment',
        'event',
        'x'
      ].map(entityKey),
      ...[
        { ...base },
        { ...base, lastSyncedAt: null },
        { ...base, pending: 1 },
        { ...base, state: 'syncing' as const },
        { ...base, state: 'offline' as const },
        { ...base, state: 'offline' as const, pending: 1 },
        { ...base, state: 'error' as const },
        { ...base, state: 'signed_out' as const },
        { ...base, failed: 1 }
      ].map((s) => chipView(s).key)
    ];
    for (const lang of ['en', 'de', 'fr', 'es', 'it']) {
      const messages = (await import(`../../i18n/locales/${lang}.json`)).default;
      for (const key of keys) {
        const [ns, name] = key.split('.') as [string, string];
        expect(messages[ns]?.[name], `${lang}: ${key}`).toBeTruthy();
      }
    }
  });
});
