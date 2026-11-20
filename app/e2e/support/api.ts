// The server's own HTTP and RPC API, used to prepare accounts, workspaces
// and records where the user interface is not what a test is about.

export type Session = { token: string; userId: string; orgId: string };

export type Account = Session & { email: string; password: string };

type Json = Record<string, unknown>;

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string
  ) {
    super(message);
  }
}

let counter = 0;
/** A name no other test uses: workers share one server and its accounts. */
export function unique(prefix: string): string {
  counter += 1;
  return `${prefix}-${process.pid.toString(36)}${Date.now().toString(36)}${counter}`;
}

/**
 * Calls made from one client address, optionally as one signed-in account.
 * `clientIp` is sent as X-Forwarded-For; the test server counts sign-ups and
 * failed sign-ins per address.
 */
export class Api {
  constructor(
    readonly baseUrl: string,
    private readonly clientIp: string,
    private readonly token = ''
  ) {}

  /** The same client, acting with another session token. */
  as(session: { token: string }): Api {
    return new Api(this.baseUrl, this.clientIp, session.token);
  }

  private async send(path: string, body?: unknown): Promise<{ status: number; json: Json }> {
    const res = await fetch(this.baseUrl + path, {
      method: body === undefined ? 'GET' : 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Forwarded-For': this.clientIp,
        ...(this.token ? { Authorization: `Bearer ${this.token}` } : {})
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) })
    });
    const json = (await res.json().catch(() => ({}))) as Json;
    return { status: res.status, json };
  }

  /** GET (no body) or POST to an /auth or /tenants endpoint; throws unless it answers 2xx. */
  async http<T = Json>(path: string, body?: unknown): Promise<T> {
    const { status, json } = await this.send(path, body);
    if (status < 200 || status >= 300) {
      throw new ApiError(
        status,
        String(json.code ?? ''),
        `${path}: ${status} ${JSON.stringify(json)}`
      );
    }
    return json as T;
  }

  /** The status an endpoint answers with, without throwing. */
  async status(path: string, body?: unknown): Promise<number> {
    return (await this.send(path, body)).status;
  }

  /** A Connect RPC with a JSON body, e.g. rpc('HiveService/CreateHive', {...}). */
  async rpc<T = Json>(method: string, message: unknown = {}): Promise<T> {
    const { status, json } = await this.send(`/openbeehive.v1.${method}`, message);
    if (status !== 200) {
      throw new ApiError(
        status,
        String(json.code ?? ''),
        `${method}: ${status} ${JSON.stringify(json)}`
      );
    }
    return json as T;
  }

  // --- accounts and workspaces ---

  async signUp(
    email = `${unique('user')}@e2e.test`,
    password = 'e2e-password-1'
  ): Promise<Account> {
    const j = await this.http<{ token: string; user_id: string; active_org: string }>(
      '/auth/signup',
      { email, password, name: '' }
    );
    return { email, password, token: j.token, userId: j.user_id, orgId: j.active_org };
  }

  async signIn(email: string, password: string): Promise<Session> {
    const j = await this.http<{ token: string; user_id: string; active_org: string }>(
      '/auth/signin',
      { email, password }
    );
    return { token: j.token, userId: j.user_id, orgId: j.active_org };
  }

  /** Creates a workspace owned by this account; the returned session works in it. */
  async createWorkspace(name: string): Promise<Session> {
    const me = await this.http<{ user_id: string }>('/auth/me');
    const j = await this.http<{ token: string; org_id: string }>('/tenants/create', { name });
    return { token: j.token, userId: me.user_id, orgId: j.org_id };
  }

  /** Invites `email` into this account's workspace `orgId`; returns the invite token. */
  async invite(orgId: string, email: string): Promise<string> {
    const j = await this.http<{ token: string }>('/tenants/invite', { org_id: orgId, email });
    return j.token;
  }

  /** Accepts an invite as this account; the returned session works in the joined workspace. */
  async acceptInvite(inviteToken: string): Promise<Session> {
    const me = await this.http<{ user_id: string }>('/auth/me');
    const j = await this.http<{ token: string; active_org: string }>('/auth/accept-invite', {
      token: inviteToken
    });
    return { token: j.token, userId: me.user_id, orgId: j.active_org };
  }

  async removeMember(orgId: string, userId: string): Promise<void> {
    await this.http('/tenants/members/remove', { org_id: orgId, user_id: userId });
  }

  // --- records (the session's active workspace) ---

  async createApiary(name: string, extra: Json = {}): Promise<string> {
    const j = await this.rpc<{ apiary: { id: string } }>('ApiaryService/CreateApiary', {
      name,
      ...extra
    });
    return j.apiary.id;
  }

  async createHive(apiaryId: string, name: string): Promise<string> {
    const j = await this.rpc<{ hive: { id: string } }>('HiveService/CreateHive', {
      apiaryId,
      name
    });
    return j.hive.id;
  }

  async createInspection(hiveId: string, fields: Json = {}): Promise<string> {
    const j = await this.rpc<{ inspection: { id: string } }>('InspectionService/CreateInspection', {
      hiveId,
      ...fields
    });
    return j.inspection.id;
  }

  /** Visits a person recorded for the hive (sensor readings left out). */
  async listInspections(hiveId: string): Promise<Json[]> {
    const j = await this.rpc<{ inspections?: Json[] }>('InspectionService/ListInspections', {
      hiveId
    });
    return j.inspections ?? [];
  }

  async createHarvest(hiveId: string, amountKg: number, fields: Json = {}): Promise<string> {
    const j = await this.rpc<{ harvest: { id: string } }>('HarvestService/CreateHarvest', {
      hiveId,
      amountKg,
      ...fields
    });
    return j.harvest.id;
  }

  async listHarvests(hiveId: string): Promise<Json[]> {
    const j = await this.rpc<{ harvests?: Json[] }>('HarvestService/ListHarvests', { hiveId });
    return j.harvests ?? [];
  }

  async createTreatment(hiveId: string, product: string, fields: Json = {}): Promise<string> {
    const j = await this.rpc<{ treatment: { id: string } }>('TreatmentService/CreateTreatment', {
      hiveId,
      product,
      ...fields
    });
    return j.treatment.id;
  }

  async listTreatments(hiveId: string): Promise<Json[]> {
    const j = await this.rpc<{ treatments?: Json[] }>('TreatmentService/ListTreatments', {
      hiveId
    });
    return j.treatments ?? [];
  }
}
