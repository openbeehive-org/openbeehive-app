import { describe, it, expect, vi, beforeEach } from 'vitest';
import { geocode, reverseGeocode, fmtCoords } from './geocode';

// A fresh mock per test: reusing one spy across resolved and throwing
// implementations trips vitest's settled-result tracking.
let fetchMock: ReturnType<typeof vi.fn<(...a: unknown[]) => unknown>>;
beforeEach(() => {
  fetchMock = vi.fn<(...a: unknown[]) => unknown>();
  vi.stubGlobal('fetch', (...a: unknown[]) => fetchMock(...a));
});

const ok = (body: unknown) => ({ ok: true, json: async () => body });
const BASE = 'https://geo.example.org';

describe('geocode', () => {
  it('found: returns coordinates and label', async () => {
    fetchMock.mockResolvedValue(ok([{ lat: '50.1', lon: '8.6', display_name: 'Frankfurt' }]));
    const res = await geocode('Frankfurt', BASE);
    expect(res).toEqual({ status: 'found', value: { lat: 50.1, lng: 8.6, label: 'Frankfurt' } });
  });

  it('asks the configured service, not a built-in one', async () => {
    fetchMock.mockResolvedValue(ok([]));
    await geocode('Frankfurt am Main', BASE);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      'https://geo.example.org/search?format=jsonv2&limit=1&q=Frankfurt%20am%20Main'
    );
  });

  it('none: service answered with no match', async () => {
    fetchMock.mockResolvedValue(ok([]));
    expect(await geocode('xyzzy nowhere', BASE)).toEqual({ status: 'none' });
  });

  it('unreachable: HTTP error (e.g. rate limited)', async () => {
    fetchMock.mockResolvedValue({ ok: false, json: async () => ({}) });
    expect(await geocode('Berlin', BASE)).toEqual({ status: 'unreachable' });
  });

  it('unreachable: network failure never throws into the UI', async () => {
    fetchMock.mockImplementation(() => {
      throw new TypeError('Failed to fetch');
    });
    expect(await geocode('Berlin', BASE)).toEqual({ status: 'unreachable' });
  });

  it('short queries resolve to none without hitting the network', async () => {
    expect(await geocode('  ab ', BASE)).toEqual({ status: 'none' });
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe('reverseGeocode', () => {
  it('found: returns the display name', async () => {
    fetchMock.mockResolvedValue(ok({ display_name: 'Somewhere 1, Town' }));
    expect(await reverseGeocode(50, 8, BASE)).toEqual({
      status: 'found',
      value: 'Somewhere 1, Town'
    });
  });

  it('asks the configured service', async () => {
    fetchMock.mockResolvedValue(ok({}));
    await reverseGeocode(50.5, 8.25, BASE);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      'https://geo.example.org/reverse?format=jsonv2&lat=50.5&lon=8.25'
    );
  });

  it('none: position without a known address (open water)', async () => {
    fetchMock.mockResolvedValue(ok({ error: 'Unable to geocode' }));
    expect(await reverseGeocode(0, 0, BASE)).toEqual({ status: 'none' });
  });

  it('unreachable: offline', async () => {
    fetchMock.mockImplementation(() => {
      throw new TypeError('Failed to fetch');
    });
    expect(await reverseGeocode(50, 8, BASE)).toEqual({ status: 'unreachable' });
  });
});

describe('fmtCoords', () => {
  it('formats with 5 decimals', () => {
    expect(fmtCoords(50.123456789, 8.1)).toBe('50.12346, 8.10000');
  });
});
