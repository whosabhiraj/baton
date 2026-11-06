# HANDOFF — read this first

Last updated: **2026-11-06**, **Phase 2 in progress** (session ended mid-phase; see §E).
Next up: finish Phase 2 wiring (§E), run all tests, show the user, get approval.

Big change since Phase 0: the hub **and** the `baton` CLI are written in **Go** (one binary).
`docs/GO_HUB_DESIGN.md` is approved and overrides `docs/PLAN.md` §3, §4.7, §5 and §6.

---

## A. For the user: how to pick this back up

1. Open a terminal in the repo folder and run `claude`.
2. Paste this as your first message:
   > Read HANDOFF.md, PROGRESS.local.md (if present) and docs/PLAN.md, then continue. Ask me the open questions
   > for the next phase before writing code.

Commands that work today (need Go and Node 24+):

| Command | What it does |
|---|---|
| `npm run check` | formatting, vet, every Go test (unit, protocol conformance, end-to-end lifecycle, CLI behaviour, crash recovery), then a build |
| `npm run build` | builds `dist/baton` (`dist/baton.exe` on Windows), version-stamped |
| `npm run install:baton` | `go install` → `baton` on PATH (Go's bin folder; open a new terminal) |
| `baton up --dev` | runs a hub in the foreground (prints URL + admin token; Ctrl+C stops). Phase 2 makes it a background daemon |
| `baton help` | agent commands (`hello`, `plan`, `step`, `progress`, `ask`, `inbox`, `done`, …) |

Things only you can do:
- **CI:** GitHub refused `.github/workflows/ci.yml` because the git token has no `workflow`
  scope. Either give the token that scope, or add the file through the GitHub web UI. The file
  exists locally (untracked).
- Phase 5: log in to each account's CLI (`baton login <account-id>`); decide on OpenCode.

## B. For Claude (fresh session, zero context): how to resume

1. Read, in order: this file → `PROGRESS.local.md` if it exists (local live log, may be newer) →
   `HANDOFF.local.md` if it exists (machine facts, not committed) → `docs/GO_HUB_DESIGN.md`
   (§0 = decisions) → `docs/PLAN.md` → the original spec, if present locally
   (`docs/BATON_BUILD_PROMPT.local.md`; not in the repo).
2. Check state: `git log --oneline`, `git status`. If the tables here disagree with git, trust
   git and tell the user.
3. **Working rules:**
   - Ask, don't assume (AskUserQuestion). Propose improvements, implement after approval.
   - Phase by phase; ask before starting the next phase.
   - **Commit (and push) progress continuously** — stable, tested pieces; scan for personal
     info first (public repo).
   - Ask before anything that spends model quota (real claude/codex runs).
   - Prefer settings over hard-coding. Explain choices in plain language.
   - Update `PROGRESS.local.md` after every meaningful step; this file at the end of every phase.
   - Never commit personal material (the user's messages, quotes of them, working notes,
     machine details) — keep it in `*.local.md` (gitignored). More working preferences: HANDOFF.local.md.
4. Go may not be on PATH in old shells: prepend Go's bin dir (see HANDOFF.local.md).
5. Commit messages end with the Co-Authored-By line from the session's attribution rules.

## C. Phase status

| Phase | Status | Commits |
|---|---|---|
| 0 Plan + repo init | ✅ done | `2a5a98d` |
| 1 Protocol + CLI + spool + minimal hub (Go) | ✅ done | up to the "Phase 1 done" commit |
| 2 Hub core | 🟡 in progress: config, audit, runs/worktrees/briefs, daemon + human commands done; hub/API wiring in flight | `d014636` … `35d8fba` |
| 3 `baton mock` | pending | — |
| 4 Dashboard | pending | — |
| 5 Adapters | pending | — |
| 6 Conductor | pending | — |
| 7 Hardening | pending | — |
| 8 Remote access (future) | not planned yet | — |

## D. What exists right now

```
cmd/baton/              main → internal/cli.Main
internal/proto          envelope/payload types, JSON-Schema validation (embedded schemas)
internal/ids            run ids (ULID), UUIDs, tokens, qids
internal/paths          ~/.baton/projects/<name>-<h8>/runs/<run_id>/ layout
internal/redact         secret redaction (+ slog handler)
internal/store          per-run store: seq, msg_id dedupe, recovery, status, inbox delivery
internal/spool          outbox.jsonl tailer (offsets, partial lines, CRLF/BOM, UTF-16)
internal/bus            SSE fan-out ring buffer
internal/api            HTTP API (PROTOCOL.md A11 + A12)
internal/hub            wiring, OS lock, port fallback, admin token, stalled detection
internal/cli            agent commands + `baton up` (foreground) + help/version
protocol/PROTOCOL.md    BATON/1 (original spec §3 + amendments A1–A12)
protocol/schema/*.json  JSON Schemas = source of truth; protocol/fixtures/ = shared examples
e2e/                    lifecycle, CLI parity (38 cases), crash recovery (hard-killed hub)
scripts/*.mjs           build / install / test / check (cross-platform)
web/embed.go            embeds web/dist (dashboard comes in Phase 4)
docs/                   PLAN.md, GO_HUB_DESIGN.md
```

Runtime data: `~/.baton/projects/<repo-name>-<hash8>/` (override with `BATON_HOME`), with
`admin.token`, `hub.lock`, `hub.port`, `runs/<run_id>/{run.json, events.jsonl, outbox.jsonl,
outbox.offset, inbox/}`.

## E. Resume here (Phase 2)

Done and committed: `internal/config` (+ `baton.config.example.yaml`, `protocol/schema/config.json`),
`internal/audit`, `internal/runs` + `internal/briefing`, `internal/gitx`, `internal/daemon`,
`internal/logx`, human commands in `internal/cli` (`baton`, `up`, `attach`, `hub serve|status|stop|rotate-token`).

Remaining ("P2-D wiring"; may be partly in the working tree: check `git status`, review, finish, test):
1. `hub.Options`: `Repo`, `GlobalConfig`, `ProjectConfig`, `ConfigPoll`, `IdleStop`; Start runs the
   config watcher, audit log and runs manager; heartbeat timeout read live from config.
2. Bus carries hub notices → SSE `event: hub` (`config_reloaded`, `spool_warning`, `idle_stop`).
3. API (admin; actor via `X-Baton-Actor`): `POST /api/spawn`, `GET /api/accounts`,
   `PATCH /api/accounts/{id}`, `GET|PATCH /api/config`, `GET /api/audit`; `/api/state` gains
   accounts, mode, config_ok, config_issues, instance; audit entry for every control action.
4. Idle stop: no live runs + no SSE clients + no admin requests for `hub.idle_stop_min` (30).
5. `store.Run.SetExtra`; PROTOCOL.md A14 for the new routes/events; `e2e/phase2_test.go`.
6. Then `hub serve` passes `Repo` to `hub.Options` (internal/cli/up.go); optional `Hub.Instance`.
7. Phase 2 acceptance: kill hub mid-run → recover (crash test), live config edit applies, broken
   config reported while the hub keeps running, one hub per project (daemon test). Then `npm run check`.

Other follow-up: CI file stays untracked until the git token has `workflow` scope.

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
- 2026-11-06: Phase 2 defaults: per-account max_concurrent always applies; no global cap when the
  human picks/approves; auto cap 6 for the conductor; dashboard opens on `baton` (open_browser);
  hub stops after 30 idle minutes (idle_stop_min, 0 = never).
- 2026-11-06: Phase 1 approved; TypeScript reference removed; answers must match an open question (A13).
- 2026-11-06: **hub + CLI in Go, one binary**; JSON Schema is the protocol master; CI on 3 OSes;
  port 7777 preferred with fallback (2222 rejected: common alt-SSH port); `hub stop` asks,
  `--keep-agents`; module `github.com/whosabhiraj/baton`; A12 approved; persistent admin token.

## G. Privacy rule for this repo (public)

Never commit the user's name, email, local paths, folder layout, OS/machine details, or which
accounts they own. Machine-specific notes go in `HANDOFF.local.md` (gitignored). Use generic
examples (`~/.baton/...`, `<repo>`) in docs. The Go module path is the public repo URL (ok).
