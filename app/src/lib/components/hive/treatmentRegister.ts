// The treatment register (the record of veterinary medicines a beekeeper has
// to be able to show): one table per hive, printed from its own window.
import { escapeHtml } from '#lib/html.js';
import { formatDate, toDateInput, todayLocal } from '#lib/dates.js';
import { openPrintWindow, SHEET_CSS } from '#lib/print.js';
import type { TreatmentRow } from '../../../routes/(app)/rows.js';

/** True while the withdrawal period is still running (its last day included). */
export function withdrawalActive(until: string | null | undefined, today = todayLocal()): boolean {
  const day = toDateInput(until);
  return day !== '' && day >= today;
}

export type RegisterHive = { name: string; apiary?: string; treatments: TreatmentRow[] };

type Translate = (key: string, options?: { values?: Record<string, string | number> }) => string;

const COLUMNS = [
  'insp.date',
  'treat.product',
  'treat.active',
  'treat.dose',
  'treat.method',
  'treat.batch',
  'treat.withdrawal',
  'treat.amended_col'
];

/** The register as an HTML fragment; every stored value is escaped. */
export function treatmentRegisterHtml(hives: RegisterHive[], t: Translate, locale: string): string {
  const date = (v: string | null | undefined) => escapeHtml(formatDate(v, locale));
  const head = COLUMNS.map((c) => `<th scope="col">${escapeHtml(t(c))}</th>`).join('');

  const sections = hives.map((h) => {
    const oldestFirst = [...h.treatments].sort((a, b) => a.date.localeCompare(b.date));
    const rows = oldestFirst.map((r) => {
      const cells = [
        date(r.date),
        escapeHtml(r.product),
        escapeHtml(r.active_ingredient),
        escapeHtml(r.dose),
        escapeHtml(r.method),
        escapeHtml(r.batch_number),
        date(r.withdrawal_until),
        r.amended_at
          ? escapeHtml(t('treat.amended', { values: { date: formatDate(r.amended_at, locale) } }))
          : ''
      ];
      return `<tr>${cells.map((c) => `<td>${c}</td>`).join('')}</tr>`;
    });
    const title = h.apiary ? `${h.name} · ${h.apiary}` : h.name;
    const body = rows.length
      ? `<table><thead><tr>${head}</tr></thead><tbody>${rows.join('')}</tbody></table>`
      : `<p class="none">${escapeHtml(t('treat.none'))}</p>`;
    return `<section><h2>${escapeHtml(title)}</h2>${body}</section>`;
  });

  const printed = t('treat.register_printed', {
    values: { date: formatDate(new Date().toISOString(), locale) }
  });
  return (
    `<h1>${escapeHtml(t('treat.register'))}</h1>` +
    `<p class="meta">${escapeHtml(printed)}</p>` +
    sections.join('')
  );
}

/**
 * Opens the register for one or several hives in a print window.
 * Returns false when the browser blocked the window.
 */
export function printTreatmentRegister(hives: RegisterHive[], t: Translate, locale: string) {
  return openPrintWindow({
    title: t('treat.register'),
    lang: locale,
    body: treatmentRegisterHtml(hives, t, locale),
    css: SHEET_CSS
  });
}
