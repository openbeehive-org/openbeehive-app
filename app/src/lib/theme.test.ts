import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { get } from 'svelte/store';
import {
  initTheme,
  parseChoice,
  resolveTheme,
  setTheme,
  themeChoice,
  THEME_KEY,
  type ThemeChoice
} from './theme';

describe('theme choice', () => {
  it('accepts the three choices and treats anything else as system', () => {
    expect(parseChoice('light')).toBe('light');
    expect(parseChoice('dark')).toBe('dark');
    expect(parseChoice('system')).toBe('system');
    expect(parseChoice(null)).toBe('system');
    expect(parseChoice('solarized')).toBe('system');
  });

  it('follows the system only when the choice is system', () => {
    expect(resolveTheme('system', true)).toBe('dark');
    expect(resolveTheme('system', false)).toBe('light');
    expect(resolveTheme('light', true)).toBe('light');
    expect(resolveTheme('dark', false)).toBe('dark');
  });
});

describe('theme-init.js (runs before the first paint)', () => {
  const source = readFileSync(path.resolve(__dirname, '../../static/theme-init.js'), 'utf8');

  // Runs the script against stand-ins for the three browser APIs it touches.
  function run(stored: string | null, systemDark: boolean, storageBlocked = false): string {
    const dataset: Record<string, string> = {};
    vm.runInNewContext(source, {
      localStorage: {
        getItem(key: string) {
          if (storageBlocked) throw new Error('blocked');
          return key === THEME_KEY ? stored : null;
        }
      },
      window: { matchMedia: () => ({ matches: systemDark }) },
      document: { documentElement: { dataset } }
    });
    return dataset.theme ?? '';
  }

  it('resolves every stored value exactly like theme.ts', () => {
    const stored: (string | null)[] = [null, 'system', 'light', 'dark', 'garbage'];
    for (const value of stored) {
      for (const systemDark of [true, false]) {
        const expected = resolveTheme(parseChoice(value) as ThemeChoice, systemDark);
        expect(run(value, systemDark), `${value} / systemDark=${systemDark}`).toBe(expected);
      }
    }
  });

  it('follows the system when storage is blocked', () => {
    expect(run('light', true, true)).toBe('dark');
    expect(run('dark', false, true)).toBe('light');
  });
});

describe('initTheme / setTheme', () => {
  let systemDark: boolean;
  let onSystemChange: (() => void) | undefined;
  let root: { dataset: Record<string, string> };
  let meta: { content: string };
  let stop: () => void;

  beforeEach(() => {
    localStorage.clear();
    systemDark = false;
    onSystemChange = undefined;
    root = { dataset: {} };
    meta = { content: '#c77f22' };
    vi.stubGlobal('document', { documentElement: root, querySelector: () => meta });
    vi.stubGlobal('matchMedia', () => ({
      get matches() {
        return systemDark;
      },
      addEventListener: (_: string, fn: () => void) => (onSystemChange = fn),
      removeEventListener: () => (onSystemChange = undefined)
    }));
    // The page background as the stylesheet would resolve it for the theme.
    vi.stubGlobal('getComputedStyle', () => ({
      getPropertyValue: () => (root.dataset.theme === 'dark' ? ' #1a1510' : ' #fbf6ea')
    }));
  });

  afterEach(() => {
    stop?.();
    vi.unstubAllGlobals();
  });

  it('applies the stored choice', () => {
    localStorage.setItem(THEME_KEY, 'dark');
    stop = initTheme();
    expect(root.dataset.theme).toBe('dark');
    expect(get(themeChoice)).toBe('dark');
  });

  it('follows the system until a choice is made, and again after "system"', () => {
    systemDark = true;
    stop = initTheme();
    expect(root.dataset.theme).toBe('dark');

    setTheme('light');
    expect(root.dataset.theme).toBe('light');
    expect(localStorage.getItem(THEME_KEY)).toBe('light');

    setTheme('system');
    expect(root.dataset.theme).toBe('dark');
    expect(localStorage.getItem(THEME_KEY)).toBeNull();
  });

  it('reacts to the system switching while the app is open', () => {
    stop = initTheme();
    expect(root.dataset.theme).toBe('light');
    systemDark = true;
    onSystemChange?.();
    expect(root.dataset.theme).toBe('dark');

    setTheme('light');
    onSystemChange?.();
    expect(root.dataset.theme).toBe('light'); // an explicit choice wins
  });

  it('colours the browser chrome: brand colour in light, page background in dark', () => {
    stop = initTheme();
    expect(meta.content).toBe('#c77f22');
    setTheme('dark');
    expect(meta.content).toBe('#1a1510');
    setTheme('light');
    expect(meta.content).toBe('#c77f22');
  });

  it('stops listening after cleanup', () => {
    initTheme()();
    expect(onSystemChange).toBeUndefined();
  });
});
