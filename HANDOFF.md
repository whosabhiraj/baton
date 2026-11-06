# HANDOFF — read this first

Last updated: **2026-11-07**, **Phase 4a/4b/4c built** and redesigned (`20f9bfe`), awaiting the user's check. Phase 4 questions answered — see §E.

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
| `baton mock [--speed N] [--seed N] [--once] [--reset]` | foreground demo hub with a fake conductor and fake agents on a scratch repo (`~/.baton/mock`); no real agents, no quota |
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
| 3 `baton mock` | ✅ done (controls, approvals, kill checked by hand) | `7e40608` … this commit |
| 4 Dashboard | 4a list/panels, 4b 3D map, 4c timeline + replay, redesign; awaiting check | `4b771b3` … `20f9bfe` |
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
internal/approvals      approval requests (merge/spawn), approvals.json
internal/mock           `baton mock`: fake conductor + fake agents driving a real hub over HTTP
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

## E. Resume here (Phase 4 built, awaiting check)

Phase 4 answers (2026-11-07, all the recommended options):
1. Split: **4a** working dashboard without 3D (list view, agent panel with plan, events, question +
   answer box, controls, approvals, usage, spawn form, account toggles, config editor) → **4b** 3D
   scene → **4c** timeline/replay. Conductor chat panel moves to Phase 6. The user checks after each step.
2. 3D: **lean first** (account orbits, agent shapes by kind, status looks, progress arcs, edges,
   click-to-focus, bloom, reduced-motion); pulses/particles/ring bursts/completed shelf only on request.
3. Login: `baton` / `baton mock` open `http://127.0.0.1:<port>/#token=...`; the page stores it in
   localStorage and strips it from the address bar; paste box as fallback. (Done in `4b771b3`.)
4. Approval gates: keep the current defaults — merges need a human click, spawns don't.

State: Phase 4 built. 4a list/panels, 4b lean 3D map (`web/src/scene.ts`, lazy-loaded three.js),
4c timeline with replay scrub (`web/src/ui/timeline.ts`), then a full redesign (`20f9bfe`). Checked
in headless Edge against the mock (no console errors). The design contract lives in a local,
uncommitted file (`web/DESIGN.local.md`). Dev loop: `baton mock` + `npm run dev:web` (Vite proxies
`/api` with a rewritten Host; never serves `*.local.*`). Next: user feedback, then ask before Phase 5.

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

Constraints for Phase 4: keep it lean (no over-engineering); stack per spec = Vite + TypeScript +
three.js, no React; types generated from `protocol/schema`; served embedded from `web/dist`, Vite
proxy in dev; list view must reach every action; verify visually against the mock (no quota).

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

## G. Privacy rule for this repo (public)

Never commit the user's name, email, local paths, folder layout, OS/machine details, or which
accounts they own. Machine-specific notes go in `HANDOFF.local.md` (gitignored). Use generic
examples (`~/.baton/...`, `<repo>`) in docs. The Go module path is the public repo URL (ok).
