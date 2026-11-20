// The printable QR label of a hive.
import { escapeHtml } from './html';

export type QrLabel = {
  /** Hive name: typed by a user, or arrived through sync or an import. */
  name: string;
  /** Short code printed under the name. */
  code: string;
  /** The QR code as rendered by qr.ts (built locally from the hive id). */
  svg: string;
};

/**
 * Body markup of the label. Name and code are escaped: they are the only
 * parts that did not originate in this app's own code.
 */
export function qrLabelMarkup({ name, code, svg }: QrLabel): string {
  return `${svg}<div class="name">${escapeHtml(name)}</div><div class="code">${escapeHtml(code)}</div>`;
}

/**
 * Opens the label in its own window (no app chrome) and prints it. Returns
 * false when the browser blocked the window.
 *
 * The window inherits the app's Content-Security-Policy, which rules out
 * inline scripts and inline styles: the layout is applied through style
 * properties and printing is triggered from here.
 */
export function printQrLabel(label: QrLabel): boolean {
  const w = window.open('', '_blank', 'width=420,height=560');
  if (!w) return false;
  const doc = w.document;
  doc.title = label.name || label.code;
  doc.body.innerHTML = qrLabelMarkup(label);

  const body = doc.body.style;
  body.margin = '0';
  body.padding = '28px';
  body.textAlign = 'center';
  body.fontFamily = 'system-ui, sans-serif';
  // Printed on paper: fixed black on white, independent of the app theme.
  body.background = 'white';
  body.color = 'black';
  const svg = doc.body.querySelector('svg');
  if (svg) {
    svg.style.width = '280px';
    svg.style.height = 'auto';
  }
  const name = doc.body.querySelector<HTMLElement>('.name');
  if (name) {
    name.style.fontSize = '20px';
    name.style.fontWeight = '700';
    name.style.margin = '14px 0 2px';
  }
  const code = doc.body.querySelector<HTMLElement>('.code');
  if (code) code.style.letterSpacing = '0.18em';

  w.focus();
  setTimeout(() => w.print(), 150);
  return true;
}
