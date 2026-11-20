// The apiary report: every apiary with its hives, their current queen and
// what the last visit recorded, printed from its own window ("Save as PDF").
import { escapeHtml } from '../html';
import { formatDate } from '../dates';
import { openPrintWindow, SHEET_CSS } from '../print';
import { formatQuantity, formatNumber, type Units } from '../units';
import type { Dataset, Row } from './portability';

type Translate = (key: string, options?: { values?: Record<string, string | number> }) => string;

const SENSOR = 2;

// The newest visit a person recorded per hive; sensor readings do not count.
function lastVisits(visits: Row[]): Map<string, Row> {
  const last = new Map<string, Row>();
  const newer = (a: Row, b: Row) =>
    String(a.date) > String(b.date) ||
    (a.date === b.date && String(a.created_at ?? '') > String(b.created_at ?? ''));
  for (const v of visits) {
    if (Number(v.source ?? 0) === SENSOR) continue;
    const cur = last.get(v.hive_id);
    if (!cur || newer(v, cur)) last.set(v.hive_id, v);
  }
  return last;
}

/** The report as an HTML fragment; every stored value is escaped. */
export function reportHtml(data: Dataset, t: Translate, locale: string, units: Units): string {
  const hivesOf = new Map<string, Row[]>();
  for (const h of data.hive ?? [])
    hivesOf.set(h.apiary_id, [...(hivesOf.get(h.apiary_id) ?? []), h]);
  const queenOf = new Map<string, Row>();
  for (const q of data.queen ?? []) if (q.active) queenOf.set(q.hive_id, q);
  const visitOf = lastVisits(data.inspection ?? []);

  const has = (v: unknown) => v !== null && v !== undefined && v !== '';
  const readings = (v: Row) =>
    [
      has(v.temp_hive) ? formatQuantity('temp', Number(v.temp_hive), units, locale) : '',
      has(v.humidity_hive) ? `${formatNumber(Number(v.humidity_hive), locale, 0)} %` : '',
      has(v.weight_kg) ? formatQuantity('weight', Number(v.weight_kg), units, locale) : ''
    ]
      .filter(Boolean)
      .join(' · ');
  const queen = (q: Row | undefined) =>
    q ? [q.year, q.marking ? t(`queens.color.${q.marking}`) : ''].filter(Boolean).join(' ') : '';

  const head = [
    'sync.entity_hive',
    'queens.title',
    'hives.last_inspection',
    'data.report_readings',
    'insp.note'
  ]
    .map((k) => `<th scope="col">${escapeHtml(t(k))}</th>`)
    .join('');
  const sections = (data.apiary ?? []).map((a) => {
    const rows = (hivesOf.get(a.id) ?? []).map((h) => {
      const v = visitOf.get(h.id) ?? {};
      const cells = [
        h.name,
        queen(queenOf.get(h.id)),
        formatDate(v.date, locale),
        readings(v),
        v.note
      ];
      return `<tr>${cells.map((c) => `<td>${escapeHtml(c || '–')}</td>`).join('')}</tr>`;
    });
    const body = rows.length
      ? `<table><thead><tr>${head}</tr></thead><tbody>${rows.join('')}</tbody></table>`
      : `<p class="none">${escapeHtml(t('apiaries.no_hives'))}</p>`;
    const address = a.address ? `<p class="meta">${escapeHtml(a.address)}</p>` : '';
    return `<section><h2>${escapeHtml(a.name)}</h2>${address}${body}</section>`;
  });

  const summary = t('data.report_summary', {
    values: {
      date: formatDate(new Date().toISOString(), locale),
      apiaries: (data.apiary ?? []).length,
      hives: (data.hive ?? []).length
    }
  });
  return (
    `<h1>${escapeHtml(t('data.report_title'))}</h1>` +
    `<p class="meta">${escapeHtml(summary)}</p>` +
    (sections.join('') || `<p class="none">${escapeHtml(t('data.report_empty'))}</p>`)
  );
}

/** Opens the report in a print window; false when the browser blocked it. */
export function printReport(data: Dataset, t: Translate, locale: string, units: Units): boolean {
  return openPrintWindow({
    title: t('data.report_title'),
    lang: locale,
    body: reportHtml(data, t, locale, units),
    css: SHEET_CSS
  });
}
