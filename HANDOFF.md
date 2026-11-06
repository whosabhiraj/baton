# HANDOFF — read this first

Last updated: **2026-11-07**, **Phase 6a done** (conductor REST routes + `baton mcp`). Phase 5's real codex
run still waits for `baton login codex-1`.

Big change since Phase 0: the hub **and** the `baton` CLI are written in **Go** (one binary).
`docs/GO_HUB_DESIGN.md` is approved and overrides `docs/PLAN.md` §3, §4.7, §5 and §6.

---

## A. For the user: how to pick this back up

1. Open a terminal in the repo folder and run `claude`.
2. Paste this as your first message:
   > Read CLAUDE.md and HANDOFF.md and follow them. Ask me any open questions before writing code.

Commands that work today (need Go, Node 24+ and git; `npm install` once):

| Command | What it does |
|---|---|
| `npm run check` | gofmt, vet, dashboard types in sync + typecheck, every Go test, then a full build |
| `npm run build` | dashboard + binary -> `dist/baton` (`.exe` on Windows), version-stamped |
| `npm run install:baton` | same, installed to Go's bin folder (on PATH in a new terminal) |
| `baton up` | in a repo: starts (or reuses) the background hub, prints URL + admin token, opens the dashboard |
| `baton hub status` / `baton hub stop` | hub state / stop it (asks; `--keep-agents`) |
| `baton login <account>` / `baton login --status` | log an account's CLI in, inside its own config dir |
| `baton mock [--speed N] [--seed N] [--once] [--reset]` | demo hub with a fake conductor and fake agents (`~/.baton/mock`), no quota |
| `npm run dev:web` | dashboard dev server with hot reload; set `BATON_HUB_URL` to a running hub (e.g. the mock) and open it with `#token=<admin token>` |
| `baton mcp` | conductor MCP stdio server (started by the hub in 6b; needs `BATON_CONDUCTOR_TOKEN`) |
| `baton help` | agent commands (`hello`, `plan`, `step`, `progress`, `ask`, `inbox`, `done`, ...) |

Things only you can do:
- Log in each account once: `baton login claude-a`, `baton login claude-b`, `baton login codex-1`
  (config is `~/.baton/config.yaml`; each account has its own dir under `~/.baton/accounts/`).
- **CI:** GitHub refused `.github/workflows/ci.yml` because the git token has no `workflow`
  scope. Give the token that scope or add the file through the GitHub web UI (it exists
  locally, untracked).
- OpenCode: stays disabled until you pick an API-key provider (never a Claude subscription).

## B. For Claude (fresh session, zero context): how to resume

1. Read, in order: this file → `PROGRESS.local.md` if it exists (local live log, may be newer) →
   `HANDOFF.local.md` if it exists (machine facts, not committed) → `docs/GO_HUB_DESIGN.md`
   (§0 = decisions) → `docs/PLAN.md` → the original spec, if present locally
   (`docs/BATON_BUILD_PROMPT.local.md`; not in the repo).
2. If §E lists unanswered open questions, ask the user those first (before any code).
3. Check state: `git log --oneline`, `git status`. If the tables here disagree with git, trust
   git and tell the user.
4. **Working rules:**
   - Ask, don't assume (AskUserQuestion). Propose improvements, implement after approval.
   - Phase by phase; ask before starting the next phase.
   - **Commit (and push) progress continuously** — stable, tested pieces; scan for personal
     info first (public repo).
   - Ask before anything that spends model quota (real claude/codex runs).
   - Prefer settings over hard-coding. Explain choices in plain language.
   - Update `PROGRESS.local.md` after every meaningful step; this file at the end of every phase.
   - Never commit personal material (the user's messages, quotes of them, working notes,
     machine details) — keep it in `*.local.md` (gitignored). More working preferences: HANDOFF.local.md.
5. Go may not be on PATH in old shells: prepend Go's bin dir (see HANDOFF.local.md).

## C. Phase status

| Phase | Status | Commits |
|---|---|---|
| 0 Plan + repo init | ✅ done | `2a5a98d` |
| 1 Protocol + CLI + spool + minimal hub (Go) | ✅ done | up to the "Phase 1 done" commit |
| 2 Hub core | ✅ done (verified end to end; PowerShell piped-`y` fix `84b18d2`) | `73491a4` … `84b18d2` |
| 3 `baton mock` | ✅ done (controls, approvals, kill checked by hand) | `7e40608` … |
| 4 Dashboard | ✅ done: list + panels, 3D rail-yard map, timeline + replay, phone layout; design iterated live with the user | `4b771b3` … `20f9bfe` |
| 5 Adapters | code done: supervisor, env allowlist, claude (real run ok), codex, opencode + refusal, custom kinds, `baton login`; real codex run pending login | `852719b` … `72e99ec` |
| 6 Conductor | 6a done: conductor token, run/diff/merge/wait routes, real merges, `baton mcp` (18 tools). Next 6b (headless conductor), 6c (terminal chat in Bubble Tea + dashboard chat), 6d (lineup flow + real e2e) | `12da74f` |
| 7 Hardening | pending | — |
| 8 Remote access (future) | not planned yet | — |

## D. What exists right now

```
cmd/baton/              main -> internal/cli.Main
internal/proto          envelope/payload types, JSON-Schema validation (embedded schemas)
internal/store          per-run store: seq, msg_id dedupe, recovery, status, inbox, AppendAdapter
internal/spool          line tailer with byte offsets (agent outbox.jsonl, adapter stdout.jsonl)
internal/api            HTTP API (PROTOCOL.md A11-A15) + serves the embedded dashboard
internal/hub            wiring, OS lock, port fallback, admin token, stalled detection
internal/daemon         background hub: discovery, start/stop
internal/runs           spawn rules, worktrees (git CLI), briefs, diff stat
internal/config         global + project YAML, validation, hot reload, comment-preserving writes
internal/approvals      merge/spawn approvals;  internal/audit  audit log
internal/proc           agent env from an allowlist (+ account env, BATON_*)
internal/supervise      `baton __supervise`: detached per-agent supervisor, tree kill, exit.json
internal/adapters       claude, codex, opencode, config-defined kinds (generic); stdout parsers
internal/launch         real Launcher: start, stdout -> activity/usage, exit + diff stat, kill,
                        adoption after hub restart, preflight (OpenCode/Claude-subscription refusal)
internal/mock           `baton mock`: fake conductor + agents through the real hub
internal/mcpserver      `baton mcp`: the conductor's MCP stdio server, tools = thin REST calls (A16)
internal/cli            agent commands, baton up/hub/login/mock, help
internal/{ids,paths,redact,gitx,logx,bus,briefing}  helpers
protocol/               PROTOCOL.md (BATON/1 + A1-A15), schema/*.json (source of truth), fixtures/
web/                    dashboard: Vite + TS + three.js, no framework; src/protocol.gen.ts is
                        generated (`npm run gen:types`); built into web/dist and embedded
e2e/                    lifecycle, CLI parity, crash recovery (hard-killed hub)
scripts/*.mjs           build / build-web / gen-types / install / test / check
docs/                   PLAN.md, GO_HUB_DESIGN.md
```

Runtime data: `~/.baton/projects/<repo-name>-<hash8>/` (override with `BATON_HOME`): `admin.token`,
`hub.lock`, `hub.port`, `worktrees/`, `runs/<run_id>/{run.json, events.jsonl, outbox.jsonl, inbox/,
brief.md, stdout.jsonl, stderr.log, supervisor.json, exit.json}`. Global config:
`~/.baton/config.yaml`; per project: `<repo>/baton.config.yaml`.

## E. Resume here (Phase 6: 6a done, next 6b)

Phase 6 answers (2026-11-07): conductor = Claude Code only, built so other kinds can be added;
default conductor `claude-a` / opus / high, configurable; split 6a/6b/6c/6d with a check-in after
each; terminal chat uses **Bubble Tea** (overrides the "plain REPL first" line in GO_HUB_DESIGN);
REST + SSE stays (no WebSockets). 6a answers: merges are `--no-ff` merge commits, conflicts are
refused (the conductor sends the agent back); a dirty checkout of the base refuses the merge and the
approval stays pending; the conductor token may switch `hub.mode` and `account.enabled` but never
decide approvals or change other config.

Done (6a, PROTOCOL.md A16): conductor session token, `GET /api/runs`, `GET /api/runs/:id`,
`GET /api/runs/:id/diff`, `POST /api/runs/:id/merge`, `GET /api/wait` (long-poll with cursor),
real merges on approval, `baton mcp` with 18 tools.

Next (6b): headless conductor owned by the hub — `claude` with stream-json in/out on the conductor
account, `--mcp-config` pointing at `baton mcp` with a freshly minted token, conductor system prompt
file, `history.jsonl`, restart with `--resume`, conductor state notices. Verify flags against
`claude --help` first; any real run uses quota (**ask first**).

Phase 5 leftover: user logs in (`baton login claude-a|claude-b|codex-1`), then one tiny real codex
run (**quota, ask first**) to capture its `--json` stream and check effort/sandbox flags and cached
token accounting. OpenCode stays disabled until an API-key provider is chosen.

API surface for the dashboard (PROTOCOL.md A11–A15):
- `GET /api/state` (runs with status, plan, step_states, open_questions, usage; accounts with
  used/max, rate_limited, usage totals; conductor state; pending approvals; config_ok/issues)
  and `GET /api/stream` (SSE: `event: baton` = stored events, `event: hub` = notices
  `config_reloaded | spool_warning | idle_stop | conductor | approval`; replay via Last-Event-ID).
- Controls: `POST /api/runs/:id/{message,answer,plan_update,pause,resume,stop,kill}`,
  `POST /api/spawn` (+ `dry_run`), `PATCH /api/accounts/:id`, `GET|PATCH /api/config`,
  `GET /api/approvals`, `POST /api/approvals/:id/{approve,reject}`, `GET /api/audit`,
  `GET /api/runs/:id/events?since=` (timeline/replay).
- Answer body is `{"qid": "...", "text": "..."}`. Admin auth = `Authorization: Bearer <admin token>`
  (SSE: `?token=`). Dev against `baton mock --speed 5` (no quota; data in `<BATON_HOME>/mock`);
  `npm run dev:web` with `BATON_HUB_URL=<mock url>` for hot reload.

Dashboard work: the design contract is a local, uncommitted file (`web/DESIGN.local.md`; if it is
missing, follow the existing tokens in `web/src/style.css`). Verify visually against the mock (no
quota), e.g. with a headless browser; keep the list view able to reach every action.

Known small items: multi-field PATCH /api/accounts is not atomic; kill route answers 409
RUN_ENDED (global mapping 410); mock hub data lives in `<BATON_HOME>/mock/data`.

## F. Decisions log (details in PLAN.md §4 and GO_HUB_DESIGN.md §0)

- 2026-11-06: run `baton` in project folder; hub daemon; Ctrl+C detaches; `baton attach`.
- 2026-11-06: headless conductor owned by the hub; terminal chat + dashboard chat.
- 2026-11-06: conductor *proposes* lineup, user approves; modes user/propose/auto.
- 2026-11-06: conductor **model is a setting** (not fixed to Opus); conductor kind open (PLAN §7 Q9).
- 2026-11-06: config global + project, hot-reload, dashboard editing, granular permissions,
  custom adapter kinds; msg_id dedupe, outbox spool, `stalled`, timeouts, process-tree kill;
  approval gates, usage tracking, worktree cleanup + gc, audit log; remote access = Phase 8.
- 2026-11-06: runtime data in the user's home (`~/.baton/projects/...`), not in the project.
- 2026-11-06: `max_agents` 6 in auto mode; no global cap when the user picks/approves
  (per-account `max_concurrent` still applies — confirm wording in Phase 2); worktrees `keep`.
- 2026-11-06: keep it small (no over-engineering); per-account usage totals stay in the API.
- 2026-11-06: Phase 3: own foreground hub on a separate data dir (`~/.baton/mock/`); scripted
  scenario then continuous fake agents (`--once`, `--seed`, `--speed`); fake agents react to
  controls; A15 adds usage, conductor state and approvals (diff stat stays on `exit`).
- 2026-11-06: Phase 2 defaults: per-account max_concurrent always applies; no global cap when the
  human picks/approves; auto cap 6 for the conductor; dashboard opens on `baton` (open_browser);
  hub stops after 30 idle minutes (idle_stop_min, 0 = never).
- 2026-11-06: Phase 1 approved; TypeScript reference removed; answers must match an open question (A13).
- 2026-11-06: **hub + CLI in Go, one binary**; JSON Schema is the protocol master; CI on 3 OSes;
  port 7777 preferred with fallback (2222 rejected: common alt-SSH port); `hub stop` asks,
  `--keep-agents`; module `github.com/whosabhiraj/baton`; A12 approved; persistent admin token.

- 2026-11-07: Phase 4: split 4a/4b/4c; lean 3D; login via `#token=` fragment (stored, stripped,
  paste fallback); approval gates: merges need a click, spawns don't. Design: black and quiet,
  muted account colours, amber only for "needs you", plans drawn as route strips, 3D map as a rail
  yard; Vite dev proxy rewrites Host and never serves `*.local.*`.
- 2026-11-07: Phase 5: split 5a/5b/5c; worker agents fully permissive by default; isolated
  per-account config dirs + `baton login`; OpenCode built but disabled; first real claude run
  verified (stdin prompt, stream-json parsing, usage/cost, exit + diff stat).

- 2026-11-07: Phase 6: Claude-only conductor (extensible), default claude-a/opus/high
  (configurable), 6a-6d split, Bubble Tea terminal chat, REST + SSE (no WebSockets); merges are
  no-ff merge commits, refused on conflict or a dirty base checkout (approval stays pending);
  conductor token limited (mode + account enabled only, no approval decisions).

## G. Privacy rule for this repo (public)

Never commit the user's name, email, local paths, folder layout, OS/machine details, or which
accounts they own. Machine-specific notes go in `HANDOFF.local.md` (gitignored). Use generic
examples (`~/.baton/...`, `<repo>`) in docs. The Go module path is the public repo URL (ok).
