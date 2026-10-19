import { defineConfig } from 'vitest/config';
import path from 'node:path';

// Unit tests run in plain node (no browser, no SvelteKit plugin): the local
// data layer is framework-free TypeScript, and the tests back LocalDB with
// node:sqlite so the same SQL runs against a real SQLite.
export default defineConfig({
  resolve: {
    alias: {
      '#lib': path.resolve(__dirname, 'src/lib')
    }
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    setupFiles: ['src/tests/setup.ts'],
    coverage: {
      provider: 'v8',
      // Honest floors just below today's numbers; raise them as tests land.
      thresholds: { statements: 80, branches: 75, functions: 65, lines: 80 }
    }
  }
});
