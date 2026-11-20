// Printable sheets (the treatment register, the apiary report): an HTML
// fragment shown in a window of its own, from which the browser prints or
// saves a PDF. The window inherits the app's Content-Security-Policy, so it
// gets neither inline scripts nor <style> elements: the stylesheet is a
// constructed one and printing is started from here.

// System fonts and no colour values: a sheet loads nothing and prints in the
// reader's default ink.
export const SHEET_CSS = `
  body { font-family: system-ui, sans-serif; margin: 24px; font-size: 12px; }
  h1 { font-size: 20px; margin: 0 0 2px; }
  h2 { font-size: 15px; margin: 22px 0 6px; }
  .meta, .none { opacity: 0.7; margin: 0 0 6px; }
  table { border-collapse: collapse; width: 100%; }
  th, td { border: 1px solid currentColor; padding: 5px 7px; text-align: left; vertical-align: top; }
  th { font-weight: 700; }
  section { break-inside: avoid; }
`;

/**
 * Opens `body` (escaped HTML) in a print window and starts printing.
 * Returns false when the browser blocked the window.
 */
export function openPrintWindow(sheet: {
  title: string;
  lang: string;
  body: string;
  css: string;
}): boolean {
  const w = window.open('', '_blank');
  if (!w) return false;
  const doc = w.document;
  doc.title = sheet.title;
  doc.documentElement.lang = sheet.lang;
  doc.body.innerHTML = sheet.body;
  const style = new (w as unknown as typeof globalThis).CSSStyleSheet();
  style.replaceSync(sheet.css);
  doc.adoptedStyleSheets = [style];
  w.focus();
  setTimeout(() => w.print(), 150);
  return true;
}
