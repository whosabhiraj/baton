// Single client-side store. Server state is authoritative (refetched, throttled);
// the only thing we keep ourselves is a per-run ring of stored events.
import type { ApiAccounts, ApiEvents, ApiState } from './protocol.gen';
import { api, errText, type Approval } from './api';

export type Run = ApiState.RunSummary;
export type Account = ApiAccounts.Account;
export type StoredEvent = ApiEvents.StoredEvent;
export type ConfigIssue = { file?: string; path?: string; line?: number; msg?: string };
export type Conn = 'connecting' | 'live' | 'down' | 'auth';
export type Overlay = null | 'spawn' | 'approvals' | 'config';

const RING = 500;

export interface State {
  conn: Conn;
  connNote: string;
  loaded: boolean;
  instance: string;
  runs: Run[];
  accounts: Account[];
  mode: string;
  conductor: ApiState.Conductor;
  approvals: Approval[];
  configOk: boolean;
  configIssues: ConfigIssue[];
  selected: string | null;
  accountFilter: string | null;
  view: 'list' | 'scene';
  overlay: Overlay;
  notices: string[];
}

export const state: State = {
  conn: 'connecting',
  connNote: '',
  loaded: false,
  instance: '',
  runs: [],
  accounts: [],
  mode: '',
  conductor: { state: 'off' },
  approvals: [],
  configOk: true,
  configIssues: [],
  selected: null,
  accountFilter: null,
  view: 'list',
  overlay: null,
  notices: [],
};

const rings = new Map<string, StoredEvent[]>();
const loaded = new Set<string>();
const stamps: number[] = [];
const subs = new Set<() => void>();
let queued = false;

/** Subscribe to any change; calls are coalesced to one per animation frame. */
export function subscribe(fn: () => void): () => void {
  subs.add(fn);
  return () => subs.delete(fn);
}

export function emit(): void {
  if (queued) return;
  queued = true;
  requestAnimationFrame(() => {
    queued = false;
    subs.forEach((f) => f());
  });
}

export function ringOf(runId: string): StoredEvent[] {
  return rings.get(runId) ?? [];
}

export function eventsPerMin(now = Date.now()): number {
  while (stamps.length && stamps[0]! < now - 60_000) stamps.shift();
  return stamps.length;
}

export function runById(id: string | null): Run | undefined {
  return id ? state.runs.find((r) => r.run_id === id) : undefined;
}

export function accountById(id: string | undefined): Account | undefined {
  return id ? state.accounts.find((a) => a.id === id) : undefined;
}

function pushEvent(ev: StoredEvent): void {
  let r = rings.get(ev.run_id);
  if (!r) rings.set(ev.run_id, (r = []));
  const last = r[r.length - 1];
  if (last && ev.seq <= last.seq) {
    if (r.some((e) => e.seq === ev.seq)) return;
    r.push(ev);
    r.sort((a, b) => a.seq - b.seq);
  } else r.push(ev);
  if (r.length > RING) r.splice(0, r.length - RING);
}

/** Called for every `baton` SSE frame. */
export function onEvent(ev: StoredEvent): void {
  stamps.push(Date.now());
  pushEvent(ev);
  scheduleRefresh();
  emit();
}

/** Called for every `hub` SSE frame. */
export function onNotice(n: { type?: string } & Record<string, unknown>): void {
  if (n.type === 'idle_stop') state.notices.push('Hub is stopping itself (idle).');
  if (n.type === 'spool_warning') state.notices.push(`Spool warning for ${String(n.run_id)}: ${String(n.code)} ${String(n.detail)}`);
  if (state.notices.length > 5) state.notices.shift();
  scheduleRefresh();
  emit();
}

// ---- state refresh (throttled to ~2/s) ----

let timer: number | undefined;
let lastRun = 0;
let inflight = false;

export function scheduleRefresh(): void {
  if (timer !== undefined) return;
  const wait = Math.max(0, 500 - (Date.now() - lastRun));
  timer = window.setTimeout(() => {
    timer = undefined;
    void refresh();
  }, wait);
}

export async function refresh(): Promise<void> {
  if (inflight) {
    scheduleRefresh();
    return;
  }
  inflight = true;
  lastRun = Date.now();
  try {
    apply(await api.state());
    if (state.conn === 'down' || state.conn === 'auth') state.conn = 'live';
  } catch (e) {
    const code = (e as { code?: string }).code;
    if (code === 'UNAUTHORIZED') state.conn = 'auth';
    else {
      state.conn = 'down';
      state.connNote = errText(e);
    }
  } finally {
    inflight = false;
    emit();
  }
}

function apply(s: ApiState.ApiState): void {
  state.loaded = true;
  state.runs = s.runs;
  state.accounts = (s.accounts ?? []) as Account[];
  state.mode = s.mode ?? '';
  state.conductor = s.conductor ?? { state: 'off' };
  state.approvals = s.approvals ?? [];
  state.configOk = s.config_ok ?? true;
  state.configIssues = (s.config_issues ?? []) as ConfigIssue[];
  if (s.instance) state.instance = s.instance;
  if (state.selected && !runById(state.selected)) state.selected = null;
  if (state.accountFilter && !accountById(state.accountFilter)) state.accountFilter = null;
}

// ---- per-run history ----

export async function loadHistory(runId: string, force = false): Promise<void> {
  if (loaded.has(runId) && !force) return;
  loaded.add(runId);
  try {
    const r = await api.events(runId, 0);
    r.events.forEach(pushEvent);
  } catch {
    loaded.delete(runId); // try again next time it is selected
  }
  emit();
}

/** Hub restarted or the stream said `reset`: drop events and reload everything we show. */
export async function fullReload(): Promise<void> {
  rings.clear();
  loaded.clear();
  await refresh();
  if (state.selected) await loadHistory(state.selected);
}

export function select(id: string | null): void {
  state.selected = id;
  if (id) void loadHistory(id);
  emit();
}

export function setOverlay(o: Overlay): void {
  state.overlay = o;
  emit();
}
