// Settings is split into groups shown one at a time: /settings?tab=<group>.
// Links to a part of it use its anchor (/settings#sync-problems); the anchor
// selects the group that holds it, so older links keep working.

export const GROUPS = ['preferences', 'workspace', 'data', 'account'] as const;
export type Group = (typeof GROUPS)[number];

// Anchors on the Settings page and the group each one is in.
export const ANCHORS: Record<string, Group> = {
  language: 'preferences',
  appearance: 'preferences',
  units: 'preferences',
  workspaces: 'workspace',
  'api-keys': 'workspace',
  'delete-workspace': 'workspace',
  'sync-problems': 'data',
  'data-export': 'data',
  'treatment-register': 'data',
  account: 'account',
  passkeys: 'account',
  'delete-account': 'account'
};

/** The parts of a URL read here (SvelteKit's page.url is read-only). */
type PageUrl = { hash: string; searchParams: Pick<URLSearchParams, 'get'> };

/** The anchor (without '#') of a Settings URL, or ''. */
export const anchorOf = (url: PageUrl): string => decodeURIComponent(url.hash.replace(/^#/, ''));

/**
 * The group to show for a Settings URL among those `available` (the ones
 * this account has): the anchor's group, else ?tab=, else the first.
 */
export function groupFor(url: PageUrl, available: readonly Group[]): Group {
  const fromAnchor = ANCHORS[anchorOf(url)];
  const fromTab = url.searchParams.get('tab') as Group;
  for (const g of [fromAnchor, fromTab]) if (g && available.includes(g)) return g;
  return available[0] ?? 'preferences';
}

/** Link to a group; the anchor is dropped so the group opens at its top. */
export const groupHref = (g: Group): string => `/settings?tab=${g}`;
