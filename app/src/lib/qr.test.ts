import { describe, expect, it } from 'vitest';
import {
  cameraProblem,
  hiveUrl,
  parseHiveId,
  qrSvg,
  rememberScanTarget,
  shortCode,
  takeScanTarget
} from './qr';

const ID = '3f9a1c22-7b1e-4c55-9d3a-0123456789ab';

describe('parseHiveId', () => {
  it('reads the label link on any host', () => {
    expect(parseHiveId(`https://app.openbeehive.org/h/${ID}`)).toBe(ID);
    expect(parseHiveId(`http://192.168.1.20:8080/h/${ID}`)).toBe(ID);
    expect(parseHiveId(`https://bees.example.org/beehive/h/${ID}/`)).toBe(ID);
    expect(parseHiveId(`  https://app.openbeehive.org/h/${ID}?utm=1#top \n`)).toBe(ID);
    expect(parseHiveId('https://app.openbeehive.org/h/demo-hive_1')).toBe('demo-hive_1');
  });

  it('reads what hiveUrl prints, also as a bare path', () => {
    expect(parseHiveId(hiveUrl(ID))).toBe(ID);
    expect(parseHiveId(`/h/${ID}`)).toBe(ID);
  });

  it('reads the custom scheme and a bare id', () => {
    expect(parseHiveId(`openbeehive://hive/${ID}`)).toBe(ID);
    expect(parseHiveId(`OPENBEEHIVE://hive/${ID}`)).toBe(ID);
    expect(parseHiveId(ID)).toBe(ID);
    expect(parseHiveId(ID.toUpperCase())).toBe(ID.toUpperCase());
  });

  it('rejects everything that is not a hive label', () => {
    for (const other of [
      '',
      'hello',
      'https://example.org/',
      'https://example.org/shop/item/42',
      `https://example.org/h/${ID}/edit`,
      'https://example.org/h/',
      `https://example.org/?next=/h/${ID}`,
      'https://example.org/h/<script>',
      'WIFI:S:Apiary;T:WPA;P:secret;;',
      `javascript:alert(1)//h/${ID}`,
      'http://[broken',
      '12345678'
    ]) {
      expect(parseHiveId(other), other).toBeNull();
    }
  });
});

describe('labels', () => {
  it('prints the first six characters of the id as the short code', () => {
    expect(shortCode(ID)).toBe('3F9A1C');
  });

  it('renders a QR code as SVG without any script', async () => {
    const svg = await qrSvg(hiveUrl(ID), 200);
    expect(svg.startsWith('<svg')).toBe(true);
    expect(svg).not.toMatch(/<script|onload=|href=/i);
  });
});

describe('scan target kept across sign-in', () => {
  it('is returned once and then forgotten', () => {
    const store = new Map<string, string>();
    (globalThis as any).sessionStorage = {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k)
    };
    expect(takeScanTarget()).toBeNull();
    rememberScanTarget(ID);
    expect(takeScanTarget()).toBe(ID);
    expect(takeScanTarget()).toBeNull();
    delete (globalThis as any).sessionStorage;
  });

  it('does not throw when storage is unavailable', () => {
    expect(() => rememberScanTarget(ID)).not.toThrow();
    expect(takeScanTarget()).toBeNull();
  });
});

describe('cameraProblem', () => {
  const named = (name: string) => Object.assign(new Error(name), { name });

  it('tells a refused permission from a missing camera from anything else', () => {
    expect(cameraProblem(named('NotAllowedError'))).toBe('denied');
    expect(cameraProblem(named('SecurityError'))).toBe('denied');
    expect(cameraProblem(named('NotFoundError'))).toBe('nocamera');
    expect(cameraProblem(named('OverconstrainedError'))).toBe('nocamera');
    expect(cameraProblem(named('NotReadableError'))).toBe('failed');
    expect(cameraProblem(null)).toBe('failed');
    expect(cameraProblem('boom')).toBe('failed');
  });
});
