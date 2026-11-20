// Escaping for the few places that build HTML as a string (print windows,
// map popups). Everything a user typed, or that arrived through sync or an
// import, goes through escapeHtml before it is concatenated into markup.

const ENTITIES: Record<string, string> = {
  '&': '&amp;',
  '<': '&lt;',
  '>': '&gt;',
  '"': '&quot;',
  "'": '&#39;'
};

/** Escapes text for use in HTML element content and quoted attribute values. */
export function escapeHtml(value: unknown): string {
  return String(value ?? '').replace(/[&<>"']/g, (c) => ENTITIES[c] ?? c);
}
