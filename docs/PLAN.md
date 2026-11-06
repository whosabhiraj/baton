# Baton — Build Plan (approved)

This is the **authoritative plan**. It is the original Baton spec (not in the repo)
plus every change the user approved on 2026-11-06. **Where this file and the spec disagree,
this file wins.** Anything not mentioned here follows the spec as written.

Status of each phase lives in `HANDOFF.md`, not here.

---

## 1. What Baton is (one paragraph)

A local hub that lets one "conductor" agent (model configurable) run a fleet of coding agents (Claude Code, Codex
CLI, OpenCode, or any command) across several accounts, with a live 3D web dashboard. The
user runs `baton` inside a project folder, adjusts settings if needed, types a prompt, and the
conductor proposes a team of agents, gets approval, spawns them in isolated git worktrees,
answers their questions, reviews their diffs and merges.

## 2. Non-negotiables (unchanged from spec §0, never relax these)

1. Official CLIs only (`claude`, `codex`, ...). Baton spawns processes and reads stdout/stderr.
   It never reads, copies, proxies or forwards subscription OAuth tokens, and never calls a
   model API with a subscription credential.
2. OpenCode / third-party harnesses use API keys or non-Claude providers only. The adapter
   **refuses to start in code** if it detects a Claude subscription login in its environment.
3. Accounts are chosen explicitly, never automatically. No quota-based failover, no rotation.
   Rate limit → mark `rate_limited`, surface it, wait for a human or the conductor.
4. Local by default: bind `127.0.0.1`, bearer token for dashboard + API, per-run tokens scoped
   to one run.
5. No secrets in logs: redact key/token patterns before writing or sending any event.
6. `baton mock` exists so the dashboard is built without burning quota.

## 3. Target environment

- Must work on Windows, macOS and Linux; Node 24+ (native TypeScript type-stripping), git.
- **Everything is config-driven** and works with any number and kind of accounts.
- Machine-specific facts (installed CLIs, which accounts exist, login state) are kept in
  `HANDOFF.local.md`, which is gitignored. Never commit them (public repo).

## 4. Approved changes to the spec

### 4.1 How the user runs it (replaces spec's `baton up` / `baton conduct` split)

- `baton` (no args), run inside a project folder:
  1. Target repo = the current folder. If it is not a git repo, **ask** the user, then
     `git init` + initial commit. Never do it silently.
  2. Start the hub as a **background daemon** if one isn't running for this project.
  3. Start the **headless conductor** (owned by the hub, not the terminal).
  4. Open a terminal chat UI attached to the conductor, and open the dashboard in the browser.
- **Ctrl+C / closing the terminal only detaches.** Agents, conductor and dashboard keep running.
- `baton attach` re-attaches the terminal chat to the running conductor (history shown).
- `baton hub stop` stops the daemon (asks for confirmation if agents are live).
- The dashboard has a **conductor chat panel** that talks to the same conductor as the terminal.
- Keep `baton up` (hub + dashboard only, no conductor) and `baton mock` as in the spec.

### 4.2 Headless conductor

- The hub runs the conductor as a long-lived official CLI process on the conductor account
  (Baton MCP server attached, conductor system prompt), using stream-json input/output. Exact
  flags must be verified against `claude --help` at build time.
- **The conductor model is a setting, not fixed to Opus** (decided 2026-11-06): `conductor:
  { account, model, effort }` in config, switchable from the dashboard. Opus is only the default
  suggestion. Whether the conductor may also be a non-Claude CLI (e.g. Codex, which also
  supports MCP) is open question #9 (Phase 6).
- The conductor keeps working while the user is detached (answers agent questions, reviews,
  *proposes* merges — merges still go through approval gates, §4.6).
- Conversation history is persisted so `baton attach` can replay it.

### 4.3 Conductor proposes, user approves (team lineup)

- For each goal the conductor proposes a **lineup**: number of agents, and per agent the
  account, role, model (e.g. haiku for reading/filtering text, sonnet for coding a well-defined
  plan, opus for architecture decisions), effort level, task and plan.
- Default mode: **"propose"** — the conductor waits for the user's OK (terminal or dashboard)
  before spawning. The user can edit the lineup before approving.
- Mode is a setting, switchable live in the dashboard: `user` (the user specifies everything),
  `propose` (default), `auto` (the conductor decides within caps and reports what it chose and why).
- Always capped by: account `max_concurrent`, account `models` list, a global `max_agents`
  setting. Account choice is still explicit (the conductor passes `account` deliberately; the
  hub never picks one) — this keeps non-negotiable #3.

### 4.4 Configuration

- **Global** `~/.baton/config.yaml`: accounts, defaults. **Project** `<repo>/baton.config.yaml`:
  optional overrides (roles, routing, limits, autonomy mode, ...). Project overrides global.
- Validated with zod; errors are shown on the dashboard and CLI, never crash the hub.
- **Hot-reload**: watch both files, apply changes live.
- **Dashboard config editor**: edit account fields (enabled, max_concurrent, roles, models,
  permissions...) and write them back to the right YAML file. Every change goes to the audit log.
- **Granular per-account permissions** (opt-in, default = each CLI's normal mode):
  `permission_mode: default | acceptEdits | permissive`, `allowed_tools`, `denied_tools`,
  `extra_args`. Note: `claude -p` in default mode silently denies tool calls that need approval —
  surface this in the UI.
- **Custom adapter kinds** defined in config with no code change: command template, args, env,
  output parser (`none | stream-json | jsonl`), kind notes for the brief.
- **Configurable timeouts**: heartbeat timeout, `ask --blocking` timeout (default 15 min), poll
  interval (default 2 s) — per hub and per account.
- Verify per-account isolation env vars against current docs at build time
  (`CLAUDE_CONFIG_DIR` for claude, `CODEX_HOME` for codex, OpenCode's equivalent).

### 4.5 Protocol amendments (BATON/1)

`protocol/PROTOCOL.md` = spec §3 verbatim **plus** an "Amendments" section containing:

- **`msg_id`**: client-generated UUID on every agent message. The hub deduplicates on it, so a
  message that was spooled and also posted is stored once.
- **Separate spool file**: when the hub is unreachable, the CLI appends to
  `$BATON_RUN_DIR/outbox.jsonl` (not `events.jsonl`). The hub is the **only** writer of
  `events.jsonl`; it ingests the outbox on startup and by tailing (tracking a byte offset).
  Hand-written lines without `msg_id` are deduplicated by offset.
- **`stalled` status** (hub-generated event): heartbeat timeout while the process is still alive
  → mark `stalled`, notify conductor + dashboard. The run ends only on `done`, process exit, or
  kill — never on heartbeat timeout alone.
- `plan` payload may be `{ confirm: true }` (from `baton plan --confirm`); the hub fills in the
  conductor's steps.
- Agents sending hub-only types (`activity`, `exit`, `rate_limited`, `killed`, `paused`,
  `resumed`, `stalled`) are rejected; other unknown types are stored and forwarded.

### 4.6 Extras

- **Human approval gates** (config): merges require a dashboard/terminal click from the user in
  addition to the conductor's `confirm: true`. Optionally spawns too.
- **Cost / usage tracking**: parse token/cost fields from claude stream-json and codex `--json`;
  show per agent and per account. Display only — never used for routing.
- **Worktree cleanup policy** (config): `keep | remove_on_merge | remove_after_days: N`, plus
  `baton gc`.
- **Audit log**: append-only log of every control action (spawn, kill, pause, merge, config
  change, approvals) with actor = `conductor | human`.
- **Process-tree kill** on Windows (`taskkill /T /F`), so children of claude/codex die too.
- **Remote access is a future phase** (Phase 8), not built now.

### 4.7 Implementation decisions (made by Claude, can be revisited)

- TypeScript run **directly by Node 24's built-in type stripping** — no build step for hub/CLI.
  Consequences: no `enum`, no parameter properties, no namespaces; relative imports use `.ts`
  extensions (`allowImportingTsExtensions` + `noEmit` in tsconfig, `tsc` only for type-checking).
  The dashboard (`web/`) is built with Vite.
- `baton` CLI is installed with `npm link`; `package.json` `bin` points at a small `.js` wrapper
  that imports `cli/baton.ts`. npm's shims make it work in PowerShell, cmd and Git Bash.
- Tests use `node:test`, written in TS, cross-platform (no bash scripts).
- JSON Schemas in `protocol/schema/*.json` are **generated from zod** (`z.toJSONSchema`, zod 4),
  single source of truth in `protocol/types.ts`.
- CLI exits 0 whenever the hub is unreachable (spools instead). Exit 2 only for invalid usage
  (bad arguments), so the agent learns it called it wrong.
- Per-run tokens stored hashed (sha256) in `run.json`; revoked when the run ends.
- Dependencies (latest at plan time): `hono` 4.x, `@hono/node-server` 2.x, `zod` 4.x, `yaml` 2.x,
  `@modelcontextprotocol/sdk`, `three`, `vite`; dev: `typescript`, `@types/node`.

## 5. Repo layout (spec §1, adjusted)

```
baton/                        # repo root
  CLAUDE.md                   # points a fresh Claude at HANDOFF.md + PLAN.md
  HANDOFF.md                  # current status, how to resume (user + Claude)
  README.md
  docs/PLAN.md                # this file
  baton.config.example.yaml   # documented example; real config is ~/.baton/config.yaml
  protocol/PROTOCOL.md        # spec §3 verbatim + Amendments (§4.5)
  protocol/types.ts           # zod schemas (source of truth)
  protocol/schema/*.json      # generated
  bin/baton.js                # npm bin wrapper → cli/baton.ts
  cli/baton.ts                # agent commands + baton / attach / up / mock / login / gc / hub stop
  hub/
    daemon.ts                 # background process management, per-project hub discovery
    server.ts                 # Hono: REST + SSE + static dashboard
    mcp.ts                    # MCP server (conductor tools, spec §5)
    conductor.ts              # headless conductor process + chat history
    runs.ts                   # run lifecycle, worktrees, supervision, stalled detection
    spool.ts                  # outbox tailing + dedupe
    config.ts                 # global+project load, zod, hot-reload, write-back
    briefing.ts               # per-agent brief (spec §6)
    audit.ts, usage.ts, redact.ts
    adapters/ claude.ts codex.ts opencode.ts generic.ts custom.ts
    conductor-prompt.md
  web/                        # Vite + TS + three.js dashboard
  test/                       # node:test suites
```

Runtime data location (runs, worktrees, tokens, logs) is an **open question** (§7).

## 6. Phases (check in with the user + commit after each; never start the next without OK)

| # | Phase | Acceptance |
|---|---|---|
| 0 | **Plan + repo init** (this commit) | PLAN.md, HANDOFF.md, CLAUDE.md, spec copy committed |
| 1 | **Protocol + schemas + `baton` CLI + spool**, with a minimal hub (ingest, events, inbox, dev endpoint to register a run) | Node test plays a full agent lifecycle incl. `ask --blocking` answered via API; hub shows every event; `baton` works from PowerShell, cmd and Git Bash after `npm link` |
| 2 | **Hub core**: daemon (`baton`, `baton attach`, `baton hub stop`), runs, worktrees (+ git-init prompt), per-run tokens, SSE, redaction, audit log, stalled detection, config (global+project, zod, hot-reload, timeouts) | Kill the hub mid-run → agent keeps working → events recovered from outbox on restart, no duplicates |
| 3 | **`baton mock`**: 3 accounts, 6 fake agents with plans, questions, blocks, a rate-limit, a failure, usage numbers | Mock drives every status the dashboard must show |
| 4 | **Dashboard**: list view → 3D scene → panels → timeline/replay → config editor → approval gates → usage display → conductor chat panel | Everything in spec §7 works against the mock; list view reaches every action |
| 5 | **Adapters**: claude → codex → generic → opencode → custom kinds; granular permissions; model + effort flags; usage parsing; `baton login <account>` | One real tiny task per adapter (**uses quota — ask first**) shows reported + passive `activity` events. OpenCode refuses with a Claude subscription login present |
| 6 | **Conductor**: MCP tools, headless conductor, terminal chat, propose → approve → spawn lineup flow, model/effort guidance in the prompt | Conductor splits a small task across two accounts with explicit accounts, personalised plans, a question answered, diff reviewed, merge approved by the user (**uses quota — ask first**) |
| 7 | **Hardening**: `ACCOUNT_REQUIRED/BUSY/DISABLED`, `ROLE_NOT_ALLOWED`, heartbeat/stalled, redaction tests, OpenCode refusal test, worktree cleanup + `baton gc` | All error paths covered by tests |
| 8 | **Future — remote access**: Cloudflare Tunnel + Access, HTTPS-only cookie auth | Only when the user asks |

Definition of done (spec §8, adjusted): running `baton` in a project starts hub + headless
conductor + terminal chat + dashboard; the user can spawn from the conductor or the dashboard,
pick accounts, approve lineups and merges, watch plans/progress/questions/activity live in 3D
(list view as fallback), detach with Ctrl+C and re-attach with `baton attach`.

## 7. Open questions — ask the user, don't guess

Ask each at the start of the phase that needs it:

1. **(Phase 1/2)** Where runtime data lives: inside the project (`<repo>/.baton/` — runs,
   worktrees; added to `.git/info/exclude`) or in the user's home
   (`~/.baton/projects/<name>/`) so the project folder stays clean.
2. **(Phase 2)** Default `max_agents` cap and default worktree cleanup policy.
3. **(Phase 4)** Approval gates default: merges need a human click by default? Spawns too?
4. **(Phase 5)** Which OpenCode provider / API key the user wants (or leave OpenCode disabled).
5. **(Phase 5)** Which accounts opt in to which permission mode.
6. **(Phase 5)** User must log in to Codex (`baton login codex-1`) and to each Claude account's
   isolated config dir before real tasks run. Confirm account ids/labels/colors for the config.
7. **(Phase 5)** Verify at build time, don't assume: `claude` flags for effort, stream-json
   input, MCP config, permission modes; `codex exec --json` event shape, `CODEX_HOME`, effort
   setting (`model_reasoning_effort`); OpenCode config dir variable.
8. **Before any step that spends quota**, ask first.
9. **(Phase 6)** Conductor kind: Claude Code only, or any MCP-capable CLI (Codex, OpenCode)? The
   model is configurable either way.
