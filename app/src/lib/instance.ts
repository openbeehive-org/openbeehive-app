// What the server says about itself (GET /auth/instance): sign-in methods,
// demo mode and the map configuration. Fetched once per page load and shared
// by the app layout, the login page and the map components.

const API = (import.meta.env?.BEEHIVE_API_URL ?? '').replace(/\/$/, '');

export type InstanceInfo = {
  password_auth?: boolean;
  registration?: boolean;
  email_verification?: boolean;
  oidc_providers?: string[];
  webauthn?: boolean;
  demo?: boolean;
  demo_autologin?: boolean;
  /** How a forgotten password is recovered: by mail, through the instance admin, or not at all. */
  password_reset?: 'email' | 'admin' | 'off';
  map?: { enabled?: boolean; tile_url?: string; geocoder_url?: string };
};

export type MapConfig = {
  /** False: no map is drawn and no tile is requested. */
  enabled: boolean;
  /** Leaflet tile URL template. */
  tileUrl: string;
  /** Base URL of a Nominatim-compatible geocoder; empty turns address lookup off. */
  geocoderUrl: string;
};

// Used when the server does not send a `map` object (servers before 0.5.0).
export const DEFAULT_MAP_CONFIG: MapConfig = {
  enabled: true,
  tileUrl: 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png',
  geocoderUrl: 'https://nominatim.openstreetmap.org'
};

const text = (value: unknown, fallback: string): string =>
  typeof value === 'string' ? value.trim() : fallback;

/**
 * Map settings from an instance response. A missing field falls back to the
 * OpenStreetMap default; an explicitly empty tile URL means "no map" and an
 * empty geocoder URL means "no address lookup".
 */
export function parseMapConfig(instance: unknown): MapConfig {
  const map = (instance as InstanceInfo | null | undefined)?.map;
  if (!map || typeof map !== 'object') return DEFAULT_MAP_CONFIG;
  const tileUrl = text(map.tile_url, DEFAULT_MAP_CONFIG.tileUrl);
  return {
    enabled: map.enabled !== false && tileUrl !== '',
    tileUrl,
    geocoderUrl: text(map.geocoder_url, DEFAULT_MAP_CONFIG.geocoderUrl).replace(/\/+$/, '')
  };
}

let pending: Promise<InstanceInfo | null> | undefined;

/** The instance info, or null when the server cannot be reached. */
export function fetchInstance(): Promise<InstanceInfo | null> {
  pending ??= (async () => {
    try {
      const r = await fetch(`${API}/auth/instance`, { credentials: 'include' });
      return r.ok ? ((await r.json()) as InstanceInfo) : null;
    } catch {
      return null;
    }
  })();
  const current = pending;
  // A failure is not remembered: the next caller retries (back online).
  void current.then((info) => {
    if (!info && pending === current) pending = undefined;
  });
  return current;
}

const MAP_KEY = 'obh.map';

/**
 * The map settings to use right now. The last known answer is kept on the
 * device, so an operator's "maps off" also holds while the app starts offline.
 */
export async function getMapConfig(): Promise<MapConfig> {
  const info = await fetchInstance();
  if (info) {
    const config = parseMapConfig(info);
    try {
      localStorage.setItem(MAP_KEY, JSON.stringify(config));
    } catch {
      // storage blocked: the answer still holds for this page load
    }
    return config;
  }
  try {
    const saved = JSON.parse(localStorage.getItem(MAP_KEY) ?? 'null') as Partial<MapConfig> | null;
    if (saved && typeof saved.tileUrl === 'string' && typeof saved.geocoderUrl === 'string') {
      return {
        enabled: saved.enabled !== false,
        tileUrl: saved.tileUrl,
        geocoderUrl: saved.geocoderUrl
      };
    }
  } catch {
    // unreadable: fall through to the default
  }
  return DEFAULT_MAP_CONFIG;
}
