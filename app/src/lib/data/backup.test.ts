import { describe, it, expect, vi } from 'vitest';

vi.mock('../local/sync', () => ({ firstSyncDone: vi.fn(async () => true) }));

import { fetchServerExport, ServerExportError } from './backup';

const answer = (status: number, body: BodyInit, headers: Record<string, string> = {}) =>
  vi.fn(async () => new Response(body, { status, headers }));

describe('fetchServerExport', () => {
  it('downloads with the session and takes the file name from the server', async () => {
    localStorage.setItem('session', 'tok');
    const send = answer(200, 'PK..', {
      'Content-Disposition': 'attachment; filename="openbeehive-server-2026-11-20.zip"'
    });
    const got = await fetchServerExport(true, send as unknown as typeof fetch);
    expect(got.filename).toBe('openbeehive-server-2026-11-20.zip');
    expect(await got.blob.text()).toBe('PK..');
    const [url, init] = send.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toMatch(/\/export\?photos=1$/);
    expect(init.headers).toEqual({ Authorization: 'Bearer tok' });
  });

  it('reports the error code the server answers with, or a missing network', async () => {
    const limited = answer(429, JSON.stringify({ code: 'rate_limited' }));
    await expect(
      fetchServerExport(false, limited as unknown as typeof fetch)
    ).rejects.toMatchObject({
      status: 429,
      code: 'rate_limited'
    });
    const offline = vi.fn(async () => {
      throw new TypeError('Failed to fetch');
    });
    const err = await fetchServerExport(false, offline as unknown as typeof fetch).catch((e) => e);
    expect(err).toBeInstanceOf(ServerExportError);
    expect(err.code).toBe('network');
  });
});
