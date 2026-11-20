// Document titles for every route, in one place. The root layout renders
// "<page> · Openbeehive" from this table; a page that knows something more
// specific (a hive's name) can still set its own <title>.

// Route id -> translation key of the page name.
const TITLE_KEYS: Record<string, string> = {
  '/(app)': 'nav.dashboard',
  '/(app)/apiaries': 'nav.apiaries',
  '/(app)/apiaries/[id]': 'nav.apiaries',
  '/(app)/hives': 'nav.hives',
  '/(app)/hives/[id]': 'nav.hives',
  '/(app)/hives/[id]/stats': 'stats.title',
  '/(app)/hives/[id]/stockkarte': 'insp.stockkarte',
  '/(app)/hives/[id]/visits': 'insp.log',
  '/(app)/scan': 'nav.scan',
  '/(app)/settings': 'nav.settings',
  '/(app)/tasks': 'nav.tasks',
  '/h/[id]': 'nav.hives',
  '/login': 'auth.signin_title'
};

/** Translation key for a route's page name; null for routes without one. */
export function titleKey(routeId: string | null | undefined): string | null {
  return (routeId && TITLE_KEYS[routeId]) || null;
}

/** Full document title for a route. `t` is the translate function. */
export function pageTitle(routeId: string | null | undefined, t: (key: string) => string): string {
  const app = t('app.name');
  const key = titleKey(routeId);
  return key ? `${t(key)} · ${app}` : app;
}
