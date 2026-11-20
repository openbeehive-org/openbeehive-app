import { describe, it, expect, vi, beforeEach } from 'vitest';
import { DEFAULT_MAP_CONFIG, parseMapConfig } from './instance';

describe('parseMapConfig', () => {
  it('falls back to the OpenStreetMap defaults for a server without map settings', () => {
    expect(parseMapConfig({ password_auth: true })).toEqual(DEFAULT_MAP_CONFIG);
    expect(parseMapConfig(null)).toEqual(DEFAULT_MAP_CONFIG);
    expect(parseMapConfig({ map: 'yes' })).toEqual(DEFAULT_MAP_CONFIG);
  });

  it('reads the configured endpoints', () => {
    expect(
      parseMapConfig({
        map: {
          enabled: true,
          tile_url: 'https://tiles.example.org/{z}/{x}/{y}.png',
          geocoder_url: 'https://geo.example.org/'
        }
      })
    ).toEqual({
      enabled: true,
      tileUrl: 'https://tiles.example.org/{z}/{x}/{y}.png',
      geocoderUrl: 'https://geo.example.org'
    });
  });

  it('fills a missing field with its default', () => {
    expect(parseMapConfig({ map: { enabled: true } })).toEqual(DEFAULT_MAP_CONFIG);
    expect(parseMapConfig({ map: { tile_url: 'https://t.example/{z}/{x}/{y}.png' } })).toEqual({
      ...DEFAULT_MAP_CONFIG,
      tileUrl: 'https://t.example/{z}/{x}/{y}.png'
    });
  });

  it('switches maps off', () => {
    expect(parseMapConfig({ map: { enabled: false } }).enabled).toBe(false);
    // No tile source means there is nothing to draw.
    expect(parseMapConfig({ map: { enabled: true, tile_url: '' } }).enabled).toBe(false);
  });

  it('treats an empty geocoder URL as "no address lookup"', () => {
    const config = parseMapConfig({ map: { enabled: true, geocoder_url: '' } });
    expect(config.enabled).toBe(true);
    expect(config.geocoderUrl).toBe('');
  });
});

describe('getMapConfig', () => {
  const disabled = {
    map: { enabled: false, tile_url: 'https://t/{z}', geocoder_url: 'https://g' }
  };

  beforeEach(() => {
    localStorage.clear();
    vi.resetModules();
  });

  async function load(fetchImpl: () => Promise<unknown>) {
    vi.stubGlobal('fetch', fetchImpl);
    return (await import('./instance')).getMapConfig;
  }

  it('uses the server answer and remembers it for offline starts', async () => {
    let getMapConfig = await load(async () => ({ ok: true, json: async () => disabled }));
    expect((await getMapConfig()).enabled).toBe(false);

    getMapConfig = await load(async () => {
      throw new TypeError('offline');
    });
    expect(await getMapConfig()).toEqual({
      enabled: false,
      tileUrl: 'https://t/{z}',
      geocoderUrl: 'https://g'
    });
  });

  it('uses the defaults when the server was never reached', async () => {
    const getMapConfig = await load(async () => ({ ok: false, json: async () => ({}) }));
    expect(await getMapConfig()).toEqual(DEFAULT_MAP_CONFIG);
  });

  it('asks again after a failure', async () => {
    let online = false;
    const getMapConfig = await load(async () => {
      if (!online) throw new TypeError('offline');
      return { ok: true, json: async () => disabled };
    });
    expect((await getMapConfig()).enabled).toBe(true);
    online = true;
    expect((await getMapConfig()).enabled).toBe(false);
  });
});
