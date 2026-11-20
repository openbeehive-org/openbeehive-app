// The server side of photo transfers: POST and GET /photos/<id>, with the
// session the rest of the app uses. Answers are reduced to what the
// transfer logic acts on.
import type { DownloadResult, Transport, UploadResult } from './transfer';

export interface HttpOptions {
  /** Origin of the server, without a trailing slash. */
  baseUrl: string;
  /** The session token, or null when the server needs none. */
  token: () => string | null;
  fetch?: typeof fetch;
}

async function errorCode(res: Response): Promise<string> {
  try {
    return String((await res.json())?.code ?? '');
  } catch {
    return '';
  }
}

export function httpTransport(opts: HttpOptions): Transport {
  const send = opts.fetch ?? ((...args: Parameters<typeof fetch>) => fetch(...args));
  const url = (id: string) => `${opts.baseUrl}/photos/${encodeURIComponent(id)}`;
  const headers = (extra: Record<string, string> = {}) => {
    const token = opts.token();
    return token ? { ...extra, Authorization: `Bearer ${token}` } : extra;
  };

  return {
    async upload(id, image): Promise<UploadResult> {
      const res = await send(url(id), {
        method: 'POST',
        headers: headers({ 'Content-Type': image.type || 'application/octet-stream' }),
        body: image
      });
      if (res.ok) return 'stored';
      switch (res.status) {
        case 401:
          return 'signed_out';
        case 403: {
          const code = await errorCode(res);
          return code === 'read_only' || code === 'demo_read_only' ? 'read_only' : 'retry';
        }
        case 404:
          return 'unknown';
        case 410:
          return 'deleted';
        case 400:
        case 413:
        case 415:
          return 'refused';
      }
      return 'retry';
    },

    async download(id): Promise<DownloadResult> {
      const res = await send(url(id), { headers: headers() });
      if (res.ok) return { blob: await res.blob() };
      if (res.status === 404 || res.status === 410) return 'missing';
      if (res.status === 401) return 'signed_out';
      return 'retry';
    }
  };
}
