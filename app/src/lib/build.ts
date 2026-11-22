// What this copy of the app is: version, commit and build time, fixed at build
// time by vite.config.ts, plus the project links shown in the footer.
//
// Release images take the commit from the workflow and the build time from
// SOURCE_DATE_EPOCH (the tagged commit's time), so rebuilding the same commit
// gives the same image. Local builds read the commit from git and use the
// current time.

export type BuildInfo = {
  /** The app's version from package.json, e.g. "0.5.0". */
  version: string;
  /** Full commit hash, or '' when the build did not know it. */
  commit: string;
  /** ISO timestamp of the build, or '' when unknown. */
  builtAt: string;
};

// The constants do not exist in unit tests, which run without the Vite build.
export const buildInfo: BuildInfo = {
  version: typeof __OBH_VERSION__ === 'string' ? __OBH_VERSION__ : '',
  commit: typeof __OBH_COMMIT__ === 'string' ? __OBH_COMMIT__ : '',
  builtAt: typeof __OBH_BUILD_TIME__ === 'string' ? __OBH_BUILD_TIME__ : ''
};

const trimSlash = (url: string) => url.replace(/\/+$/, '');

// A fork can point these at its own docs, source and homepage at build time;
// the AGPL asks that users can find the source of the code they run.
export const links = {
  docs: trimSlash(import.meta.env?.BEEHIVE_DOCS_URL || 'https://docs.openbeehive.org'),
  source: trimSlash(
    import.meta.env?.BEEHIVE_SOURCE_URL || 'https://github.com/openbeehive-org/openbeehive-app'
  ),
  homepage: trimSlash(import.meta.env?.BEEHIVE_MARKETING_URL || 'https://openbeehive.org')
};

/** The commit as shown to people: its first seven characters. */
export function shortCommit(commit: string): string {
  return /^[0-9a-f]{7,40}$/i.test(commit) ? commit.slice(0, 7).toLowerCase() : '';
}

/** Where the commit can be looked at, or '' when it is unknown. */
export function commitUrl(source: string, commit: string): string {
  return shortCommit(commit) ? `${trimSlash(source)}/commit/${commit.toLowerCase()}` : '';
}

/** The release notes of a version, or '' for something that is not a version. */
export function releaseUrl(source: string, version: string): string {
  return /^\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)?$/.test(version)
    ? `${trimSlash(source)}/releases/tag/v${version}`
    : '';
}

/** The build time in the UI language, or '' when it is unknown or unreadable. */
export function formatBuildTime(iso: string, locale: string | null | undefined): string {
  if (!iso) return '';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat(locale || undefined, {
    dateStyle: 'medium',
    timeStyle: 'short'
  }).format(date);
}
