// Colour theme: follows the system setting unless the user picked Light or
// Dark. The choice is stored per device. static/theme-init.js applies the
// same rule before the first paint; keep the storage key and the rule in sync
// with it (theme.test.ts checks that both agree).
import { writable } from 'svelte/store';

export const THEME_CHOICES = ['system', 'light', 'dark'] as const;
export type ThemeChoice = (typeof THEME_CHOICES)[number];
export type Theme = 'light' | 'dark';

export const THEME_KEY = 'obh.theme';
const DARK_QUERY = '(prefers-color-scheme: dark)';

/** Anything that is not a known choice (missing, corrupted) means "system". */
export function parseChoice(value: unknown): ThemeChoice {
  return THEME_CHOICES.includes(value as ThemeChoice) ? (value as ThemeChoice) : 'system';
}

export function resolveTheme(choice: ThemeChoice, systemDark: boolean): Theme {
  if (choice === 'system') return systemDark ? 'dark' : 'light';
  return choice;
}

function storedChoice(): ThemeChoice {
  try {
    return parseChoice(localStorage.getItem(THEME_KEY));
  } catch {
    return 'system'; // storage blocked: follow the system
  }
}

/** The user's choice (not the resolved theme), for the switch in Settings. */
export const themeChoice = writable<ThemeChoice>('system');

let lightThemeColor: string | undefined;

function apply(choice: ThemeChoice) {
  const root = document.documentElement;
  const theme = resolveTheme(choice, matchMedia(DARK_QUERY).matches);
  root.dataset.theme = theme;

  // Browser chrome (status bar of the installed app) follows the theme: the
  // brand colour from app.html in light, the page background in dark.
  const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]');
  if (!meta) return;
  lightThemeColor ??= meta.content;
  const bg = getComputedStyle(root).getPropertyValue('--bg').trim();
  meta.content = theme === 'dark' && bg ? bg : lightThemeColor;
}

/** Applies the stored choice and keeps following the system. Returns a cleanup. */
export function initTheme(): () => void {
  let current = storedChoice();
  themeChoice.set(current);
  const unsubscribe = themeChoice.subscribe((choice) => {
    current = choice;
    apply(choice);
  });
  const media = matchMedia(DARK_QUERY);
  const onSystemChange = () => apply(current);
  media.addEventListener('change', onSystemChange);
  return () => {
    unsubscribe();
    media.removeEventListener('change', onSystemChange);
  };
}

export function setTheme(choice: ThemeChoice) {
  try {
    if (choice === 'system') localStorage.removeItem(THEME_KEY);
    else localStorage.setItem(THEME_KEY, choice);
  } catch {
    // storage blocked: the choice still applies until the page is closed
  }
  themeChoice.set(choice);
}
