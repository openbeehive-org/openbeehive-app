import { describe, it, expect } from 'vitest';
import { readdirSync } from 'node:fs';
import path from 'node:path';
import { pageTitle, titleKey } from './titles';
import en from './i18n/locales/en.json';

const routesDir = path.resolve(__dirname, '../routes');

// Route ids as SvelteKit derives them: one per directory holding a +page.
function routeIds(dir = routesDir, id = ''): string[] {
  const entries = readdirSync(dir, { withFileTypes: true });
  const own = entries.some((e) => e.isFile() && e.name.startsWith('+page')) ? [id || '/'] : [];
  const nested = entries
    .filter((e) => e.isDirectory())
    .flatMap((e) => routeIds(path.join(dir, e.name), `${id}/${e.name}`));
  return [...own, ...nested];
}

const lookup = (key: string): string | undefined =>
  key.split('.').reduce<any>((node, part) => node?.[part], en);

describe('page titles', () => {
  it('names every route with an existing translation', () => {
    const ids = routeIds();
    expect(ids.length).toBeGreaterThan(5);
    for (const id of ids) {
      const key = titleKey(id);
      expect(key, `route ${id} has no title`).toBeTruthy();
      expect(lookup(key!), `missing translation ${key}`).toEqual(expect.any(String));
    }
  });

  it('appends the app name and falls back to it alone', () => {
    const t = (key: string) => lookup(key) ?? key;
    expect(pageTitle('/(app)/tasks', t)).toBe('Tasks · Openbeehive');
    expect(pageTitle('/unknown', t)).toBe('Openbeehive');
    expect(pageTitle(null, t)).toBe('Openbeehive');
  });
});
