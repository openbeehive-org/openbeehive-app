// See https://svelte.dev/docs/kit/types#app.d.ts
declare global {
  /** Set by vite.config.ts at build time; see src/lib/build.ts. */
  const __OBH_VERSION__: string;
  const __OBH_COMMIT__: string;
  const __OBH_BUILD_TIME__: string;

  namespace App {
    // interface Error {}
    // interface Locals {}
    // interface PageData {}
    // interface PageState {}
    // interface Platform {}
  }
}

export {};
