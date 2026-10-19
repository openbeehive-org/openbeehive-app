// Forward + reverse geocoding via OSM Nominatim. Usage stays within the
// public instance's fair-use policy: requests are user-triggered (typing an
// address / dropping a pin), debounced by the caller, and identified by the
// browser's Referer.
//
// Results carry an explicit status so the UI can tell the user what happened:
// 'found': a match; 'none': the service answered but knows no match;
// 'unreachable': network/service failure (offline, blocked, rate-limited).
export type GeoHit = { lat: number; lng: number; label: string };
export type GeoResult<T> =
  { status: 'found'; value: T } | { status: 'none' } | { status: 'unreachable' };

const BASE = 'https://nominatim.openstreetmap.org';

async function ask(path: string): Promise<GeoResult<any>> {
  try {
    const r = await fetch(`${BASE}${path}`, {
      headers: { Accept: 'application/json', 'Accept-Language': navigator.language ?? 'en' }
    });
    if (!r.ok) return { status: 'unreachable' };
    return { status: 'found', value: await r.json() };
  } catch {
    return { status: 'unreachable' };
  }
}

/** Best match for a free-form address. */
export async function geocode(query: string): Promise<GeoResult<GeoHit>> {
  const q = query.trim();
  if (q.length < 3) return { status: 'none' };
  const res = await ask(`/search?format=jsonv2&limit=1&q=${encodeURIComponent(q)}`);
  if (res.status !== 'found') return res;
  const hit = Array.isArray(res.value) ? res.value[0] : null;
  if (!hit) return { status: 'none' };
  return {
    status: 'found',
    value: { lat: parseFloat(hit.lat), lng: parseFloat(hit.lon), label: hit.display_name ?? q }
  };
}

/** Human-readable address for a position ('none' means the caller shows coordinates). */
export async function reverseGeocode(lat: number, lng: number): Promise<GeoResult<string>> {
  const res = await ask(`/reverse?format=jsonv2&lat=${lat}&lon=${lng}`);
  if (res.status !== 'found') return res;
  const label = res.value?.display_name;
  return label ? { status: 'found', value: label } : { status: 'none' };
}

export const fmtCoords = (lat: number, lng: number): string =>
  `${lat.toFixed(5)}, ${lng.toFixed(5)}`;
