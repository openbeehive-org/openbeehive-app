// Guards a form's save against double taps: while one save runs, further
// calls are dropped and `busy` lets the form disable its button. `error` is
// the translation key of what went wrong.
import { writable } from 'svelte/store';
import { errorKey } from '#lib/errors.js';

export function createSaver() {
  const busy = writable(false);
  const error = writable('');
  let running = false;

  /** Runs `fn` unless a save is already in flight. True when it completed. */
  async function run(fn: () => Promise<void>): Promise<boolean> {
    if (running) return false;
    running = true;
    busy.set(true);
    error.set('');
    try {
      await fn();
      return true;
    } catch (e) {
      error.set(errorKey(e));
      return false;
    } finally {
      running = false;
      busy.set(false);
    }
  }

  return { busy, error, run };
}
