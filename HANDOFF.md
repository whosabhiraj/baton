# HANDOFF — read this first

Last updated: **2026-11-07**, **Phase 7 code done** (hardening + release): `baton gc`, conductor usage in
account totals, error-path tests, `npm run release`, one full code review with its findings fixed.
Waiting on the user: Windows CI log, and one design question (§E).

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
| `npm run release` | 6 cross-compiled static binaries + `SHA256SUMS` in `dist/release/<version>/` (plain version on a `v<version>` tag) |
| `baton` | in a repo: hub (background) + conductor, opens the dashboard and, in a terminal, the conductor chat (`--no-chat`) |
| `baton up` | in a repo: starts (or reuses) the background hub, prints URL + admin token, opens the dashboard (no conductor) |
| `baton attach` | status, then the conductor chat (Bubble Tea; plain line mode when not a terminal). Ctrl+C detaches |
| `baton hub status` / `baton hub stop` | hub state / stop it (asks; `--keep-agents`) |
| `baton gc [--yes] [--force]` | list and (after asking) remove policy-allowed worktrees and ended run folders older than `hub.runs_keep_days`; never live runs or branches |
| `baton login <account>` / `baton login --status` | log an account's CLI in, inside its own config dir; an unknown id is added to the global config (asks the kind, or `--kind`) |
| `baton mock [--speed N] [--seed N] [--once] [--reset]` | demo hub with a fake conductor and fake agents (`~/.baton/mock`), no quota; the chat echoes, one lineup is proposed |
| `npm run dev:web` | dashboard dev server with hot reload; set `BATON_HUB_URL` to a running hub (e.g. the mock) and open it with `#token=<admin token>` |
| `baton mcp` | conductor MCP stdio server (started by the hub in 6b; needs `BATON_CONDUCTOR_TOKEN`) |
| `baton help` | agent commands (`hello`, `plan`, `step`, `progress`, `ask`, `inbox`, `done`, ...) |

Things only you can do:
- Log in each account once: `baton login <account-id>` (ids are whatever the config says; a new id gets added)
  (config is `~/.baton/config.yaml`; each account has its own dir under `~/.baton/accounts/`).
- **CI** (`.github/workflows/ci.yml`, 3 OSes) is in the repo. Changing it needs a token with the
  `workflow` scope (or the GitHub web UI). Job logs need a signed-in GitHub user to read.
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
| 5 Adapters | ✅ done: supervisor, env allowlist, claude + codex (real runs ok), opencode + refusal, custom kinds, `baton login` (adds unknown ids) | `852719b` … `72e99ec` |
| 6 Conductor | ✅ done: 6a conductor token, run/diff/merge/wait routes, real merges, `baton mcp`; 6b headless conductor (history, resume, auto-wake); 6c chat (Bubble Tea + dashboard sheet); 6d lineups. Acceptance passed on real accounts | `12da74f` … `b9cb6fe` |
| 7 Hardening + release | code done: gc + cleanup policy enforced, conductor usage, error-path tests, release script, review fixes | `172cec5` … `93f6a83` |
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
internal/mcpserver      `baton mcp`: the conductor's MCP stdio server, 20 tools = thin REST calls (A16, A18)
internal/conductor      headless conductor process (claude -p stream-json), history, resume, auto-wake,
                        embedded prompt.md, FakeCLI (BATON_FAKE_CLAUDE=1) for mock and tests (A17)
internal/chat           terminal chat: Bubble Tea UI + line mode, slash commands
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

## E. Resume here (Phase 7 code done; sandbox part 4 done and verified)

Sandbox part 4, network on approval (2026-11-07), SOLVED (`8d313bb`): `baton __codex` opts into
codex's experimental app-server API (`capabilities.experimentalApi`) and turns on
`features.exec_permission_approvals` + `features.request_permissions_tool` (both "under
development" in codex 0.167). A blocked agent asks for network with a permissions request, the user
approves it in the dashboard, and the grant holds for the rest of the run (scope `session`). Real run
on Windows/mxc: approved, curl exit 0, task committed. What did NOT work (do not retry): approving a
`require_escalated` command (still sandboxed under mxc, no network); codex's managed network proxy
(`features.network_proxy` + `permissions.<p>.network`) hard-denies hosts not on its allowlist and
never asks (host prompts look admin-only, `requirements.toml`); `network.enabled` without that
feature opens all network. Risk: both features are experimental and may change with codex
updates. Approval is network-wide (any host), not per host.


Phase 7 (2026-11-07): 7a conductor usage counted in its account's totals (`conductor/usage.json`:
claude's `result` lines carry session running totals that survive `--resume`); 7b `baton gc`
(+ `POST /api/gc`, A19, `hub.runs_keep_days`, the worktree cleanup policy now runs hourly in the
hub); 7c `*_errpaths_test.go` (every error code has a status test); 7d `npm run release`; 7e one
code review. Fixed from it: spawn `kind` must match the account (subscription preflight could be
bypassed); admin token no longer printed to `hub.out`, data dirs 0700 / logs 0600; no run tokens
in spawn responses to the conductor, conductor history redacted, `bt_cond_` rule, envelope extras
redacted; conductor wait after a restart starts where it last read; idle stop works with a
conductor; merge approvals bound to the reviewed commit (`detail.head`); Unix kill always
SIGKILLs the group; runs orphaned by a crash mid-spawn get an exit; history fanout order; conductor
stdout read to EOF before Wait. Verified on Windows and Linux (WSL).

Open (ask the user):
- Sandboxed agents (2026-11-07), plan parts 1-4: (1) DONE `permissions.mode: sandboxed` default
  (`eab4bbf`, `0494005`, `ad2c4fb`). Codex: `-c default_permissions="baton"` + inline
  `permissions.baton.filesystem` (`:root` read, worktree + run dir + own-branch git paths write,
  siblings of the worktree path inside Baton's home + `deny_read` denied, never the worktree's
  parents: codex exec cannot start commands under a denied parent), `windows.sandbox="mxc"` on
  Windows (unelevated cannot deny reads), `approval_policy="never"`, no network. Verified with
  real runs: token unreadable, commit on own branch ok, main can't move, network off. Codex under
  mxc hangs after `turn.completed`: the launcher ends a CLI 20s after its final line (exit 0,
  detail). Claude: `--settings` sandbox file (macOS/Linux/WSL2; refuses on native Windows, config
  note). Agents get no hub URL/token (spool). (2) Claude access approvals via
  `--permission-prompts host --permission-prompt-tool` (an agent MCP tool -> Baton approval).
  (3) `runtime: wsl` for Claude on Windows (not started). (4) DONE + verified (network grant, see top of §E): `baton __codex` driver over the app-server
  (`codex app-server generate-json-schema`: CommandExecutionRequestApproval, FileChange...,
  network policy amendments). The user's Claude accounts are set to permissive for now.
- Windows CI: two timing flakes found from the job log and fixed (`d476bf2`).
- Known small items: multi-field PATCH /api/accounts not atomic; kill answers 409 RUN_ENDED; claude
  adapter may emit two identical `usage` events; gc removes an old run even with a pending approval.

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

- 2026-11-07: Phase 6b-6d: headless conductor with history/resume/auto-wake, Bubble Tea and
  dashboard chat, lineups (propose -> approve, editable -> spawn); conductor read-only + MCP tools.

- 2026-11-07: Phase 7: `baton gc` = worktrees the cleanup policy allows + ended run folders older
  than `hub.runs_keep_days` (30); dry list, asks (`--yes`), never live runs, uncommitted worktrees
  only with `--force`, branches never deleted. Release = local `npm run release` (GitHub Releases
  workflow later, needs the `workflow` token scope). Hardening = error-path tests + one code review.
  The conductor's own usage counts toward its account's totals.

## G. Privacy rule for this repo (public)

Never commit the user's name, email, local paths, folder layout, OS/machine details, or which
accounts they own. Machine-specific notes go in `HANDOFF.local.md` (gitignored). Use generic
examples (`~/.baton/...`, `<repo>`) in docs. The Go module path is the public repo URL (ok).
