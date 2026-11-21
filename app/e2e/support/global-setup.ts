// Fails early, with the command to run, when the binary under test is missing
// or its app was built to talk to another server.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { BINARY, WEB_DIST } from './server';

const appDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

// The env files Vite reads for a production build.
const ENV_FILES = ['.env', '.env.local', '.env.production', '.env.production.local'];

/** The non-empty BEEHIVE_API_URL values set in the app's env files. */
export function configuredApiUrls(dir: string): string[] {
  const urls: string[] = [];
  for (const name of ENV_FILES) {
    const file = path.join(dir, name);
    if (!fs.existsSync(file)) continue;
    for (const line of fs.readFileSync(file, 'utf8').split('\n')) {
      const m = /^\s*(?:export\s+)?BEEHIVE_API_URL\s*=\s*(.*)$/.exec(line);
      const value = (m?.[1] ?? '')
        .trim()
        .replace(/^(['"])(.*)\1$/, '$2')
        .replace(/\/$/, '');
      if (value) urls.push(value);
    }
  }
  return urls;
}

/** Files of the build (recursively) whose text contains one of the URLs. */
export function buildFilesNaming(dist: string, urls: string[]): string[] {
  if (urls.length === 0) return [];
  const found: string[] = [];
  for (const entry of fs.readdirSync(dist, { recursive: true, withFileTypes: true })) {
    if (!entry.isFile() || !/\.(js|html)$/.test(entry.name)) continue;
    const file = path.join(entry.parentPath, entry.name);
    const text = fs.readFileSync(file, 'utf8');
    if (urls.some((u) => text.includes(u))) found.push(file);
  }
  return found;
}

export default function globalSetup() {
  for (const file of [BINARY, path.join(WEB_DIST, 'index.html')]) {
    if (!fs.existsSync(file)) {
      throw new Error(
        `${file} is missing: run \`make e2e\` (it builds the app and the binary first)`
      );
    }
  }
  // The tests talk to the server that serves the app. An app built with a
  // BEEHIVE_API_URL from app/.env calls another server instead, which the
  // Content-Security-Policy blocks, and every test fails.
  // `make e2e` builds with OBH_IGNORE_DOTENV, which makes Vite skip app/.env,
  // so the build is known to be clean. Otherwise look for the configured URL
  // in the build (this can also match the code's own non-browser fallback,
  // http://localhost:8080; the advice to run `make e2e` is right either way).
  const urls = process.env.OBH_IGNORE_DOTENV ? [] : configuredApiUrls(appDir);
  const named = buildFilesNaming(WEB_DIST, urls);
  if (named.length > 0) {
    throw new Error(
      `The app under test was built with BEEHIVE_API_URL=${urls.join(', ')} (from app/.env) ` +
        `and would call that server (${path.relative(appDir, named[0] ?? '')}). ` +
        'Run `make e2e`, which builds it without that setting.'
    );
  }
}
