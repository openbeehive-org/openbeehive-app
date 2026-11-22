import { describe, expect, it } from 'vitest';
import { canAskForLocation } from './geolocation';

const geo = { geolocation: {} as Geolocation };
const policy = (allowed: boolean | 'throws') => ({
  allowsFeature: (feature: string) => {
    if (allowed === 'throws') throw new Error(`unknown feature ${feature}`);
    return feature === 'geolocation' ? allowed : true;
  }
});

describe('canAskForLocation', () => {
  it('needs the geolocation API', () => {
    expect(canAskForLocation(undefined, {})).toBe(false);
    expect(canAskForLocation({} as Pick<Navigator, 'geolocation'>, {})).toBe(false);
  });

  it('asks when the browser says nothing about a policy', () => {
    expect(canAskForLocation(geo, {})).toBe(true);
    expect(canAskForLocation(geo, undefined)).toBe(true);
  });

  it('follows a Permissions-Policy that forbids or allows it', () => {
    expect(canAskForLocation(geo, { permissionsPolicy: policy(false) })).toBe(false);
    expect(canAskForLocation(geo, { permissionsPolicy: policy(true) })).toBe(true);
    expect(canAskForLocation(geo, { featurePolicy: policy(false) })).toBe(false);
  });

  it('lets the request decide when the policy check fails', () => {
    expect(canAskForLocation(geo, { permissionsPolicy: policy('throws') })).toBe(true);
  });
});
