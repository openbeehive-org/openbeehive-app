import { describe, it, expect } from 'vitest';
import { httpTransport } from './http';

type Call = { url: string; init: RequestInit };

function server(answer: (call: Call) => Response) {
  const calls: Call[] = [];
  const fetch = (async (url: RequestInfo | URL, init: RequestInit = {}) => {
    const call = { url: String(url), init };
    calls.push(call);
    return answer(call);
  }) as typeof globalThis.fetch;
  return { calls, fetch };
}

const json = (status: number, code: string) =>
  new Response(JSON.stringify({ error: 'x', code }), {
    status,
    headers: { 'Content-Type': 'application/json' }
  });
const image = new Blob(['jpeg'], { type: 'image/jpeg' });

describe('upload', () => {
  it('posts the image to the photo, with the session', async () => {
    const s = server(() => json(201, ''));
    const t = httpTransport({
      baseUrl: 'https://bees.example',
      token: () => 'tok',
      fetch: s.fetch
    });
    expect(await t.upload('a b/c', image)).toBe('stored');
    expect(s.calls).toHaveLength(1);
    expect(s.calls[0]!.url).toBe('https://bees.example/photos/a%20b%2Fc');
    expect(s.calls[0]!.init.method).toBe('POST');
    expect(s.calls[0]!.init.body).toBe(image);
    expect(s.calls[0]!.init.headers).toEqual({
      'Content-Type': 'image/jpeg',
      Authorization: 'Bearer tok'
    });
  });

  it('sends no Authorization header without a session (single-user server)', async () => {
    const s = server(() => json(200, ''));
    const t = httpTransport({ baseUrl: '', token: () => null, fetch: s.fetch });
    expect(await t.upload('p1', image)).toBe('stored');
    expect(s.calls[0]!.init.headers).toEqual({ 'Content-Type': 'image/jpeg' });
  });

  it.each([
    [200, '', 'stored'],
    [201, '', 'stored'],
    [401, 'not_signed_in', 'signed_out'],
    [403, 'read_only', 'read_only'],
    [403, 'demo_read_only', 'read_only'],
    [403, 'cross_site_request', 'retry'],
    [404, 'unknown_photo', 'unknown'],
    [410, 'deleted', 'deleted'],
    [400, 'invalid_body', 'refused'],
    [413, 'too_large', 'refused'],
    [415, 'unsupported_type', 'refused'],
    [500, 'internal', 'retry'],
    [503, 'unavailable', 'retry']
  ])('reads %i %s as "%s"', async (status, code, want) => {
    const s = server(() => json(status, code));
    const t = httpTransport({ baseUrl: '', token: () => 'tok', fetch: s.fetch });
    expect(await t.upload('p1', image)).toBe(want);
  });

  it('copes with an error body that is not JSON', async () => {
    const s = server(() => new Response('<html>Bad gateway</html>', { status: 403 }));
    const t = httpTransport({ baseUrl: '', token: () => 'tok', fetch: s.fetch });
    expect(await t.upload('p1', image)).toBe('retry');
  });
});

describe('download', () => {
  it('returns the image', async () => {
    const s = server(() => new Response(image, { status: 200 }));
    const t = httpTransport({
      baseUrl: 'https://bees.example',
      token: () => 'tok',
      fetch: s.fetch
    });
    const res = await t.download('p1');
    expect(typeof res === 'object' && (await res.blob.text())).toBe('jpeg');
    expect(s.calls[0]!.url).toBe('https://bees.example/photos/p1');
    expect(s.calls[0]!.init.method).toBeUndefined();
    expect(s.calls[0]!.init.headers).toEqual({ Authorization: 'Bearer tok' });
  });

  it.each([
    [404, 'missing'],
    [410, 'missing'],
    [401, 'signed_out'],
    [500, 'retry'],
    [503, 'retry']
  ])('reads %i as "%s"', async (status, want) => {
    const s = server(() => json(status, 'x'));
    const t = httpTransport({ baseUrl: '', token: () => null, fetch: s.fetch });
    expect(await t.download('p1')).toBe(want);
  });
});
