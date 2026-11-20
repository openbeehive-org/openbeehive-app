import { describe, expect, it } from 'vitest';
import { escapeHtml } from './html';

describe('escapeHtml', () => {
  it('neutralises markup and attribute breakouts', () => {
    expect(escapeHtml(`<img src=x onerror="alert('x')">`)).toBe(
      '&lt;img src=x onerror=&quot;alert(&#39;x&#39;)&quot;&gt;'
    );
    expect(escapeHtml('Orchard & Meadow')).toBe('Orchard &amp; Meadow');
  });

  it('turns missing values into an empty string', () => {
    expect(escapeHtml(null)).toBe('');
    expect(escapeHtml(undefined)).toBe('');
    expect(escapeHtml(42)).toBe('42');
  });
});
