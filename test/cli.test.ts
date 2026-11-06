import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { after, before, describe, it } from 'node:test';

// BATON_TEST_BIN=<exe> runs the Go binary directly; default is the TS reference CLI.
const EXE = process.env.BATON_TEST_BIN;
const CMD = EXE ?? process.execPath;
const pre = (args: string[]): string[] => (EXE ? args : ['bin/baton.js', ...args]);
const roots: string[] = [];

function newRun(extra: Record<string, string> = {}) {
  const root = mkdtempSync(join(tmpdir(), 'baton-cli-'));
  roots.push(root);
  const dir = join(root, 'run');
  const env: Record<string, string> = {
    PATH: process.env.PATH ?? '', SystemRoot: process.env.SystemRoot ?? '',
    BATON_RUN_ID: 'r_test', BATON_AGENT_ID: 'cc-a-0001', BATON_RUN_DIR: dir,
    BATON_URL: 'http://127.0.0.1:9', BATON_TOKEN: 'bt_run_x', ...extra,
  };
  const outbox = () =>
    existsSync(join(dir, 'outbox.jsonl'))
      ? readFileSync(join(dir, 'outbox.jsonl'), 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l))
      : [];
  return { dir, env, outbox };
}

function baton(env: Record<string, string>, args: string[], input?: string) {
  const r = spawnSync(CMD, pre(args), { env, input, encoding: 'utf8', timeout: 20000 });
  return { code: r.status, out: r.stdout.trim(), err: r.stderr.trim() };
}

/** Async variant: needed when the fake hub lives in this process (spawnSync would block it). */
function batonAsync(env: Record<string, string>, args: string[]) {
  return new Promise<{ code: number | null; out: string; err: string }>((resolve) => {
    const c = spawn(CMD, pre(args), { env });
    let out = '', err = '';
    c.stdout.on('data', (d) => { out += d; });
    c.stderr.on('data', (d) => { err += d; });
    const t = setTimeout(() => c.kill(), 20000);
    c.on('close', (code) => { clearTimeout(t); resolve({ code, out: out.trim(), err: err.trim() }); });
  });
}

function putInbox(dir: string, n: number, type: string, payload: object) {
  mkdirSync(join(dir, 'inbox'), { recursive: true });
  writeFileSync(join(dir, 'inbox', `${String(n).padStart(6, '0')}.json`), JSON.stringify({ v: 1, n, ts: new Date().toISOString(), type, payload }));
}

after(() => { for (const r of roots) rmSync(r, { recursive: true, force: true }); });

describe('spool mode', () => {
  const cases: [string, string[], string, object][] = [
    ['hello', ['hello'], 'hello', { brief_ack: true }],
    ['step start', ['step', 's1', 'start', '--note', 'go'], 'step_start', { step_id: 's1', note: 'go' }],
    ['step done', ['step', 's1', 'done', '--summary', 'ok', '--evidence', 'abc'], 'step_done', { step_id: 's1', summary: 'ok', evidence: 'abc' }],
    ['step blocked', ['step', 's1', 'blocked', '--reason', 'stuck'], 'step_blocked', { step_id: 's1', reason: 'stuck' }],
    ['progress', ['progress', '40', 'half'], 'progress', { percent: 40, note: 'half' }],
    ['artifact', ['artifact', 'commit', 'abc123', '--note', 'n'], 'artifact', { kind: 'commit', ref: 'abc123', note: 'n' }],
    ['ask', ['ask', 'which db?', '--options', 'pg,sqlite'], 'question', { text: 'which db?', options: ['pg', 'sqlite'], blocking: false }],
    ['heartbeat', ['heartbeat'], 'heartbeat', {}],
    ['done', ['done', '--status', 'partial', '--summary', 'half', '--followups', 'more'], 'done', { status: 'partial', summary: 'half', followups: 'more' }],
    ['error', ['error', 'boom', '--fatal'], 'error', { message: 'boom', fatal: true }],
    ['plan --confirm', ['plan', '--confirm'], 'plan', { confirm: true }],
  ];
  for (const [name, args, type, expected] of cases) {
    it(name, () => {
      const { env, outbox } = newRun();
      const r = baton(env, args);
      assert.equal(r.code, 0, r.err);
      assert.match(r.out, name === 'ask' ? /^qid q_[0-9a-z]{6}$/ : /^spooled/);
      const [m] = outbox();
      assert.equal(outbox().length, 1);
      assert.equal(m.type, type);
      assert.equal(m.v, 1);
      assert.equal(m.run_id, 'r_test');
      assert.equal(m.agent_id, 'cc-a-0001');
      assert.match(m.msg_id, /^[0-9a-f-]{36}$/);
      assert.ok(!Number.isNaN(Date.parse(m.ts)));
      assert.deepEqual(Object.fromEntries(Object.entries(m.payload).filter(([k]) => k in expected)), expected);
    });
  }

  it('hello includes cwd and model', () => {
    const { env, outbox } = newRun({ BATON_MODEL: 'opus' });
    baton(env, ['hello']);
    assert.equal(outbox()[0].payload.model, 'opus');
    assert.equal(outbox()[0].payload.cwd, process.cwd());
  });

  it('spools without URL/token', () => {
    const { env, outbox } = newRun();
    delete env.BATON_URL; delete env.BATON_TOKEN;
    assert.equal(baton(env, ['heartbeat']).code, 0);
    assert.equal(outbox().length, 1);
  });

  it('plan from stdin, bare array, and --file', () => {
    const steps = [{ id: 's1', title: 'T', acceptance: 'A' }];
    const a = newRun();
    assert.equal(baton(a.env, ['plan'], JSON.stringify({ steps, reason: 'r' })).code, 0);
    assert.deepEqual(a.outbox()[0].payload, { steps, reason: 'r' });
    const b = newRun();
    assert.equal(baton(b.env, ['plan'], JSON.stringify(steps)).code, 0);
    assert.deepEqual(b.outbox()[0].payload, { steps });
    const c = newRun();
    const f = join(c.dir, '..', 'plan.json');
    writeFileSync(f, JSON.stringify(steps));
    assert.equal(baton(c.env, ['plan', '--file', f]).code, 0);
    assert.deepEqual(c.outbox()[0].payload, { steps });
  });
});

describe('usage errors (exit 2)', () => {
  const bad: [string, string[], string?][] = [
    ['bad percent', ['progress', '101', 'x']],
    ['non-numeric percent', ['progress', 'abc', 'x']],
    ['bad status', ['done', '--status', 'great', '--summary', 'x']],
    ['missing summary', ['step', 's1', 'done']],
    ['bad step action', ['step', 's1', 'jump']],
    ['bad artifact kind', ['artifact', 'zip', 'x']],
    ['bad plan json', ['plan'], '{nope'],
    ['bad plan shape', ['plan'], '{"steps":[]}'],
    ['unknown command', ['frobnicate']],
    ['unknown flag', ['hello', '--wat']],
    ['no command', []],
  ];
  for (const [name, args, input] of bad) {
    it(name, () => {
      const { env, outbox } = newRun();
      const r = baton(env, args, input ?? '');
      assert.equal(r.code, 2);
      assert.match(r.err, /^baton: /);
      assert.equal(outbox().length, 0);
    });
  }

  it('missing env', () => {
    const { env } = newRun();
    delete env.BATON_RUN_ID;
    const r = baton(env, ['hello']);
    assert.equal(r.code, 2);
    assert.match(r.err, /^baton: not inside a Baton run/);
  });

  it('help works without env', () => {
    const r = baton({ PATH: process.env.PATH ?? '', SystemRoot: process.env.SystemRoot ?? '' }, ['help']);
    assert.equal(r.code, 0);
    assert.match(r.out, /^usage: baton/);
    assert.equal(r.err, '');
  });
});

describe('inbox', () => {
  it('prints unread, marks read, then empty', () => {
    const { dir, env } = newRun();
    putInbox(dir, 3, 'answer', { qid: 'q_ab12cd', text: 'use postgres' });
    putInbox(dir, 4, 'message', { from: 'conductor', text: 'rebase onto main' });
    putInbox(dir, 5, 'plan_update', { steps: [{ id: 's1', title: 'One', acceptance: 'a' }, { id: 's2', title: 'Two', acceptance: 'a' }] });
    putInbox(dir, 6, 'stop', { reason: 'enough' });
    writeFileSync(join(dir, 'inbox', '000007.json.tmp'), '{');
    writeFileSync(join(dir, 'inbox', 'junk.json'), '{');
    const r = baton(env, ['inbox']);
    assert.equal(r.code, 0);
    assert.equal(r.out, [
      '[3] answer q_ab12cd: use postgres',
      '[4] message (conductor): rebase onto main',
      '[5] plan_update: s1 One; s2 Two',
      '[6] stop: enough',
    ].join('\n'));
    assert.deepEqual(readFileSync(join(dir, 'inbox', '.read'), 'utf8').split('\n').filter(Boolean), ['3', '4', '5', '6']);
    assert.equal(baton(env, ['inbox']).out, 'inbox empty');
    putInbox(dir, 8, 'resume', {});
    assert.equal(baton(env, ['inbox']).out, '[8] resume');
  });

  it('empty when no inbox dir', () => {
    const { dir, env } = newRun();
    mkdirSync(dir, { recursive: true });
    assert.equal(baton(env, ['inbox']).out, 'inbox empty');
  });
});

describe('ask --blocking', () => {
  it('times out with NO_ANSWER', () => {
    const { env, outbox } = newRun({ BATON_POLL_INTERVAL_S: '0.1' });
    const r = baton(env, ['ask', 'q?', '--blocking', '--timeout', '0.5']);
    assert.equal(r.code, 0);
    assert.equal(r.out, 'NO_ANSWER');
    assert.equal(outbox()[0].payload.blocking, true);
  });

  it('finds its answer, leaves other messages unread', async () => {
    const { dir, env, outbox } = newRun({ BATON_POLL_INTERVAL_S: '0.1' });
    const child = spawn(CMD, pre(['ask', 'which db?', '--blocking', '--timeout', '15']), { env });
    let out = '';
    child.stdout.on('data', (d) => { out += d; });
    const done = new Promise<number | null>((res) => child.on('close', res));
    await new Promise((r) => setTimeout(r, 300));
    // The question must have been spooled by now; read its qid.
    const qid = outbox()[0].payload.qid;
    putInbox(dir, 1, 'answer', { qid: 'q_other1', text: 'not mine' });
    putInbox(dir, 2, 'message', { from: 'human', text: 'hi' });
    putInbox(dir, 3, 'answer', { qid, text: 'postgres' });
    const timer = setTimeout(() => child.kill(), 12000);
    const code = await done;
    clearTimeout(timer);
    assert.equal(code, 0);
    assert.equal(out.trim(), 'postgres');
    assert.deepEqual(readFileSync(join(dir, 'inbox', '.read'), 'utf8').split('\n').filter(Boolean), ['3']);
    assert.equal(baton(env, ['inbox']).out, '[1] answer q_other1: not mine\n[2] message (human): hi');
  });

  it('sends heartbeats while waiting', () => {
    const { env, outbox } = newRun({ BATON_POLL_INTERVAL_S: '0.05', BATON_HEARTBEAT_INTERVAL_S: '0.2' });
    baton(env, ['ask', 'q?', '--blocking', '--timeout', '0.7']);
    assert.ok(outbox().filter((m) => m.type === 'heartbeat').length >= 1);
  });
});

describe('against a fake hub', () => {
  let server: Server;
  let url = '';
  let status = 200;
  let reply: object = {};
  const seen: { auth?: string; body: any }[] = [];

  before(async () => {
    server = createServer((req, res) => {
      let b = '';
      req.on('data', (d) => { b += d; });
      req.on('end', () => {
        seen.push({ auth: req.headers.authorization, body: b ? JSON.parse(b) : null });
        res.writeHead(status, { 'content-type': 'application/json' }).end(JSON.stringify(reply));
      });
    });
    await new Promise<void>((r) => server.listen(0, '127.0.0.1', r));
    url = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
  });
  after(() => new Promise<void>((r) => { server.close(() => r()); server.closeAllConnections(); }));

  it('200 prints ok #seq and does not spool', async () => {
    status = 200; reply = { ok: true, seq: 7, duplicate: false };
    const { env, outbox } = newRun({ BATON_URL: url });
    const r = await batonAsync(env, ['progress', '10', 'x']);
    assert.equal(r.code, 0);
    assert.equal(r.out, 'ok #7');
    assert.equal(outbox().length, 0);
    const last = seen.at(-1)!;
    assert.equal(last.auth, 'Bearer bt_run_x');
    assert.equal(last.body.type, 'progress');
    assert.ok(last.body.msg_id);
  });

  it('duplicate', async () => {
    status = 200; reply = { ok: true, seq: 8, duplicate: true };
    assert.equal((await batonAsync(newRun({ BATON_URL: url }).env, ['heartbeat'])).out, 'ok #8 (dup)');
  });

  it('500 spools', async () => {
    status = 500; reply = { ok: false, code: 'X', error: 'x' };
    const { env, outbox } = newRun({ BATON_URL: url });
    const r = await batonAsync(env, ['heartbeat']);
    assert.equal(r.code, 0);
    assert.match(r.out, /^spooled/);
    assert.equal(outbox().length, 1);
  });

  it('410 exits 0 without spooling', async () => {
    status = 410; reply = { ok: false, code: 'RUN_ENDED', error: 'ended' };
    const { env, outbox } = newRun({ BATON_URL: url });
    const r = await batonAsync(env, ['heartbeat']);
    assert.equal(r.code, 0);
    assert.match(r.err, /rejected: RUN_ENDED/);
    assert.equal(outbox().length, 0);
  });

  it('400 exits 2', async () => {
    status = 400; reply = { ok: false, code: 'INVALID_PAYLOAD', error: 'bad' };
    const { env } = newRun({ BATON_URL: url });
    const r = await batonAsync(env, ['heartbeat']);
    assert.equal(r.code, 2);
    assert.match(r.err, /rejected: INVALID_PAYLOAD bad/);
  });

  it('inbox falls back to the API when the run dir is missing', async () => {
    status = 200;
    reply = { ok: true, messages: [{ v: 1, n: 2, ts: 't', type: 'message', payload: { from: 'human', text: 'yo' } }] };
    const { env } = newRun({ BATON_URL: url });
    assert.equal((await batonAsync(env, ['inbox'])).out, '[2] message (human): yo');
  });
});

