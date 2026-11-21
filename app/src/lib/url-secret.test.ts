import { describe, expect, it, vi } from 'vitest';

vi.mock('$app/navigation', () => ({ replaceState: vi.fn() }));
const { withoutParam } = await import('./url-secret');

describe('withoutParam', () => {
  it('drops only the secret, keeping the rest of the address', () => {
    expect(withoutParam('https://x.test/login?invite=abc&reauth=1#top', 'invite')).toBe(
      '/login?reauth=1#top'
    );
    expect(withoutParam('https://x.test/reset-password?token=abc', 'token')).toBe(
      '/reset-password'
    );
  });

  it('leaves an address without the secret alone', () => {
    expect(withoutParam('https://x.test/login?reauth=1', 'invite')).toBeNull();
  });
});
