// Serializes everything that reads a row and then writes it back: local
// writes (repo.ts) and remote changes (sync.ts) both read the field clock,
// merge and store it. The database worker handles one message at a time,
// but such a read-then-write spans several messages, and two of them
// interleaving would lose one side's field stamps.
let tail: Promise<unknown> = Promise.resolve();

// Runs fn after every earlier exclusive() call has settled.
export function exclusive<T>(fn: () => Promise<T>): Promise<T> {
  const run = tail.then(fn, fn);
  tail = run.catch(() => undefined);
  return run;
}
