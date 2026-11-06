// Phase 1 acceptance: a full agent lifecycle through the real `baton` CLI and an in-process hub,
// including `ask --blocking` answered via the API and hub-down spooling with recovery on restart.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, readFileSync, existsSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { startHub } from '../hub/server.ts';
import type { HubHandle } from '../hub/server.ts';
import type { StoredEvent } from '../protocol/types.ts';

const repo = join(dirname(fileURLToPath(import.meta.url)), '..');
const bin = join(repo, 'bin', 'baton.js');

interface Res { code: number | null; out: string; err: string }

function baton(env: Record<string, string>, args: string[], input?: string): Promise<Res> {
  return new Promise((resolve) => {
    const p = spawn(process.execPath, [bin, ...args], {
      cwd: repo,
      env: { ...process.env, ...env, BATON_POLL_INTERVAL_S: '0.1', BATON_HEARTBEAT_INTERVAL_S: '0.3' },
    });
    let out = '';
    let err = '';
    p.stdout.on('data', (d) => (out += d));
    p.stderr.on('data', (d) => (err += d));
    p.on('close', (code) => resolve({ code, out: out.trim(), err: err.trim() }));
    p.stdin.end(input ?? '');
  });
}

async function api(hub: HubHandle, method: string, path: string, body?: unknown): Promise<any> {
  const r = await fetch(hub.url + path, {
    method,
    headers: { authorization: `Bearer ${hub.adminToken}`, 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return r.json();
}

async function events(hub: HubHandle, runId: string): Promise<StoredEvent[]> {
  return (await api(hub, 'GET', `/api/runs/${runId}/events?since=0`)).events;
}

async function until<T>(fn: () => Promise<T | undefined>, ms = 5000): Promise<T> {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v !== undefined) return v;
    if (Date.now() > end) throw new Error('timed out waiting');
    await new Promise((r) => setTimeout(r, 50));
  }
}

test('full agent lifecycle with blocking ask and hub-down spooling', async (t) => {
  const dataDir = mkdtempSync(join(tmpdir(), 'baton-life-'));
  const adminToken = 'bt_adm_test_lifecycle';
  let hub = await startHub({ dataDir, port: 0, dev: true, adminToken, spoolPollMs: 100, log: () => {} });
  t.after(async () => {
    await hub.close();
    rmSync(dataDir, { recursive: true, force: true });
  });

  const plan = [
    { id: 's1', title: 'Write the thing', acceptance: 'node thing.js prints ok' },
    { id: 's2', title: 'Test the thing', acceptance: 'npm test passes' },
  ];
  const reg = await api(hub, 'POST', '/api/dev/runs', { role: 'implementer', kind: 'generic', plan });
  assert.equal(reg.ok, true);
  const env: Record<string, string> = reg.env;
  const runId: string = reg.run_id;
  assert.ok(existsSync(join(reg.run_dir, 'run.json')));

  // --- hub up: report normally ---------------------------------------------------------------
  const ok = (r: Res) => {
    assert.equal(r.code, 0, `exit ${r.code}: ${r.out} ${r.err}`);
    assert.match(r.out, /^ok #\d+/);
  };
  ok(await baton(env, ['hello']));
  ok(await baton(env, ['plan', '--confirm']));
  ok(await baton(env, ['step', 's1', 'start', '--note', 'starting']));
  ok(await baton(env, ['progress', '30', 'scaffolded']));

  // blocking ask: answer it through the API once the question shows up
  const asking = baton(env, ['ask', 'Which db?', '--options', 'sqlite,postgres', '--blocking', '--timeout', '20']);
  const q = await until(async () => (await events(hub, runId)).find((e) => e.type === 'question'));
  assert.equal(q.payload.blocking, true);
  assert.deepEqual(q.payload.options, ['sqlite', 'postgres']);
  const state = await api(hub, 'GET', '/api/state');
  const summary = state.runs.find((r: any) => r.run_id === runId);
  assert.equal(summary.status, 'waiting');
  // an unrelated message first: ask must not consume it
  assert.equal((await api(hub, 'POST', `/api/runs/${runId}/message`, { text: 'fyi: main moved', from: 'conductor' })).ok, true);
  assert.equal((await api(hub, 'POST', `/api/runs/${runId}/answer`, { qid: q.payload.qid, text: 'sqlite' })).ok, true);
  const answered = await asking;
  assert.equal(answered.code, 0);
  assert.equal(answered.out, 'sqlite');

  const inbox = await baton(env, ['inbox']);
  assert.equal(inbox.code, 0);
  assert.match(inbox.out, /fyi: main moved/);
  assert.doesNotMatch(inbox.out, /sqlite/);
  assert.equal((await baton(env, ['inbox'])).out, 'inbox empty');

  ok(await baton(env, ['step', 's1', 'done', '--summary', 'thing works', '--evidence', 'node thing.js']));

  // --- hub down: the agent keeps working, CLI spools -----------------------------------------
  await hub.close();
  const spooled = async (args: string[]) => {
    const r = await baton(env, args);
    assert.equal(r.code, 0);
    assert.match(r.out, /spooled/);
  };
  await spooled(['step', 's2', 'start']);
  await spooled(['artifact', 'commit', 'abc1234', '--note', 'tests added']);
  await spooled(['progress', '90', 'tests green']);
  // a hand-written spool line (Layer B), no msg_id
  const handLine = { v: 1, run_id: runId, agent_id: env.BATON_AGENT_ID, type: 'progress', payload: { percent: 95, note: 'by hand' } };
  writeFileSync(join(reg.run_dir, 'outbox.jsonl'), JSON.stringify(handLine) + '\n', { flag: 'a' });

  // --- hub restarts: recovers the outbox, no duplicates --------------------------------------
  hub = await startHub({ dataDir, port: 0, dev: true, adminToken, spoolPollMs: 100, log: () => {} });
  env.BATON_URL = hub.url; // new random port
  let evs = await events(hub, runId);
  const notes = evs.filter((e) => e.type === 'progress').map((e) => e.payload.note);
  assert.deepEqual(notes, ['scaffolded', 'tests green', 'by hand']);
  assert.ok(evs.some((e) => e.type === 'artifact' && e.src === 'spool'));

  ok(await baton(env, ['step', 's2', 'done', '--summary', 'tests pass', '--evidence', 'npm test']));
  ok(await baton(env, ['done', '--status', 'success', '--summary', 'thing written and tested']));

  // restart again: nothing re-ingested twice
  await hub.close();
  hub = await startHub({ dataDir, port: 0, dev: true, adminToken, spoolPollMs: 100, log: () => {} });
  evs = await events(hub, runId);

  const seqs = evs.map((e) => e.seq);
  assert.deepEqual(seqs, seqs.map((_, i) => i + 1), 'seq is 1..n with no gaps');
  const ids = evs.map((e) => e.msg_id);
  assert.equal(new Set(ids).size, ids.length, 'no duplicate msg_id');
  const types = evs.filter((e) => e.src !== 'hub' && e.type !== 'heartbeat').map((e) => e.type);
  assert.deepEqual(types, [
    'hello', 'plan', 'step_start', 'progress', 'question', 'step_done',
    'step_start', 'artifact', 'progress', 'progress', 'step_done', 'done',
  ]);
  const planEv = evs.find((e) => e.type === 'plan')!;
  assert.deepEqual(planEv.payload.steps, plan, 'plan --confirm filled in the conductor plan');
  assert.ok(evs.some((e) => e.type === 'to_agent' && (e.payload as any).type === 'answer'));

  const final = (await api(hub, 'GET', '/api/state')).runs.find((r: any) => r.run_id === runId);
  assert.equal(final.status, 'done');
  assert.deepEqual(final.step_states, { s1: 'done', s2: 'done' });

  // token revoked after done: CLI still exits 0
  env.BATON_URL = hub.url;
  const late = await baton(env, ['progress', '100', 'after done']);
  assert.equal(late.code, 0);
  assert.match(late.out + late.err, /rejected/);
  const runJson = JSON.parse(readFileSync(join(reg.run_dir, 'run.json'), 'utf8'));
  assert.equal(runJson.token_revoked, true);
  assert.ok(!JSON.stringify(runJson).includes(env.BATON_TOKEN!), 'plaintext token never stored');
});
