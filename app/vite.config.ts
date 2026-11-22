import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig, type Plugin } from 'vite';

// `virtual:obh-build-files` gives the service worker the list of every file
// under _app/immutable of the finished client build. SvelteKit's own list
// ($app/manifest `immutable`) comes from Vite's manifest, which leaves out
// what a web worker emits: the SQLite WASM binary and its helper scripts. The
// service worker is built after the client, so the files are on disk by then.
function buildFiles(): Plugin {
  const id = 'virtual:obh-build-files';
  const resolved = '\0' + id;
  let isBuild = false;
  return {
    name: 'obh-build-files',
    configResolved(config) {
      isBuild = config.command === 'build';
    },
    resolveId(source) {
      return source === id ? resolved : null;
    },
    load(source) {
      if (source !== resolved) return null;
      let files: string[] = [];
      if (isBuild) {
        const outDir = path.resolve(
          this.environment.config.root,
          this.environment.config.build.outDir
        );
        const dir = path.join(outDir, '_app/immutable');
        if (!fs.existsSync(dir)) {
          this.error(`${dir} is missing: the service worker must be built after the client`);
        }
        files = fs
          .readdirSync(dir, { recursive: true, withFileTypes: true })
          .filter((entry) => entry.isFile())
          .map((entry) => path.relative(outDir, path.join(entry.parentPath, entry.name)))
          .map((file) => file.split(path.sep).join('/'))
          .sort();
      }
      return `export const files = ${JSON.stringify(files)};`;
    }
  };
}

// Version, commit and build time for the footer (src/lib/build.ts). The release
// workflow passes BUILD_COMMIT and SOURCE_DATE_EPOCH (the commit's time, which
// keeps images reproducible); local builds ask git and use the current time.
function buildInfo() {
  const pkg = JSON.parse(fs.readFileSync(new URL('./package.json', import.meta.url), 'utf8'));
  let commit = process.env.BUILD_COMMIT ?? '';
  if (!commit) {
    try {
      commit = execFileSync('git', ['rev-parse', 'HEAD'], {
        encoding: 'utf8',
        stdio: 'pipe'
      }).trim();
    } catch {
      commit = ''; // no git (e.g. a Docker build without BUILD_COMMIT)
    }
  }
  const epoch = Number(process.env.SOURCE_DATE_EPOCH);
  const builtAt = (
    Number.isFinite(epoch) && epoch > 0 ? new Date(epoch * 1000) : new Date()
  ).toISOString();
  return {
    __OBH_VERSION__: JSON.stringify(String(pkg.version ?? '')),
    __OBH_COMMIT__: JSON.stringify(commit),
    __OBH_BUILD_TIME__: JSON.stringify(builtAt)
  };
}

export default defineConfig({
  define: buildInfo(),
  plugins: [
    sveltekit({
      preprocess: vitePreprocess(),
      adapter: adapter({
        fallback: 'index.html',
        pages: 'build',
        assets: 'build',
        strict: false
      })
    }),
    buildFiles()
  ],
  // Client-exposed env vars are namespaced BEEHIVE_ (VITE_ kept for compatibility).
  envPrefix: ['BEEHIVE_', 'VITE_'],
  // Builds for the browser tests must not pick up a developer's app/.env
  // (Vite ignores an empty BEEHIVE_API_URL in the environment and falls back
  // to the file), so they read env files from a directory that has none.
  envDir: process.env.OBH_IGNORE_DOTENV ? 'e2e/support' : '.',
  server: { port: 5173 },
  // Don't pre-bundle sqlite-wasm: the optimizer rewrites its path so the
  // sibling sqlite3.wasm can no longer be fetched (404). Excluding it keeps
  // the wasm resolvable relative to the package.
  optimizeDeps: { exclude: ['@sqlite.org/sqlite-wasm'] },
  worker: { format: 'es' }
});
