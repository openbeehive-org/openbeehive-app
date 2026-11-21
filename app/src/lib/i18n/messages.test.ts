import { beforeAll, describe, expect, it } from 'vitest';
import { addMessages, init, locale, _ } from 'svelte-i18n';
import { get } from 'svelte/store';
import en from './locales/en.json';
import de from './locales/de.json';
import fr from './locales/fr.json';
import es from './locales/es.json';
import it_ from './locales/it.json';

const LOCALES = { en, de, fr, es, it: it_ } as const;
type Lang = keyof typeof LOCALES;

beforeAll(() => {
  for (const [lang, messages] of Object.entries(LOCALES)) addMessages(lang, messages);
  init({ fallbackLocale: 'en', initialLocale: 'en' });
});

function t(lang: Lang, key: string, values?: Record<string, string | number>) {
  locale.set(lang);
  return get(_)(key, values ? { values } : undefined);
}

describe('plural forms', () => {
  const hives: Record<Lang, [string, string, string]> = {
    en: ['0 hives', '1 hive', '2 hives'],
    de: ['0 Beuten', '1 Beute', '2 Beuten'],
    fr: ['0 ruche', '1 ruche', '2 ruches'],
    es: ['0 colmenas', '1 colmena', '2 colmenas'],
    it: ['0 arnie', '1 arnia', '2 arnie']
  };

  it.each(Object.keys(hives) as Lang[])('counts hives correctly in %s', (lang) => {
    expect([0, 1, 2].map((n) => t(lang, 'apiaries.hives_count', { n }))).toEqual(hives[lang]);
    expect(t(lang, 'apiaries.hives_count', { n: 21 })).toMatch(/^21 /);
  });

  it('names the apiary and the number of hives when a delete is refused', () => {
    expect(t('en', 'apiaries.delete_blocked', { name: 'Orchard', n: 1 })).toBe(
      '“Orchard” still has 1 hive. Move or delete it first, then delete the apiary.'
    );
    expect(t('en', 'apiaries.delete_blocked', { name: 'Orchard', n: 3 })).toBe(
      '“Orchard” still has 3 hives. Move or delete them first, then delete the apiary.'
    );
    expect(t('de', 'apiaries.delete_blocked', { name: 'Obstwiese', n: 1 })).toContain('1 Beute.');
    expect(t('fr', 'apiaries.delete_blocked', { name: 'Verger', n: 2 })).toContain(
      '2 ruches. Déplacez-les'
    );
    expect(t('es', 'apiaries.delete_blocked', { name: 'Huerto', n: 1 })).toContain(
      '1 colmena. Muévela'
    );
    expect(t('it', 'apiaries.delete_blocked', { name: 'Frutteto', n: 2 })).toContain(
      '2 arnie. Spostale'
    );
  });
});

describe('counts in every language', () => {
  // Every message that shows a count must read right for one and for many.
  const counted: [string, Record<string, string | number>][] = [
    ['data.pending_note', {}],
    ['data.failed_note', {}],
    ['data.photos_missing', {}],
    ['data.photos_no_image', {}],
    ['data.undo_help', { date: '1.2.2026' }],
    ['data.undone', {}],
    ['data.undone_kept', { kept: 1 }],
    ['stats.points', {}],
    ['sync.sheet_problems', {}]
  ];
  const one: Record<Lang, RegExp> = {
    en: /\b1 (change|photo|record|reading)(?=[\s.,;:]|$)|\b1 day(?=[\s.,;:]|$)/,
    de: /\b1 (Änderung|Foto|Eintrag|Wert)(?=[\s.,;:]|$)/,
    fr: /\b1 (modification|photo|enregistrement|relevé)(?=[\s.,;:]|$)/,
    es: /\b1 (cambio|foto|registro|lectura)(?=[\s.,;:]|$)/,
    it: /\b1 (modifica|foto|record|rilevazione)(?=[\s.,;:]|$)/
  };
  const wrongOne: Record<Lang, RegExp> = {
    en: /\b1 (changes|photos|records|readings|days)(?=[\s.,;:]|$)|were refused|are missing/,
    de: /\b1 (Änderungen|Fotos|Einträge|Werte|Tagen)(?=[\s.,;:]|$)/,
    fr: /\b1 (modifications|photos|enregistrements|relevés|jours)(?=[\s.,;:]|$)/,
    es: /\b1 (cambios|fotos|registros|lecturas|días)(?=[\s.,;:]|$)/,
    it: /\b1 (modifiche|record eliminati|rilevazioni|giorni)(?=[\s.,;:]|$)/
  };

  it.each(Object.keys(LOCALES) as Lang[])('uses the singular for 1 in %s', (lang) => {
    for (const [key, extra] of counted) {
      const text = t(lang, key, { n: 1, ...extra });
      expect(text, `${lang} ${key}`).toMatch(one[lang]);
      expect(text, `${lang} ${key}`).not.toMatch(wrongOne[lang]);
      expect(text, `${lang} ${key}`).not.toMatch(/[{}]/);
    }
    const day = t(lang, 'dashboard.days_ago', { days: 1 });
    expect(day, `${lang} days_ago`).not.toMatch(wrongOne[lang]);
    expect(day).toContain('1');
  });

  it('uses the plural for several', () => {
    expect(t('en', 'data.failed_note', { n: 3 })).toMatch(/^3 changes were refused/);
    expect(t('de', 'stats.points', { n: 2 })).toBe('2 Werte');
    expect(t('de', 'stats.points', { n: 1 })).toBe('1 Wert');
    expect(t('en', 'stats.harvests', { n: 1 })).toBe('1 harvest');
    expect(t('en', 'stats.harvests', { n: 4 })).toBe('4 harvests');
    expect(t('de', 'stats.harvests', { n: 1 })).toBe('1 Ernte');
    expect(t('fr', 'stats.visits', { n: 2 })).toBe('2 visites');
    expect(t('es', 'stats.visits', { n: 1 })).toBe('1 visita');
    expect(t('it', 'stats.harvests', { n: 1 })).toBe('1 raccolto');
    expect(t('de', 'dashboard.days_ago', { days: 1 })).toBe('vor 1 Tag');
    expect(t('de', 'dashboard.days_ago', { days: 5 })).toBe('vor 5 Tagen');
    expect(t('en', 'data.undone_kept', { n: 1, kept: 2 })).toBe(
      'Import undone: 1 record deleted. 2 were kept because records were added to them since.'
    );
  });

  it('gives every count message a plural form in every language', () => {
    // A message that names a count ({n}) with a noun after it needs plural
    // forms; ordinal or label uses ("Photo {n} of {total}") do not.
    const counts = [
      'stats.visits',
      'stats.harvests',
      'stats.points',
      'data.failed_note',
      'data.pending_note'
    ];
    for (const lang of Object.keys(LOCALES) as Lang[]) {
      for (const key of counts) {
        expect(t(lang, key, { n: 1 }), `${lang} ${key}`).not.toBe(
          t(lang, key, { n: 2 }).replace('2', '1')
        );
      }
    }
  });

  it('shows no count in the chip while offline', () => {
    for (const lang of Object.keys(LOCALES) as Lang[])
      expect(t(lang, 'sync.chip_offline_pending', { n: 1 })).not.toMatch(/\d/);
  });
});

describe('messages with placeholders', () => {
  // Every message of the list, dashboard, task, scan and label pages must
  // format in every language without leaving a raw placeholder behind.
  const values = { n: 2, name: 'Orchard', title: 'Feed', days: 3, shown: 1, total: 4 };
  const namespaces = ['dashboard', 'apiaries', 'hivelist', 'tasks', 'scan', 'qr'] as const;

  it.each(Object.keys(LOCALES) as Lang[])('format cleanly in %s', (lang) => {
    for (const ns of namespaces) {
      for (const key of Object.keys(LOCALES[lang][ns])) {
        const text = t(lang, `${ns}.${key}`, values);
        expect(text, `${lang} ${ns}.${key}`).not.toMatch(/[{}]|plural|undefined/);
        expect(text.trim(), `${lang} ${ns}.${key}`).not.toBe('');
      }
    }
  });

  it('names the item in confirmations and labels', () => {
    expect(t('en', 'apiaries.confirm_delete', { name: 'Orchard' })).toBe(
      'Delete apiary “Orchard”? This cannot be undone.'
    );
    expect(t('de', 'tasks.completed', { n: 4 })).toBe('Erledigt (4)');
    expect(t('fr', 'tasks.mark_done', { title: 'Nourrir' })).toBe(
      'Marquer « Nourrir » comme terminée'
    );
    expect(t('it', 'apiaries.deleted_text', { n: 1 })).toBe(
      "Vi appartiene ancora 1 arnia. Apri quell'arnia e spostala in un altro apiario."
    );
  });

  it('has the same keys in every language', () => {
    // Messages nest to any depth (queens.color.1), so compare full key paths.
    const paths = (node: unknown, prefix: string): string[] =>
      typeof node === 'object' && node !== null
        ? Object.entries(node).flatMap(([k, v]) => paths(v, prefix ? `${prefix}.${k}` : k))
        : [prefix];
    const keys = (m: unknown) => paths(m, '').sort();
    for (const lang of Object.keys(LOCALES) as Lang[]) {
      expect(keys(LOCALES[lang]), lang).toEqual(keys(en));
    }
  });
});
