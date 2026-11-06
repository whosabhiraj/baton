// The `baton` CLI: what agents call to report to, and hear from, the hub. See protocol/PROTOCOL.md §3.4 + A1-A11.
import { randomUUID } from 'node:crypto';
import { appendFileSync, existsSync, mkdirSync, readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import { RUN_FILES, projectDataDir } from '../hub/paths.ts';
import { validateAgentPayload } from '../protocol/types.ts';

const USAGE = `usage: baton <command>
  hello | plan [--file f | --confirm] | step <id> start|done|blocked [--note|--summary|--evidence|--reason]
  progress <0-100> "note" | artifact <file|diff|commit|url|log> <ref> [--note]
  ask "question" [--options a,b] [--blocking] [--timeout s] | inbox | heartbeat
  done --status success|partial|failed --summary ".." [--followups ".."] | error "msg" [--fatal]
  up [--port N] [--dev]   (foreground hub for manual testing)`;

class UsageError extends Error {}
type Payload = Record<string, unknown>;
type Inbox = { n: number; type: string; payload: Payload };
type Opts = {
  note?: string; summary?: string; evidence?: string; reason?: string; options?: string; blocking?: boolean;
  timeout?: string; status?: string; followups?: string; fatal?: boolean; file?: string; confirm?: boolean;
  port?: string; dev?: boolean; help?: boolean;
};

const OPTION_DEFS = {
  note: { type: 'string' }, summary: { type: 'string' }, evidence: { type: 'string' }, reason: { type: 'string' },
  options: { type: 'string' }, blocking: { type: 'boolean' }, timeout: { type: 'string' }, status: { type: 'string' },
  followups: { type: 'string' }, fatal: { type: 'boolean' }, file: { type: 'string' }, confirm: { type: 'boolean' },
  port: { type: 'string' }, dev: { type: 'boolean' }, help: { type: 'boolean', short: 'h' },
} as const;

const num = (v: string | undefined, d: number): number => {
  const x = Number(v);
  return v !== undefined && v !== '' && Number.isFinite(x) && x >= 0 ? x : d;
};
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const say = (s: string) => console.log(s);

function need<T>(v: T | undefined, what: string): T {
  if (v === undefined || v === '') throw new UsageError(`missing ${what}`);
  return v;
}

function runEnv() {
  const env = process.env;
  const run_id = env.BATON_RUN_ID, agent_id = env.BATON_AGENT_ID, dir = env.BATON_RUN_DIR;
  if (!run_id || !agent_id || !dir) {
    throw new UsageError('not inside a Baton run (BATON_RUN_ID, BATON_AGENT_ID, BATON_RUN_DIR must be set)');
  }
  return { run_id, agent_id, dir, url: env.BATON_URL?.replace(/\/+$/, ''), token: env.BATON_TOKEN };
}
type RunEnv = ReturnType<typeof runEnv>;

/** Send one agent message. Returns the exit code; prints one line unless quiet. */
async function send(e: RunEnv, type: string, payload: Payload, quiet = false): Promise<number> {
  const v = validateAgentPayload(type, payload);
  if (!v.ok) throw new UsageError(`invalid ${type}: ${v.error}`);
  const msg = { v: 1, run_id: e.run_id, agent_id: e.agent_id, ts: new Date().toISOString(), type, payload: v.payload, msg_id: randomUUID() };
  const out = (s: string) => { if (!quiet) say(s); };
  let spool = !e.url || !e.token;
  if (!spool) {
    try {
      const res = await fetch(`${e.url}/api/ingest`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', authorization: `Bearer ${e.token}` },
        body: JSON.stringify(msg),
        signal: AbortSignal.timeout(3000),
      });
      const body = (await res.json().catch(() => ({}))) as { seq?: number; duplicate?: boolean; code?: string; error?: string };
      if (res.status >= 500) spool = true;
      else if (res.ok) { out(`ok #${body.seq ?? '?'}${body.duplicate ? ' (dup)' : ''}`); return 0; }
      else if (res.status === 400) { console.error(`baton: rejected: ${body.code ?? 400} ${body.error ?? ''}`.trim()); return 2; }
      else { console.error(`baton: rejected: ${body.code ?? res.status}`); return 0; }
    } catch { spool = true; }
  }
  try {
    mkdirSync(e.dir, { recursive: true });
    appendFileSync(join(e.dir, RUN_FILES.outbox), JSON.stringify(msg) + '\n');
    out('spooled (hub unreachable)');
  } catch (err) {
    console.error(`baton: warning: could not report (${(err as Error).message})`);
  }
  return 0;
}

// ---- inbox ----------------------------------------------------------------

const inboxDir = (e: RunEnv) => join(e.dir, RUN_FILES.inboxDir);
const readFilePath = (e: RunEnv) => join(inboxDir(e), RUN_FILES.inboxRead);

function readSet(e: RunEnv): Set<number> {
  try {
    return new Set(readFileSync(readFilePath(e), 'utf8').split(/\s+/).filter(Boolean).map(Number));
  } catch { return new Set(); }
}

function markRead(e: RunEnv, ns: number[]): void {
  if (!ns.length) return;
  try {
    mkdirSync(inboxDir(e), { recursive: true });
    appendFileSync(readFilePath(e), ns.map((n) => `${n}\n`).join(''));
  } catch { /* best effort */ }
}

/** All inbox messages, from local files (or the hub API if the run dir isn't readable), sorted by n. */
async function loadInbox(e: RunEnv): Promise<Inbox[]> {
  let msgs: unknown[] = [];
  if (existsSync(e.dir)) {
    let names: string[] = [];
    try { names = readdirSync(inboxDir(e)).filter((f) => /^\d{6}\.json$/.test(f)); } catch { /* no inbox yet */ }
    for (const f of names) {
      try { msgs.push(JSON.parse(readFileSync(join(inboxDir(e), f), 'utf8'))); } catch { /* partial/bad file */ }
    }
  } else if (e.url && e.token) {
    try {
      const res = await fetch(`${e.url}/api/runs/${e.run_id}/inbox?after=0`, {
        headers: { authorization: `Bearer ${e.token}` }, signal: AbortSignal.timeout(3000),
      });
      if (res.ok) msgs = ((await res.json()) as { messages?: unknown[] }).messages ?? [];
    } catch { /* unreachable */ }
  }
  return msgs
    .filter((m): m is Inbox => !!m && typeof (m as Inbox).n === 'number' && typeof (m as Inbox).type === 'string')
    .map((m) => ({ n: m.n, type: m.type, payload: m.payload ?? {} }))
    .sort((a, b) => a.n - b.n);
}

function describe(m: Inbox): string {
  const p = m.payload;
  switch (m.type) {
    case 'answer': return `[${m.n}] answer ${p.qid}: ${p.text}`;
    case 'message': return `[${m.n}] message (${p.from}): ${p.text}`;
    case 'plan_update': {
      const steps = (Array.isArray(p.steps) ? p.steps : []) as { id?: string; title?: string }[];
      return `[${m.n}] plan_update: ${steps.map((s) => `${s.id} ${s.title}`).join('; ')}`;
    }
    default: return `[${m.n}] ${m.type}${p.reason ? `: ${p.reason}` : ''}`;
  }
}

async function cmdInbox(e: RunEnv): Promise<number> {
  const read = readSet(e);
  const unread = (await loadInbox(e)).filter((m) => !read.has(m.n));
  if (!unread.length) say('inbox empty');
  for (const m of unread) say(describe(m).replace(/\s*\n\s*/g, ' '));
  markRead(e, unread.map((m) => m.n));
  return 0;
}

async function cmdAsk(e: RunEnv, text: string, o: Opts): Promise<number> {
  const qid = `q_${Array.from({ length: 6 }, () => Math.floor(Math.random() * 36).toString(36)).join('')}`;
  const payload: Payload = { qid, text, blocking: !!o.blocking };
  if (o.options) payload.options = o.options.split(',').map((s) => s.trim()).filter(Boolean);
  const timeoutS = o.timeout !== undefined ? num(o.timeout, NaN) : num(process.env.BATON_ASK_TIMEOUT_S, 900);
  if (Number.isNaN(timeoutS)) throw new UsageError('--timeout must be a number of seconds');
  const code = await send(e, 'question', payload, true);
  if (!o.blocking) { if (code === 0) say(`qid ${qid}`); return code; }
  if (code !== 0) return code;

  const pollMs = Math.max(10, num(process.env.BATON_POLL_INTERVAL_S, 2) * 1000);
  const hbMs = Math.max(10, num(process.env.BATON_HEARTBEAT_INTERVAL_S, 30) * 1000);
  const start = Date.now();
  let lastHb = start;
  for (;;) {
    const read = readSet(e);
    const hit = (await loadInbox(e)).find((m) => m.type === 'answer' && m.payload.qid === qid && !read.has(m.n));
    if (hit) { markRead(e, [hit.n]); say(String(hit.payload.text ?? '')); return 0; }
    const left = start + timeoutS * 1000 - Date.now();
    if (left <= 0) { say('NO_ANSWER'); return 0; }
    if (Date.now() - lastHb >= hbMs) { lastHb = Date.now(); await send(e, 'heartbeat', {}, true); }
    await sleep(Math.min(pollMs, Math.max(1, left)));
  }
}

// ---- plan -----------------------------------------------------------------

function readPlan(o: Opts): Payload {
  let raw: string;
  if (!o.file && process.stdin.isTTY) throw new UsageError('plan needs --file <plan.json>, piped JSON on stdin, or --confirm');
  try {
    raw = readFileSync(o.file ?? 0, 'utf8');
  } catch (err) {
    throw new UsageError(`cannot read plan: ${(err as Error).message}`);
  }
  let j: unknown;
  try { j = JSON.parse(raw); } catch { throw new UsageError('plan is not valid JSON'); }
  return Array.isArray(j) ? { steps: j } : (j as Payload);
}

// ---- commands -------------------------------------------------------------

async function cmdUp(o: Opts): Promise<number> {
  const { startHub } = await import('../hub/server.ts');
  const dataDir = projectDataDir(process.cwd());
  const hub = await startHub({ dataDir, port: o.port ? Number(o.port) : undefined, dev: o.dev });
  say(`baton hub: ${hub.url}`);
  say(`admin token: ${hub.adminToken}`);
  say(`data dir: ${dataDir}`);
  await new Promise<void>((resolve) => { process.once('SIGINT', resolve); process.once('SIGTERM', resolve); });
  await hub.close();
  return 0;
}

const MAX_POSITIONALS: Record<string, number> = {
  hello: 0, heartbeat: 0, inbox: 0, plan: 0, done: 0, step: 2, progress: 2, artifact: 2, ask: 1, error: 1,
};

async function dispatch(argv: string[]): Promise<number> {
  let parsed;
  try {
    parsed = parseArgs({ args: argv, options: OPTION_DEFS, allowPositionals: true, strict: true });
  } catch (err) {
    throw new UsageError((err as Error).message);
  }
  const o = parsed.values as Opts;
  const [cmd, ...pos] = parsed.positionals;
  if (!cmd && !o.help) throw new UsageError('missing command');
  if (!cmd || cmd === 'help' || o.help) { say(USAGE); return 0; }
  if (cmd === 'up') return cmdUp(o);

  const max = MAX_POSITIONALS[cmd];
  if (max === undefined) throw new UsageError(`unknown command "${cmd}"`);
  if (pos.length > max) throw new UsageError(`unexpected argument "${pos[max]}"`);
  const e = runEnv();

  switch (cmd) {
    case 'hello': return send(e, 'hello', { model: process.env.BATON_MODEL, cwd: process.cwd(), brief_ack: true });
    case 'heartbeat': return send(e, 'heartbeat', {});
    case 'inbox': return cmdInbox(e);
    case 'plan': return send(e, 'plan', o.confirm ? { confirm: true } : readPlan(o));
    case 'step': {
      const id = need(pos[0], 'step id'), action = need(pos[1], 'action (start|done|blocked)');
      if (action === 'start') return send(e, 'step_start', { step_id: id, note: o.note });
      if (action === 'done') return send(e, 'step_done', { step_id: id, summary: need(o.summary, '--summary'), evidence: o.evidence });
      if (action === 'blocked') return send(e, 'step_blocked', { step_id: id, reason: need(o.reason, '--reason') });
      throw new UsageError(`unknown step action "${action}" (start|done|blocked)`);
    }
    case 'progress': {
      const s = need(pos[0], 'percent (0-100)');
      if (!/^\d+(\.\d+)?$/.test(s) || Number(s) > 100) throw new UsageError(`percent must be 0-100, got "${s}"`);
      return send(e, 'progress', { percent: Number(s), note: pos[1] ?? '' });
    }
    case 'artifact':
      return send(e, 'artifact', { kind: need(pos[0], 'kind (file|diff|commit|url|log)'), ref: need(pos[1], 'ref'), note: o.note });
    case 'ask': return cmdAsk(e, need(pos[0], 'question text'), o);
    case 'done':
      return send(e, 'done', { status: need(o.status, '--status success|partial|failed'), summary: need(o.summary, '--summary'), followups: o.followups });
    default: return send(e, 'error', { message: need(pos[0], 'error message'), fatal: !!o.fatal });
  }
}

export async function main(argv: string[]): Promise<number> {
  try {
    return await dispatch(argv);
  } catch (err) {
    if (err instanceof UsageError) { console.error(`baton: ${err.message}. Try: baton help`); return 2; }
    console.error(`baton: ${(err as Error).message}`);
    return 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = await main(process.argv.slice(2));
}
