// Node-side stand-ins for the browser APIs the local data layer touches.
// Kept minimal on purpose: only what repo/sync/hlc actually use.

class MemoryStorage {
  private m = new Map<string, string>();
  getItem(k: string) {
    return this.m.has(k) ? this.m.get(k)! : null;
  }
  setItem(k: string, v: string) {
    this.m.set(k, String(v));
  }
  removeItem(k: string) {
    this.m.delete(k);
  }
  clear() {
    this.m.clear();
  }
}

(globalThis as any).localStorage = new MemoryStorage();

// sync.ts checks navigator.onLine before doing anything.
if (!('navigator' in globalThis)) (globalThis as any).navigator = {};
try {
  Object.defineProperty(globalThis.navigator, 'onLine', { value: true, configurable: true });
  Object.defineProperty(globalThis.navigator, 'language', { value: 'en', configurable: true });
} catch {
  // node exposes navigator as a getter-only global in some versions; the
  // spread-copy below keeps the fields the code reads.
  (globalThis as any).navigator = { ...globalThis.navigator, onLine: true, language: 'en' };
}
