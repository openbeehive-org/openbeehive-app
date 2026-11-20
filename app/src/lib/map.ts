// Leaflet set-up shared by the apiary overview map and the location picker.
// Leaflet is loaded on demand: the bundle and the tile requests only happen
// when a map is actually shown.
import type * as Leaflet from 'leaflet';
import { escapeHtml } from './html';
import type { MapConfig } from './instance';

export type LeafletLib = typeof import('leaflet');

/**
 * Popup content for an apiary pin. Leaflet inserts popup strings as HTML and
 * names arrive through sync and imports, so the name is escaped.
 */
export const popupHtml = (name: string): string => `<strong>${escapeHtml(name)}</strong>`;

/** Teardrop pin drawn with CSS (.obh-pin), so no marker image has to be bundled. */
export function pinIcon(L: LeafletLib): Leaflet.DivIcon {
  return L.divIcon({
    className: 'obh-pin',
    html: '<span></span>',
    iconSize: [26, 26],
    iconAnchor: [13, 26]
  });
}

export type MapOptions = {
  center: Leaflet.LatLngTuple;
  zoom: number;
  /** Translated labels of the zoom buttons. */
  zoomIn: string;
  zoomOut: string;
};

export async function createMap(
  el: HTMLElement,
  config: MapConfig,
  options: MapOptions
): Promise<{ L: LeafletLib; map: Leaflet.Map }> {
  const L = (await import('leaflet')).default;
  const map = L.map(el, { zoomControl: false }).setView(options.center, options.zoom);
  L.control.zoom({ zoomInTitle: options.zoomIn, zoomOutTitle: options.zoomOut }).addTo(map);
  L.tileLayer(config.tileUrl, {
    maxZoom: 19,
    attribution:
      '&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap</a>'
  }).addTo(map);
  // The container often gets its final size a moment after mounting.
  setTimeout(() => map.invalidateSize(), 60);
  return { L, map };
}
