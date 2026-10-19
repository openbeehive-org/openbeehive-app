import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

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
    })
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
