import { describe, expect, it } from 'vitest';
import { GROUPS, groupFor, groupHref } from './groups';

const url = (s: string) => new URL(s, 'https://app.test');

describe('settings groups', () => {
  it('opens the group named in the URL', () => {
    expect(groupFor(url('/settings?tab=data'), GROUPS)).toBe('data');
    expect(groupFor(url('/settings'), GROUPS)).toBe('preferences');
    expect(groupFor(url('/settings?tab=bogus'), GROUPS)).toBe('preferences');
  });

  // Deep links from before the split, and from the sync chip, still land on
  // their section.
  it('opens the group that holds an anchor', () => {
    expect(groupFor(url('/settings#sync-problems'), GROUPS)).toBe('data');
    expect(groupFor(url('/settings#delete-account'), GROUPS)).toBe('account');
    expect(groupFor(url('/settings#treatment-register'), GROUPS)).toBe('data');
    expect(groupFor(url('/settings?tab=preferences#sync-problems'), GROUPS)).toBe('data');
  });

  it('falls back to the first group the account has', () => {
    const noLogin = ['preferences', 'data'] as const;
    expect(groupFor(url('/settings?tab=account'), noLogin)).toBe('preferences');
    expect(groupFor(url('/settings#delete-account'), noLogin)).toBe('preferences');
  });

  it('links to a group', () => {
    expect(groupHref('workspace')).toBe('/settings?tab=workspace');
  });
});
