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
  'treat.reason',
  'treat.product',
  'treat.active',
  'treat.dose',
  'treat.method',
  'treat.batch',
  'treat.withdrawal',
  'treat.amended_col'
];

/** The register as an HTML fragment; every stored value is escaped. */
export function treatmentRegisterHtml(
  hives: RegisterHive[],
  t: Translate,
  locale: string,
  scope = ''
): string {
  const date = (v: string | null | undefined) => escapeHtml(formatDate(v, locale));
  const head = COLUMNS.map((c) => `<th scope="col">${escapeHtml(t(c))}</th>`).join('');

  const sections = hives.map((h) => {
    const oldestFirst = [...h.treatments].sort((a, b) => a.date.localeCompare(b.date));
    const rows = oldestFirst.map((r) => {
      const cells = [
        date(r.date),
        escapeHtml(r.reason),
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
    (scope ? `<p class="meta">${escapeHtml(scope)}</p>` : '') +
    `<p class="meta">${escapeHtml(printed)}</p>` +
    (sections.join('') || `<p class="none">${escapeHtml(t('treat.register_empty'))}</p>`)
  );
}

/** Which part of the workspace a register covers; '' means all. */
export type RegisterFilter = { apiary: string; year: string };

type Named = { id: string; name: string };

/** The local calendar year of a stored date. */
const yearOf = (date: string | null | undefined) => toDateInput(date).slice(0, 4);

/** The years that have treatments, newest first. */
export function treatmentYears(treatments: Pick<TreatmentRow, 'date'>[]): string[] {
  return [...new Set(treatments.map((r) => yearOf(r.date)).filter(Boolean))].sort().reverse();
}

/**
 * The register of the whole workspace, one apiary or one year: the hives
 * with treatments in that part, ordered by apiary and hive name. A hive
 * counts for the apiary it stands at.
 */
export function workspaceRegister(
  apiaries: Named[],
  hives: (Named & { apiary_id?: string | null })[],
  treatments: TreatmentRow[],
  filter: RegisterFilter
): RegisterHive[] {
  const apiaryName = new Map(apiaries.map((a) => [a.id, a.name]));
  const byName = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true });
  return hives
    .filter((h) => !filter.apiary || h.apiary_id === filter.apiary)
    .map((h) => ({
      name: h.name,
      apiary: apiaryName.get(h.apiary_id ?? '') ?? '',
      treatments: treatments.filter(
        (r) => r.hive_id === h.id && (!filter.year || yearOf(r.date) === filter.year)
      )
    }))
    .filter((h) => h.treatments.length > 0)
    .sort((a, b) => byName(a.apiary, b.apiary) || byName(a.name, b.name));
}

/**
 * Opens the register for one or several hives in a print window.
 * Returns false when the browser blocked the window.
 */
export function printTreatmentRegister(
  hives: RegisterHive[],
  t: Translate,
  locale: string,
  scope = ''
) {
  return openPrintWindow({
    title: t('treat.register'),
    lang: locale,
    body: treatmentRegisterHtml(hives, t, locale, scope),
    css: SHEET_CSS
  });
}
