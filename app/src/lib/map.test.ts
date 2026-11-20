import { describe, it, expect } from 'vitest';
import { popupHtml } from './map';

describe('popupHtml', () => {
  it('renders the name as text, never as markup', () => {
    const html = popupHtml('<img src=x onerror="alert(1)">');
    expect(html).toBe('<strong>&lt;img src=x onerror=&quot;alert(1)&quot;&gt;</strong>');
    expect(html).not.toContain('<img');
  });

  it('keeps ordinary names readable', () => {
    expect(popupHtml('Obstwiese & Wald')).toBe('<strong>Obstwiese &amp; Wald</strong>');
  });
});
