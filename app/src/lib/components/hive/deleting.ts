// The delete flow shared by all record cards: ask (naming the record),
// delete, refresh the page, then offer to take it back.
import type { Undo } from '#lib/local/history.js';

/** Shows "<text> — Undo" for a few seconds. */
export type OfferUndo = (text: string, undo: Undo) => void;

export async function deleteWithUndo(o: {
  /** Confirmation question naming the record. */
  ask: string;
  /** Message shown next to the undo action afterwards. */
  done: string;
  run: () => Promise<Undo>;
  reload: () => Promise<void> | void;
  offer: OfferUndo;
  confirm?: (question: string) => boolean;
}): Promise<boolean> {
  const ask = o.confirm ?? ((q: string) => window.confirm(q));
  if (!ask(o.ask)) return false;
  const undo = await o.run();
  await o.reload();
  o.offer(o.done, async () => {
    await undo();
    await o.reload();
  });
  return true;
}
