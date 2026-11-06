# BATON/1 protocol

> Sections 3.x below are copied verbatim from the original Baton spec (§3).
> Where they conflict with the **Amendments** at the end of this file, the Amendments win.

## 3. The protocol: BATON/1

One protocol for every agent. It has three layers, so an agent with any capability level can take part:

- **Layer A, the `baton` CLI** (preferred). Any agent that can run a shell command can report and receive messages.
- **Layer B, file spool** (fallback). If the CLI can't reach the hub, it appends to `$BATON_RUN_DIR/events.jsonl`. The hub tails that file. An agent with only file access can write the same JSON lines by hand.
- **Layer C, passive telemetry** (automatic). Adapters translate native output into `activity` events. Agents don't need to do anything for this.

### 3.1 Envelope

Every message is one JSON object per line:

```json
{
  "v": 1,
  "run_id": "r_01J...",
  "agent_id": "cc-b-0007",
  "seq": 42,
  "ts": "2026-11-06T14:12:09.123Z",
  "type": "step_done",
  "payload": { }
}
```

`seq` is per-run, strictly increasing; the hub assigns it if the sender omits it. Unknown `type` values must be stored and forwarded, never rejected (forward compatibility).

### 3.2 Environment given to every agent

| Var | Meaning |
|---|---|
| `BATON_URL` | hub base URL (e.g. `http://127.0.0.1:7777`) |
| `BATON_TOKEN` | per-run bearer token; can only post/read for this run |
| `BATON_RUN_ID` | run id |
| `BATON_AGENT_ID` | e.g. `cc-b-0007` |
| `BATON_RUN_DIR` | `runs/<run_id>/` (spool + inbox) |
| `BATON_BRIEF` | path to `BATON_BRIEF.md` |

`BATON_TOKEN` is a Baton credential for this one run. It is not, and must never be derived from, any Anthropic/OpenAI credential. Revoke it when the run ends.

### 3.3 Message types

Agent → hub:

| type | payload | when |
|---|---|---|
| `hello` | `{ model, cwd, brief_ack: true }` | first thing, after reading the brief |
| `plan` | `{ steps: [{ id, title, acceptance }] }` | after reading the brief; may revise the conductor's plan, with a `reason` |
| `step_start` | `{ step_id, note? }` | beginning a step |
| `step_done` | `{ step_id, summary, evidence? }` | acceptance met; `evidence` = test output, file, commit |
| `step_blocked` | `{ step_id, reason }` | cannot continue, no question to ask |
| `progress` | `{ percent: 0-100, note }` | at least every ~5 minutes of real work |
| `artifact` | `{ kind: "file"\|"diff"\|"commit"\|"url"\|"log", ref, note? }` | something the conductor should look at |
| `question` | `{ qid, text, options?, blocking: bool }` | needs a decision from the conductor |
| `heartbeat` | `{}` | automatic from the CLI every 30s while a command runs; hand-send if idle >60s |
| `done` | `{ status: "success"\|"partial"\|"failed", summary, followups? }` | last message from the agent |
| `error` | `{ message, fatal: bool }` | something broke |

Hub-generated (agents never send these): `activity` (tool call, file edit, shell command, from adapters), `exit` (`{ code, signal, diff_stat, duration_s }`), `rate_limited` (`{ detail }`), `killed`, `paused`, `resumed`.

Hub → agent (delivered to `$BATON_RUN_DIR/inbox/<n>.json`, shown by `baton inbox`):

| type | payload |
|---|---|
| `message` | `{ from: "conductor"\|"human", text }` |
| `answer` | `{ qid, text }` |
| `plan_update` | `{ steps }`, a revised plan from the conductor |
| `pause` / `resume` / `stop` | `{ reason? }`; `stop` means finish the current edit, commit, send `done`, exit |

### 3.4 The `baton` CLI (what agents call)

```
baton hello
baton plan < plan.json                    # or: baton plan --file plan.json
baton plan --confirm                      # accept the conductor's plan unchanged
baton step <id> start [--note ".."]
baton step <id> done  --summary ".." [--evidence <ref>]
baton step <id> blocked --reason ".."
baton progress <0-100> "note"
baton artifact <file|diff|commit|url|log> <ref> [--note ".."]
baton ask "question" [--options a,b,c] [--blocking]   # blocking: waits, prints the answer to stdout
baton inbox                               # prints unread hub→agent messages, marks them read
baton heartbeat
baton done --status success|partial|failed --summary ".." [--followups ".."]
baton error "message" [--fatal]
```

Rules for the CLI:
- Exit 0 even when the hub is unreachable (spool to `events.jsonl` instead); an agent must never fail its real work because reporting failed.
- `baton ask --blocking` polls the inbox every 2s, times out after 15 minutes, then prints `NO_ANSWER` and exits 0. The agent then proceeds with its best-judgement default and says so in the next `progress`.
- Output is terse (one line) so it doesn't waste agent context.

### 3.5 Agent behaviour rules (copied into every brief)

1. Read the brief. Run `baton hello`.
2. Send `plan` (confirm or revise the conductor's plan). Revise only with a reason.
3. For each step: `step start` → work → verify against the acceptance criterion → `step done` with evidence.
4. Run `baton inbox` at every step boundary and before any destructive action.
5. If a decision is needed and it isn't covered by the brief, `baton ask`. Don't guess on anything marked `ask_before` in the brief.
6. Stay inside the worktree and the allowed paths. Commit to your branch with small, descriptive commits.
7. Finish with `baton done`. If you stop for any reason, say so with `done --status partial` or `error --fatal`.
8. Never print, store or transmit credentials. Never read other agents' run dirs or worktrees.

### 3.6 Identity

- `agent_id`: `{kind}-{account}-{nnnn}`, e.g. `cc-b-0007` (Claude Code, account b), `cx-1-0003` (Codex, account 1), `oc-k-0001` (OpenCode, key k).
- `callsign`: a short readable name from a word list (Vesper, Ratchet, Lumen, …), unique among live agents. Shown on the dashboard and used by the conductor in conversation.
- Each agent also has `account_id`, `role`, `model`, `color` (inherited from the account), `branch`, `worktree`.
- The hub is the only source of identity. Agents state their `agent_id` in messages but the hub checks it against the run token.


---

## Amendments (approved 2026-11-06; these override §3 above)

Machine-readable schemas: the JSON Schema files `protocol/schema/*.json` are the master copy
(edit them directly; dashboard types are generated from them). Shared test
fixtures live in `protocol/fixtures/` (`valid/`, `invalid/`, `ingest/`, `outbox/`, `redact/`);
the Go conformance tests validate every fixture against the schemas. A protocol change means:
edit the schema file, add fixtures, update this document.

### A1. `msg_id` and deduplication

Every agent message carries `msg_id`, a client-generated UUID (the `baton` CLI always sets it).
The hub stores a message **once per `msg_id`** per run: a message that was spooled *and* posted
is stored once. A repeated `msg_id` is acknowledged (`duplicate: true`) and not stored again.

### A2. `seq` is assigned by the hub

The hub is the only writer of `events.jsonl` and always assigns `seq` (per run, strictly
increasing, starting at 1). A sender-supplied `seq` is kept as `client_seq` for reference.
Stored events also carry `msg_id` and `src` (`cli | spool | hub | adapter`).

### A3. Separate spool file (replaces Layer B's use of `events.jsonl`)

When the hub is unreachable, the CLI appends the envelope (one JSON line, with `msg_id` and
`ts`) to **`$BATON_RUN_DIR/outbox.jsonl`**. Agents writing by hand append to the same file.
Agents never write `events.jsonl`.

The hub ingests `outbox.jsonl` on startup (before it starts listening) and then by tailing it.
It tracks how far it has read as a byte offset in `$BATON_RUN_DIR/outbox.offset`, and only
consumes complete lines (ending in `\n`). A line without `msg_id` gets
`msg_id = "spool@<byteOffset>"`, so re-reading the same bytes never duplicates it. Lines that are
not valid JSON, or whose `run_id`/`agent_id` don't match the run, are skipped and reported as a
hub warning (not stored as events).

"Unreachable" for the CLI means: connection refused/reset, DNS failure, timeout (3 s), or HTTP 5xx.

### A4. `stalled` (hub-generated)

If no event arrives from a run for `heartbeat_timeout_s` while its process is still alive, the
hub emits `stalled { last_seen, timeout_s }` and sets the status to `stalled`; the next agent
event clears it. **A run never ends on heartbeat timeout alone** — only on `done`, process exit
or kill.

### A5. `plan --confirm`

`baton plan --confirm` sends `plan { confirm: true }`. The hub stores it with the conductor's
steps filled in: `{ confirm: true, steps: [...] }`.

### A6. Hub-only types are rejected

Agent messages with type `activity`, `exit`, `rate_limited`, `killed`, `paused`, `resumed`,
`stalled` or `to_agent` are rejected (`HUB_ONLY_TYPE`). Other unknown types are stored and
forwarded unchanged. Known types have their payload validated (`INVALID_PAYLOAD` on failure).

### A7. Inbox files and read state

Hub → agent messages are files `$BATON_RUN_DIR/inbox/<n>.json`, `n` = 1, 2, 3 … zero-padded to
6 digits (`000001.json`), body = `{ v: 1, n, ts, type, payload }`. The hub writes each file
atomically (temp file + rename) and also records the delivery in the run's event log as a
hub-generated `to_agent { n, type, payload }` event, so the dashboard sees both directions.

Read state is kept by the agent side only: the CLI appends each consumed `n` (one per line) to
`$BATON_RUN_DIR/inbox/.read`. `baton inbox` prints and marks all unread messages.
`baton ask --blocking` consumes **only** the `answer` whose `qid` matches; other messages stay
unread for the next `baton inbox`.

### A8. Timeouts and polling

`baton ask --blocking` defaults: poll every 2 s, give up after 900 s (prints `NO_ANSWER`, exit 0).
Overridable per call (`--timeout <s>`) or by the hub via env `BATON_ASK_TIMEOUT_S` and
`BATON_POLL_INTERVAL_S`. While blocked it sends a `heartbeat` every 30 s
(`BATON_HEARTBEAT_INTERVAL_S`).

### A9. CLI exit codes and output

- `0`: success, **or** the hub was unreachable and the message was spooled, **or** the hub
  rejected the message for a non-usage reason (e.g. revoked token). The agent's real work must
  never fail because reporting failed.
- `2`: invalid usage (bad/missing arguments, invalid JSON for `plan`, not inside a Baton run).
- Output is one terse line, e.g. `ok #12`, `spooled (hub unreachable)`, `qid q_ab12cd`.

### A10. Per-run tokens

Tokens look like `bt_run_<base64url>`; the hub stores only their sha256 in `run.json`. A token
can only post to `/api/ingest` for its own run and read its own run's inbox and events. It is
revoked when the run ends (in Phase 1: when `done` is ingested).

### A11. Hub HTTP API (BATON/1, local)

All routes are under the hub base URL (`BATON_URL`, default `http://127.0.0.1:7777`).
Auth is `Authorization: Bearer <token>`. **Admin token** = the hub's dashboard/API token.
**Run token** = a per-run token (A10). Errors are JSON `{ ok: false, code, error }`.

| Route | Auth | Body / query | Response |
|---|---|---|---|
| `GET /api/health` | none | — | `{ ok: true, name: "baton", v: 1 }` |
| `POST /api/ingest` | run | envelope (A1–A6) | `200 { ok: true, seq, duplicate }`; `400 INVALID_ENVELOPE / INVALID_PAYLOAD / HUB_ONLY_TYPE`; `401 UNAUTHORIZED`; `403 RUN_MISMATCH / AGENT_MISMATCH`; `410 RUN_ENDED` |
| `GET /api/runs/:id/inbox?after=<n>` | run (own) or admin | — | `{ ok: true, messages: InboxMessage[] }` (all with `n > after`; stateless) |
| `GET /api/runs/:id/events?since=<seq>` | run (own) or admin | — | `{ ok: true, events: StoredEvent[] }` (`seq > since`) |
| `GET /api/state` | admin | — | `{ ok: true, runs: RunSummary[] }` (run record minus token hash, plus `last_seq`, `last_event_ts`, `open_questions`, `step_states`) |
| `GET /api/stream` | admin (header, or `?token=` for EventSource) | `Last-Event-ID` | SSE; each event `id: <global n>`, `event: baton`, `data: StoredEvent`. If the id is older than the in-memory buffer, the hub sends `event: reset` and the client re-fetches `/api/state`. |
| `POST /api/runs/:id/message` | admin | `{ text, from? }` (`from` default `human`) | `{ ok: true, n }` |
| `POST /api/runs/:id/answer` | admin | `{ qid, text }` | `{ ok: true, n }`; `409 UNKNOWN_QID` if `qid` is not an open question of the run (A13) |
| `POST /api/runs/:id/plan_update` | admin | `{ steps }` | `{ ok: true, n }` |
| `POST /api/runs/:id/pause` · `resume` · `stop` | admin | `{ reason? }` | `{ ok: true, n }` |
| `POST /api/dev/runs` | admin, dev mode only | `{ agent_id?, callsign?, role?, kind?, model?, account_id?, plan? }` | `{ ok: true, run_id, agent_id, token, run_dir, env }` — `env` is the full `BATON_*` set for the agent |

`run_id` format: `r_` + 26-char ULID-like id (time-sortable). The hub never logs tokens.

### A12. Instance id, SSE hello, hub shutdown (approved 2026-11-06)

- Every SSE stream (`GET /api/stream` and any later stream) starts with
  `event: hello` / `data: {"instance":"<id>"}`, sent before any `baton` event. `instance` is a
  random id of the running hub process; clients compare it across reconnects to notice a hub restart.
- `GET /api/health` additionally returns `instance`: `{ ok: true, name: "baton", v: 1, instance }`.
- `POST /api/hub/shutdown` (admin) answers `{ ok: true }` and then shuts the hub down gracefully.
- Phase 6 will add `GET /api/wait` and `/api/conductor/*`.

### A13. Answers must match an open question

`POST /api/runs/:id/answer` is refused with `409 UNKNOWN_QID` unless `qid` belongs to a
`question` of that run that has not been answered yet. A delivered answer closes the question,
so a second answer to the same `qid` is refused too. This stops a mistyped qid from being
accepted while the agent keeps waiting (`ask --blocking` only consumes the matching answer).

### A14. Phase 2 API: spawn, accounts, config, audit, hub notices

All routes below are **admin**-auth. Mutating admin routes read the caller from the header
`X-Baton-Actor: human|conductor` (default `human`; anything else is `400 INVALID_PAYLOAD`). The actor
is recorded in the audit log. Every authenticated admin request (not `/api/health`) counts as activity
for idle stop.

| Route | Body / query | Response |
|---|---|---|
| `POST /api/spawn` | `SpawnRequest`: `{ account?, role, kind?, model?, effort?, callsign?, task, plan?, scope?{paths,forbidden,ask_before}, base_branch?, dry_run?, actor? }` (unknown fields refused; `actor` in the body overrides the header) | `200` `api.spawn`: `{ run_id, agent_id, callsign, account_id, role, kind, model, branch, base_branch, worktree, run_dir, brief?, env?, dry_run }`. `env` = the full `BATON_*` set incl. the run token (omitted for dry runs, which create nothing and return the rendered `brief`). Errors: `400 INVALID_PAYLOAD / ACCOUNT_REQUIRED / UNKNOWN_ACCOUNT`; `409 ACCOUNT_DISABLED / ROLE_NOT_ALLOWED / MODEL_NOT_ALLOWED / ACCOUNT_BUSY / MAX_AGENTS / CALLSIGN_TAKEN / NOT_A_REPO` (also when the hub was started without a git repo); `500 GIT_ERROR`. |
| `GET /api/accounts` | — | bare JSON array (`api.accounts`) of `{ id, label, kind, color, enabled, max_concurrent, used, roles[], models[], rate_limited, permissions_mode }`. `used` = live (not ended) runs; `rate_limited` = any live run has status `rate_limited`; `permissions_mode` defaults to `default`. |
| `PATCH /api/accounts/:id` | `{ <account field>: value, ... }` (YAML key names, e.g. `max_concurrent`, `enabled`, `roles`) | `200` the updated account (one `api.accounts` element). Fields are applied in alphabetical order, each written back to the YAML file that defines the account (comments kept), then the config is reloaded. `404 NOT_FOUND` unknown account; `400 CONFIG_INVALID` `{ ok:false, code, error, issues:[string] }` if a value or field is refused (fields before it stay applied). |
| `GET /api/config` | — | `200` `api.config`: `{ config, issues[{file,path,line?,msg}], global_path, project_path, version, ok }`. `ok:false` = the files currently have errors; `config` is then the last good one. |
| `PATCH /api/config` | `{ "mode": "auto", "worktrees.cleanup": "keep", ... }` (dotted hub keys) | `200` same as `GET /api/config`; `400 CONFIG_INVALID` as above. |
| `GET /api/audit?limit=N` | `limit` default 100, `0` = all | `{ entries: [{ ts, actor, action, target?, detail? }] }`, oldest first. |
| `GET /api/state` | — | additionally: `accounts` (as `/api/accounts`), `mode` (hub.mode), `config_ok`, `config_issues[]`, `instance` (as in health). |

**Audit actions** (`actor` = header, or `hub`): `spawn` (target run id; detail `account, role, model,
run_id, dry_run`), `config.account` (target account id; detail `field, value, file`), `config.hub`
(target `hub`; detail `field, value, file`), `deliver.message|answer|plan_update|pause|resume|stop`
(target run id; detail `n`), `hub.shutdown`, `dev.run` (target run id).

**SSE hub notices.** Besides `event: baton`, `GET /api/stream` sends `event: hub` frames with the same
global `id:` sequence and replay buffer. `data` is a JSON object with a `type` (schema `api.notice`):

| `type` | Fields | When |
|---|---|---|
| `config_reloaded` | `version, ok, issues[]` | a config file changed on disk (or was edited through the API); `ok:false` = the new content has errors and the last good config stays in effect |
| `spool_warning` | `run_id, code, detail` | the hub skipped or repaired a bad line in a run's outbox (e.g. `INVALID_PAYLOAD`, `SPOOL_UTF16`, `SPOOL_TRUNCATED`) |
| `idle_stop` | `after_min` (number) | the hub is about to stop itself (see below) |

**Live configuration.** The hub polls the global and project config files (default every 2 s). A
broken file never stops the hub: the last good config stays in effect and `config_reloaded` carries
`ok:false`. The stalled detector reads `hub.heartbeat_timeout_s` on every tick (an account's own
`heartbeat_timeout_s` wins for its runs).

**Idle stop.** The hub stops itself when it has had no live (not ended) run, no SSE subscriber and
no admin API request for `hub.idle_stop_min` minutes (`0` = never; re-read live). It sends the
`idle_stop` notice, logs, and shuts down gracefully.

**Run record.** `run.json` may carry extra top-level fields written by the hub (e.g. `"merged": true`);
readers must ignore unknown fields (already required by A-rules for run records).

### A15. Phase 3: usage, conductor state, approvals, kill

**`usage` event** (hub-only, schema `hub.usage`): `{ input_tokens, output_tokens, cache_read_tokens?,
cache_write_tokens?, cost_usd?, model? }` (integers >= 0, cost number >= 0). It carries the run's
**cumulative** totals; the latest event wins. `RunSummary.usage` holds the latest payload (omitted
if none). Each element of `GET /api/accounts` (and `/api/state` `accounts`) gets `usage`: the sum over
the account's runs of each run's latest usage (`input_tokens, output_tokens, cache_read_tokens,
cache_write_tokens, cost_usd`; zeros when none).

**Additions to `GET /api/state`**: `conductor` = `{ state: off|idle|thinking|acting, account?, model?,
since }` (default `{state:"off"}`), and `approvals` = the pending approvals.

| Route (admin) | Body / query | Response |
|---|---|---|
| `GET /api/approvals` | `?status=pending\|approved\|rejected` (default all) | bare array of approvals, oldest first |
| `POST /api/approvals/:id/approve` and `/reject` | `{ note? }` | `200` the approval; `404 NOT_FOUND`; `409 ALREADY_DECIDED` |
| `POST /api/runs/:id/kill` | — | `200 { ok:true }`; `409 NOT_SUPPORTED` (no launcher); `404 NOT_FOUND`; `409 RUN_ENDED` (note: 409, not 410, on this route) |

Approval: `{ id ("ap_"+short id), kind: merge|spawn, run_id?, title, detail?, requested_by, status:
pending|approved|rejected, created_at, decided_at?, decided_by?, note? }`, stored in
`<dataDir>/approvals.json`. The decider is the `X-Baton-Actor` header (default `human`).

**Spawn** with a launcher: after a successful non-dry spawn the hub starts the agent; if that fails the
run gets a hub `exit` with `code:-1` and the response is `500 LAUNCH_FAILED` (`{ ok:false, code, error,
run_id }`).

**SSE hub notices** (extends A14): `conductor` (`state, account?, model?, since`) when the conductor
state changes; `approval` (`approval`) when an approval is requested or decided.

**Audit actions**: `approval.request|approve|reject` (target approval id), `kill` (target run id).

### A16. Phase 6: conductor token, run routes, diff, merge, wait

**Conductor token.** The hub mints one per conductor session (`bt_cond_` + random; held in memory,
a new session or a hub restart invalidates the old one) and passes it to `baton mcp` as
`BATON_CONDUCTOR_TOKEN`. It is accepted on the routes marked *C* below and on every existing
read/control route the conductor's tools use: `GET /api/state`, `/api/accounts`, `/api/config`,
`/api/approvals`, `/api/audit`, `GET /api/runs/:id/events`, `POST /api/runs/:id/{message,answer,
plan_update,pause,resume,stop,kill}`, `POST /api/spawn`. It is **refused** (`401`) on approval
decisions, `/api/stream`, `/api/hub/shutdown`, `/api/dev/runs`, token rotation. With it,
`PATCH /api/config` may only change `mode` and `PATCH /api/accounts/:id` only `enabled`
(`403 FORBIDDEN` otherwise). The actor of a conductor request is always `conductor` (audit, spawn
`actor`, message `from`), whatever the header or body says.

`:id` on run routes may be a run id or an agent id (the agent's newest run).

| Route | Body / query | Response |
|---|---|---|
| `GET /api/runs` *C* | `?status=a,b` `?active=true` | `{ ok, runs: RunSummary[] }` |
| `GET /api/runs/:id` *C* | `?events=N` (default 10, max 500) | `{ ok, run: RunSummary, task, effort, worktree, actor, recent_events (newest N without activity/heartbeat/usage/to_agent), artifacts (artifact payloads), diff_stat, approvals (pending, this run) }` |
| `GET /api/runs/:id/events` | adds `?types=a,b` and `?limit=N` (newest N after filtering) | as A11 |
| `GET /api/runs/:id/diff` *C* | `?max_bytes=N` (default 200000) | `{ ok, diff: { run_id, agent_id, branch, base_branch, stat, diff, bytes, truncated, uncommitted[], merged } }`; committed work only (`base...branch`) |
| `POST /api/runs/:id/merge` *C* | `{ confirm: true, note? }` | see below |
| `GET /api/wait` *C* | `?cursor=N&instance=I&timeout_s=S&types=a,b` | `{ ok, cursor, instance, items[], timed_out, reset }` |

**Merge.** `400 CONFIRM_REQUIRED` without `confirm:true`; `409 RUN_ACTIVE` unless the run reported
`done` or ended. A conductor request with `approvals.merge` on opens a merge approval (or returns the
pending one): `202 { ok, status:"pending", approval }`. A human request, or the gate off, merges now:
`200 { ok, status:"merged", merge: { run_id, branch, base_branch, commit, already_merged, method } }`;
already merged: `200 { ok, status:"merged", already_merged:true }`. Approving a merge approval runs the
merge first; if it fails the approval **stays pending** and the approve call answers the error.
Merge rules: always a merge commit (`--no-ff`); refused with `409 MERGE_BLOCKED` when the agent's
worktree has uncommitted changes, the base is not a local branch, the base is checked out in another
worktree, or the base is checked out in the project folder and that checkout has uncommitted changes
to tracked files; refused with `409 MERGE_CONFLICT` (files listed, nothing left behind) when it does
not merge cleanly. A base not checked out anywhere is merged without a checkout (`merge-tree` +
`commit-tree` + `update-ref`, `method:"ref"`). The run gets `merged: true` (`RunSummary.merged`;
run.json extras `merged`, `merge_commit`).

**Wait.** Long-poll over the SSE ring (A12 ids). Without `cursor` it starts at "now". It returns as
soon as at least one matching item is after the cursor (plus a 250 ms burst, at most 50 items), or
after `timeout_s` (default 60, max 600; 0 = do not block) with `timed_out:true`. `cursor` advances
past every item looked at, matching or not. Default types: events `question, step_blocked, done,
error, stalled, rate_limited, exit` and notices `approval` (decided only) and `merged`. Item:
`{ n, kind: event|notice, type, run_id?, agent_id?, callsign?, seq?, ts?, payload }`. `reset:true`
(empty items): the cursor is unknown to this hub process (other `instance`, ahead of or older than
the ring, or the waiter fell behind): re-read the run list.

**SSE notice** `merged` (`run_id, agent_id, commit?`). **Audit action** `merge` (target run id).
**Error codes**: `FORBIDDEN` 403, `CONFIRM_REQUIRED` 400, `RUN_ACTIVE`, `MERGE_CONFLICT`,
`MERGE_BLOCKED` 409.

**`baton mcp [--project P] [--url U]`**: MCP stdio server (official Go SDK). Tools: `list_accounts,
spawn_agent (account required), list_agents, get_agent, read_events, wait_for_events, send_message,
answer_question, update_plan, pause_agent, resume_agent, stop_agent, kill_agent, review_diff,
merge_agent, list_approvals, set_mode, set_account_enabled`. Each is one REST call above; results are
the hub's JSON as text, hub errors come back as tool errors (`CODE: message`). It keeps the wait
cursor itself, pinned at its first tool call, so nothing is missed between calls.
