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

describe('geocode', () => {
  it('found: returns coordinates and label', async () => {
    fetchMock.mockResolvedValue(ok([{ lat: '50.1', lon: '8.6', display_name: 'Frankfurt' }]));
    const res = await geocode('Frankfurt');
    expect(res).toEqual({ status: 'found', value: { lat: 50.1, lng: 8.6, label: 'Frankfurt' } });
  });

  it('none: service answered with no match', async () => {
    fetchMock.mockResolvedValue(ok([]));
    expect(await geocode('xyzzy nowhere')).toEqual({ status: 'none' });
  });

  it('unreachable: HTTP error (e.g. rate limited)', async () => {
    fetchMock.mockResolvedValue({ ok: false, json: async () => ({}) });
    expect(await geocode('Berlin')).toEqual({ status: 'unreachable' });
  });

  it('unreachable: network failure never throws into the UI', async () => {
    fetchMock.mockImplementation(() => {
      throw new TypeError('Failed to fetch');
    });
    expect(await geocode('Berlin')).toEqual({ status: 'unreachable' });
  });

  it('short queries resolve to none without hitting the network', async () => {
    expect(await geocode('  ab ')).toEqual({ status: 'none' });
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe('reverseGeocode', () => {
  it('found: returns the display name', async () => {
    fetchMock.mockResolvedValue(ok({ display_name: 'Somewhere 1, Town' }));
    expect(await reverseGeocode(50, 8)).toEqual({ status: 'found', value: 'Somewhere 1, Town' });
  });

  it('none: position without a known address (open water)', async () => {
    fetchMock.mockResolvedValue(ok({ error: 'Unable to geocode' }));
    expect(await reverseGeocode(0, 0)).toEqual({ status: 'none' });
  });

  it('unreachable: offline', async () => {
    fetchMock.mockImplementation(() => {
      throw new TypeError('Failed to fetch');
    });
    expect(await reverseGeocode(50, 8)).toEqual({ status: 'unreachable' });
  });
});

describe('fmtCoords', () => {
  it('formats with 5 decimals', () => {
    expect(fmtCoords(50.123456789, 8.1)).toBe('50.12346, 8.10000');
  });
});
