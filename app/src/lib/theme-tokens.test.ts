// Guards the colour tokens in routes/+layout.svelte: both themes define the
// same set, and every foreground / background pairing the components rely on
// meets WCAG 2.1 AA (4.5:1 for text, 3:1 for control borders and markers).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import path from 'node:path';

const css = readFileSync(path.resolve(__dirname, '../routes/+layout.svelte'), 'utf8');

/** Hex-valued custom properties of the first rule whose selector matches. */
function tokens(selector: string): Record<string, string> {
  const start = css.indexOf(selector);
  expect(start, `no rule for ${selector}`).toBeGreaterThan(-1);
  const block = css.slice(css.indexOf('{', start) + 1, css.indexOf('}', start));
  const out: Record<string, string> = {};
  for (const m of block.matchAll(/--([a-z-]+):\s*(#[0-9a-f]{6})\b/gi)) out[m[1]!] = m[2]!;
  return out;
}

function luminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  const [r, g, b] = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((v) => {
    const c = v / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!;
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
}

const SEMANTIC = [
  'bg',
  'surface',
  'text',
  'text-soft',
  'border',
  'border-strong',
  'accent',
  'on-accent',
  'accent-text',
  'danger',
  'on-danger',
  'ok',
  'warn-bg',
  'warn-text'
];

// [foreground, background, minimum ratio]
const PAIRS: [string, string, number][] = [
  ['text', 'bg', 4.5],
  ['text', 'surface', 4.5],
  ['text-soft', 'bg', 4.5],
  ['text-soft', 'surface', 4.5],
  ['accent-text', 'bg', 4.5],
  ['accent-text', 'surface', 4.5],
  ['on-accent', 'accent', 4.5],
  ['on-danger', 'danger', 4.5],
  ['danger', 'bg', 4.5],
  ['danger', 'surface', 4.5],
  ['ok', 'bg', 4.5],
  ['ok', 'surface', 4.5],
  ['warn-text', 'warn-bg', 4.5],
  // Toasts and the offline banner invert the page colours.
  ['bg', 'text', 4.5],
  ['surface', 'text', 4.5],
  ['border-strong', 'bg', 3],
  ['border-strong', 'surface', 3],
  ['accent', 'bg', 3],
  ['accent', 'surface', 3]
];

const themes = {
  light: tokens(':global(:root) {'),
  dark: tokens(":global(:root[data-theme='dark']) {")
};

describe.each(Object.entries(themes))('%s theme tokens', (_name, theme) => {
  it('defines every semantic token as a colour', () => {
    for (const name of SEMANTIC) expect(theme[name], `--${name}`).toMatch(/^#[0-9a-f]{6}$/i);
  });

  it.each(PAIRS)('--%s on --%s has a contrast of at least %d:1', (fg, bg, min) => {
    expect(contrast(theme[fg]!, theme[bg]!)).toBeGreaterThanOrEqual(min);
  });
});

describe('contrast maths', () => {
  it('matches the WCAG reference values', () => {
    expect(contrast('#000000', '#ffffff')).toBeCloseTo(21, 5);
    expect(contrast('#777777', '#ffffff')).toBeCloseTo(4.48, 2);
  });
});
