import type { ApiAccounts, ApiConfig, ApiEvents, ApiSpawn, ApiState } from './protocol.gen';
import { getToken } from './auth';

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public issues: string[] = [],
  ) {
    super(message);
  }
  /** One-line text for inline display: "CODE: message (issue; issue)". */
  get text(): string {
    return `${this.code}: ${this.message}` + (this.issues.length ? ` (${this.issues.join('; ')})` : '');
  }
}

let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn;
}

export function errText(e: unknown): string {
  return e instanceof ApiError ? e.text : e instanceof Error ? e.message : String(e);
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Authorization: 'Bearer ' + getToken() };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  let res: Response;
  try {
    res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    throw new ApiError(0, 'NETWORK', 'hub unreachable');
  }
  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    /* non-JSON body */
  }
  if (!res.ok) {
    const d = (data ?? {}) as { code?: string; error?: string; issues?: unknown };
    if (res.status === 401) onUnauthorized();
    const issues = Array.isArray(d.issues) ? d.issues.map(String) : [];
    throw new ApiError(res.status, d.code ?? 'HTTP_' + res.status, d.error ?? res.statusText, issues);
  }
  return data as T;
}

const enc = encodeURIComponent;

export type Approval = ApiState.Approval;
export interface SpawnBody {
  account?: string;
  role: string;
  kind?: string;
  model?: string;
  effort?: string;
  callsign?: string;
  task: string;
  plan?: { id: string; title: string; acceptance: string }[];
  scope?: { paths: string[]; forbidden: string[]; ask_before: string[] };
  base_branch?: string;
  dry_run?: boolean;
}

export const api = {
  state: () => call<ApiState.ApiState>('GET', '/api/state'),
  events: (id: string, since = 0) => call<ApiEvents.ApiEvents>('GET', `/api/runs/${enc(id)}/events?since=${since}`),
  message: (id: string, text: string) => call<{ ok: true }>('POST', `/api/runs/${enc(id)}/message`, { text }),
  answer: (id: string, qid: string, text: string) => call<{ ok: true }>('POST', `/api/runs/${enc(id)}/answer`, { qid, text }),
  control: (id: string, verb: 'pause' | 'resume' | 'stop' | 'kill') =>
    call<{ ok: true }>('POST', `/api/runs/${enc(id)}/${verb}`, verb === 'kill' ? undefined : {}),
  spawn: (b: SpawnBody) => call<ApiSpawn.ApiSpawn>('POST', '/api/spawn', b),
  patchAccount: (id: string, fields: Record<string, unknown>) => call<ApiAccounts.Account>('PATCH', `/api/accounts/${enc(id)}`, fields),
  config: () => call<ApiConfig.ApiConfig>('GET', '/api/config'),
  patchConfig: (fields: Record<string, unknown>) => call<ApiConfig.ApiConfig>('PATCH', '/api/config', fields),
  decide: (id: string, verb: 'approve' | 'reject', note: string) =>
    call<Approval>('POST', `/api/approvals/${enc(id)}/${verb}`, note ? { note } : {}),
};
