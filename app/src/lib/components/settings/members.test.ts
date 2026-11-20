import { describe, expect, it } from 'vitest';
import { inviteExpired, memberLabel, namesMatch, type OpenInvite } from './members';

describe('memberLabel', () => {
  const member = { user_id: 'u1', me: false };

  it('prefers the name, then the address', () => {
    expect(memberLabel({ ...member, name: 'Ada', email: 'ada@example.org' }, 'Member')).toBe('Ada');
    expect(memberLabel({ ...member, name: '  ', email: 'ada@example.org' }, 'Member')).toBe(
      'ada@example.org'
    );
  });

  it('falls back to a stand-in for a member who shows neither', () => {
    // What a non-admin sees of a member who never entered a name.
    expect(memberLabel({ ...member, name: '' }, 'Member without a name')).toBe(
      'Member without a name'
    );
  });
});

describe('inviteExpired', () => {
  const now = new Date('2026-12-01T12:00:00Z');
  const invite = (over: Partial<OpenInvite>): OpenInvite => ({
    id: 'i1',
    email: 'a@example.org',
    role: 'member',
    created_at: '2026-11-25T12:00:00Z',
    link: 'https://bees.example.org/login?invite=t',
    ...over
  });

  it('takes the server at its word', () => {
    expect(inviteExpired(invite({ expired: true, expires_at: '2027-01-01T00:00:00Z' }), now)).toBe(
      true
    );
  });

  it('compares the expiry with the clock', () => {
    expect(inviteExpired(invite({ expires_at: '2026-12-02T12:00:00Z' }), now)).toBe(false);
    expect(inviteExpired(invite({ expires_at: '2026-12-01T12:00:00Z' }), now)).toBe(true);
    expect(inviteExpired(invite({ expires_at: '2026-11-30T12:00:00Z' }), now)).toBe(true);
  });

  it('treats an invite without an expiry as open', () => {
    expect(inviteExpired(invite({}), now)).toBe(false);
    expect(inviteExpired(invite({ expires_at: 'not a date' }), now)).toBe(false);
  });
});

describe('namesMatch', () => {
  it('needs the exact workspace name', () => {
    expect(namesMatch('Beekeeping Club', 'Beekeeping Club')).toBe(true);
    expect(namesMatch('  Beekeeping Club ', 'Beekeeping Club')).toBe(true);
    expect(namesMatch('beekeeping club', 'Beekeeping Club')).toBe(false);
    expect(namesMatch('Beekeeping', 'Beekeeping Club')).toBe(false);
    expect(namesMatch('', 'Beekeeping Club')).toBe(false);
  });

  it('never matches a workspace without a name', () => {
    expect(namesMatch('', '')).toBe(false);
    expect(namesMatch(' ', '  ')).toBe(false);
  });
});
