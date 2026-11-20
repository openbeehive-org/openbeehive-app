// Helpers for the Leaflet maps.
import { escapeHtml } from './html';

/**
 * Popup content for an apiary pin. Leaflet inserts popup strings as HTML and
 * names arrive through sync and imports, so the name is escaped.
 */
export const popupHtml = (name: string): string => `<strong>${escapeHtml(name)}</strong>`;
