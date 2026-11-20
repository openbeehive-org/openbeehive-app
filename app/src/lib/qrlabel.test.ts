import { describe, it, expect, vi, afterEach } from 'vitest';
import { printQrLabel, qrLabelMarkup } from './qrlabel';

const svg = '<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0h1v1z"/></svg>';

describe('qrLabelMarkup', () => {
  it('prints a hive name as text, never as markup', () => {
    const html = qrLabelMarkup({
      name: '</div><script>alert(1)</script><img src=x onerror=alert(2)>',
      code: 'AB12CD',
      svg
    });
    expect(html).not.toContain('<script');
    expect(html).not.toContain('<img');
    expect(html).toContain('&lt;script&gt;alert(1)&lt;/script&gt;');
  });

  it('escapes the code as well', () => {
    expect(qrLabelMarkup({ name: 'Hive 1', code: '"><b>', svg })).toContain(
      '<div class="code">&quot;&gt;&lt;b&gt;</div>'
    );
  });

  it('keeps the locally rendered QR code intact', () => {
    expect(qrLabelMarkup({ name: 'Hive 1', code: 'AB12CD', svg })).toBe(
      `${svg}<div class="name">Hive 1</div><div class="code">AB12CD</div>`
    );
  });
});

describe('printQrLabel', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  // A stand-in for the opened window: records what the label code does to it.
  function fakeWindow() {
    const parts: Record<string, { style: Record<string, string> }> = {
      svg: { style: {} },
      '.name': { style: {} },
      '.code': { style: {} }
    };
    const body = {
      innerHTML: '',
      style: {} as Record<string, string>,
      querySelector: (selector: string) => parts[selector] ?? null
    };
    const w = { document: { title: '', body }, focus: vi.fn(), print: vi.fn() };
    return { w, body, parts };
  }

  it('returns false when the browser blocks the window', () => {
    vi.stubGlobal('window', { open: () => null });
    expect(printQrLabel({ name: 'Hive 1', code: 'AB12CD', svg })).toBe(false);
  });

  it('fills the window with escaped markup, styles it by property and prints', () => {
    vi.useFakeTimers();
    const { w, body, parts } = fakeWindow();
    vi.stubGlobal('window', { open: () => w });

    expect(printQrLabel({ name: '<b>Hive</b>', code: 'AB12CD', svg })).toBe(true);

    expect(w.document.title).toBe('<b>Hive</b>'); // a title is text, not markup
    expect(body.innerHTML).toBe(qrLabelMarkup({ name: '<b>Hive</b>', code: 'AB12CD', svg }));
    expect(body.innerHTML).not.toContain('<b>');
    expect(body.innerHTML).not.toMatch(/<style|<script|style=/);
    expect(parts['.name']?.style.fontSize).toBe('20px');
    expect(parts.svg?.style.width).toBe('280px');
    expect(body.style.background).toBe('white');

    expect(w.print).not.toHaveBeenCalled();
    vi.runAllTimers();
    expect(w.print).toHaveBeenCalledOnce();
  });

  it('titles a nameless label with its code', () => {
    const { w } = fakeWindow();
    vi.stubGlobal('window', { open: () => w });
    printQrLabel({ name: '', code: 'AB12CD', svg });
    expect(w.document.title).toBe('AB12CD');
  });
});
