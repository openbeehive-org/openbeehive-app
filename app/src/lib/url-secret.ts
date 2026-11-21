// One-time secrets that arrive in a link (a password reset or invite token)
// are read by the page and then taken out of the address bar, so they do not
// stay in the browser history or show on a shared screen. The page keeps the
// value it read: SvelteKit's `page.url` does not change with a shallow
// navigation.
import { goto } from '$app/navigation';

/** The address without the parameter `name`, or null when it has none. */
export function withoutParam(href: string, name: string): string | null {
  const url = new URL(href);
  if (!url.searchParams.has(name)) return null;
  url.searchParams.delete(name);
  return url.pathname + url.search + url.hash;
}

/** Removes the parameter `name` from the address bar. Call it from onMount. */
export function hideUrlSecret(name: string): void {
  const next = withoutParam(location.href, name);
  if (next !== null) void goto(next, { state: {}, shallow: true, replace: true });
}
