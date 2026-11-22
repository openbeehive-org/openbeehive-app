import { describe, expect, it } from 'vitest';
import { buildInfo, commitUrl, formatBuildTime, links, releaseUrl, shortCommit } from './build';

const SOURCE = 'https://github.com/openbeehive-org/openbeehive-app';
const SHA = '42C8AE7B2ECAA073843B3191D944E715D2179CF3';

describe('build information', () => {
  it('is empty outside a Vite build', () => {
    expect(buildInfo).toEqual({ version: '', commit: '', builtAt: '' });
  });

  it('links to the official project by default', () => {
    expect(links).toEqual({
      docs: 'https://docs.openbeehive.org',
      source: SOURCE,
      homepage: 'https://openbeehive.org'
    });
  });

  it('shortens a commit and links to it', () => {
    expect(shortCommit(SHA)).toBe('42c8ae7');
    expect(commitUrl(`${SOURCE}/`, SHA)).toBe(`${SOURCE}/commit/${SHA.toLowerCase()}`);
  });

  it('leaves out a commit it cannot trust', () => {
    for (const bad of ['', 'unknown', '42c8ae', '42c8ae7-dirty', 'g2c8ae7']) {
      expect(shortCommit(bad)).toBe('');
      expect(commitUrl(SOURCE, bad)).toBe('');
    }
  });

  it('links a version to its release notes', () => {
    expect(releaseUrl(SOURCE, '0.5.0')).toBe(`${SOURCE}/releases/tag/v0.5.0`);
    expect(releaseUrl(SOURCE, '1.0.0-rc.1')).toBe(`${SOURCE}/releases/tag/v1.0.0-rc.1`);
    expect(releaseUrl(SOURCE, '')).toBe('');
    expect(releaseUrl(SOURCE, 'next')).toBe('');
  });

  it('formats the build time in the UI language', () => {
    const iso = '2026-11-21T19:15:00.000Z';
    const de = formatBuildTime(iso, 'de');
    expect(de).toMatch(/2026/);
    expect(de).not.toBe(formatBuildTime(iso, 'en'));
    expect(formatBuildTime('', 'en')).toBe('');
    expect(formatBuildTime('not a date', 'en')).toBe('');
  });
});
