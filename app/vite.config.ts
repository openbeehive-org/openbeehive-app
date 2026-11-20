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

export default defineConfig({
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
  server: { port: 5173 },
  // Don't pre-bundle sqlite-wasm: the optimizer rewrites its path so the
  // sibling sqlite3.wasm can no longer be fetched (404). Excluding it keeps
  // the wasm resolvable relative to the package.
  optimizeDeps: { exclude: ['@sqlite.org/sqlite-wasm'] },
  worker: { format: 'es' }
});
