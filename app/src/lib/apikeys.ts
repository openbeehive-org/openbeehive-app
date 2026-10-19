// API keys for scripts and integrations (Settings > API keys). Managed with
// the normal session; the plaintext key is returned once on creation.

export type ApiKeyScope = 'write' | 'read';
export type ApiKeyInfo = {
  id: string;
  name: string;
  prefix: string;
  scope: ApiKeyScope;
  created_at: string;
  expires_at: string | null;
  last_used_at: string | null;
  mine: boolean;
  user_email?: string;
};

// tenant = true lists every key of the active tenant (tenant admins only).
export async function listApiKeys(
  api: string,
  headers: Record<string, string>,
  tenant = false
): Promise<ApiKeyInfo[]> {
  const r = await fetch(`${api}/auth/api-keys${tenant ? '?tenant=1' : ''}`, {
    credentials: 'include',
    headers
  });
  if (!r.ok) return [];
  const j = await r.json().catch(() => ({}));
  return j.keys ?? [];
}

export async function createApiKey(
  api: string,
  name: string,
  scope: ApiKeyScope,
  expiresInDays: number,
  headers: Record<string, string>
): Promise<{ token: string } & ApiKeyInfo> {
  const r = await fetch(`${api}/auth/api-keys`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: JSON.stringify({ name, scope, expires_in_days: expiresInDays })
  });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error ?? `HTTP ${r.status}`);
  return j;
}

export async function deleteApiKey(
  api: string,
  id: string,
  headers: Record<string, string>
): Promise<boolean> {
  const r = await fetch(`${api}/auth/api-keys/delete`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: JSON.stringify({ id })
  });
  return r.ok;
}
