// Moving focus into something that just opened (a form below the button
// that opened it), so keyboard and screen reader users continue there
// instead of on the button.

// Text-like fields that can take focus without opening a picker on phones.
const FIELD =
  'input:not([type=date]):not([type=hidden]):not([type=checkbox]):not([type=radio]), textarea';

/** The element of a freshly opened container that should get focus. */
function focusTarget(node: HTMLElement): HTMLElement {
  const heading = node.querySelector<HTMLElement>('h1, h2, h3, h4');
  if (heading) {
    // Headings are not focusable by default; -1 keeps them out of the tab order.
    if (!heading.hasAttribute('tabindex')) heading.tabIndex = -1;
    return heading;
  }
  const field = node.querySelector<HTMLElement>(FIELD);
  if (field) return field;
  if (!node.hasAttribute('tabindex')) node.tabIndex = -1;
  return node;
}

/**
 * Svelte action for a container that is mounted when it opens: moves focus
 * to its heading (or first text field) and scrolls it into view.
 */
export function focusOnOpen(node: HTMLElement): void {
  const target = focusTarget(node);
  target.focus({ preventScroll: true });
  node.scrollIntoView?.({ block: 'nearest' });
}
