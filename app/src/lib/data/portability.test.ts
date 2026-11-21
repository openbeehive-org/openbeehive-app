import { describe, expect, it, vi } from 'vitest';

vi.mock('../local/db', () => ({ getDB: vi.fn() }));
const { csvCell, toCSV } = await import('./portability');

describe('CSV export', () => {
  it('keeps text that a spreadsheet would run as a formula as text', () => {
    expect(csvCell('=HYPERLINK("http://x","click")')).toBe(
      '"\'=HYPERLINK(""http://x"",""click"")"'
    );
    expect(csvCell('+1 frame')).toBe("'+1 frame");
    expect(csvCell('@SUM(A1)')).toBe("'@SUM(A1)");
    expect(csvCell('\t=1+1')).toBe("'\t=1+1");
  });

  it('writes numbers and ordinary text as they are', () => {
    expect(csvCell(-3.5)).toBe('-3.5');
    expect(csvCell('-12.5')).toBe('-12.5');
    expect(csvCell('calm, busy')).toBe('"calm, busy"');
    expect(csvCell(null)).toBe('');
    expect(toCSV(['a', 'b'], [{ a: '-x', b: 2 }])).toBe("a,b\n'-x,2\n");
  });
});
