# HANDOFF — read this first

Last updated: **2026-11-06**, end of **Phase 0 (plan + repo init)**.
Next up: **Phase 1** (protocol + `baton` CLI + spool). Nothing has been built yet — no code,
no `package.json`, no dependencies installed.

---

## A. For the user: how to pick this back up

1. Open a terminal in the repo folder and run `claude`.
2. Paste this as your first message:
   > Read HANDOFF.md and docs/PLAN.md, then continue with the next phase. Ask me the open
   > questions for that phase before writing code.
3. Claude will ask you the open questions for Phase 1/2 (see `docs/PLAN.md` §7), then build
   the phase, show you the result, and wait for your OK before committing.

Things only you can do (needed by Phase 5, not before):
- Log in to each account's CLI in its own isolated config dir (via `baton login <account-id>`,
  built in Phase 5).
- Decide whether to use OpenCode, and with which API-key provider.

Once Phase 1+ exist, this section will also list the exact run/test commands.

## B. For Claude (fresh session, zero context): how to resume

1. Read, in order: this file → `HANDOFF.local.md` if it exists (machine-specific facts, not
   committed) → `docs/PLAN.md` (authoritative) → `docs/BATON_BUILD_PROMPT.md`
   (original spec; PLAN.md overrides it where they differ).
2. Check state: `git log --oneline`, `git status`. The "Phase status" table below must match
   the git log; if it doesn't, trust git and tell the user.
3. **Working rules the user set (strict):**
   - **Ask, don't assume.** Any ambiguity → ask the user (AskUserQuestion). The spec/plan is a
     suggestion: propose improvements, implement only after approval.
   - **Phase by phase.** Finish one phase, show the result, **wait for approval**, then commit
     (one or more commits per phase), then ask before starting the next.
   - **Ask before anything that spends model quota** (real claude/codex runs).
   - The user favours **configurability and control** — prefer settings over hard-coding.
   - Explain technical choices in plain language; the user isn't deep on protocol internals.
   - **Update this file at the end of every phase** (status table, what exists, how to run and
     test, next steps, new decisions). Keep it clear enough that a fresh Claude can continue
     with no other context.
4. Start the next phase by asking that phase's open questions from `docs/PLAN.md` §7.
5. Commit messages end with the Co-Authored-By line from the session's attribution rules.

## C. Phase status

| Phase | Status | Commit |
|---|---|---|
| 0 Plan + repo init | ✅ done | initial commit |
| 1 Protocol + CLI + spool | ⏭ next | — |
| 2 Hub core | pending | — |
| 3 `baton mock` | pending | — |
| 4 Dashboard | pending | — |
| 5 Adapters | pending | — |
| 6 Conductor | pending | — |
| 7 Hardening | pending | — |
| 8 Remote access (future) | not planned yet | — |

## D. What exists right now

```
CLAUDE.md                    pointer to this file
HANDOFF.md                   this file
docs/PLAN.md                 approved plan (all decisions)
docs/BATON_BUILD_PROMPT.md   original spec
.gitignore
```

## E. Phase 1 — concrete starting checklist

1. Ask the user open question #1 (runtime data location) from PLAN.md §7.
2. `npm init`, add deps (`hono`, `@hono/node-server`, `zod@4`, `yaml`; dev `typescript`,
   `@types/node`), `tsconfig.json` with `allowImportingTsExtensions`, `noEmit`, `strict`,
   `erasableSyntaxOnly`.
3. `protocol/PROTOCOL.md`: copy spec §3 verbatim (lines starting at `## 3. The protocol: BATON/1`
   up to, not including, `## 4. Accounts`), then append the Amendments from PLAN.md §4.5.
4. `protocol/types.ts` (zod) → script that generates `protocol/schema/*.json`.
5. `hub/redact.ts`, minimal `hub/server.ts` (ingest w/ per-run token + msg_id dedupe + seq,
   events, inbox, SSE, dev endpoint to register a run), `hub/spool.ts` (outbox tail).
6. `cli/baton.ts` + `bin/baton.js`: all agent commands from spec §3.4 with the CLI rules.
7. `test/lifecycle.test.ts`: full agent lifecycle including `ask --blocking` and hub-down spooling.
8. `npm link`; check `baton` from PowerShell, cmd, Git Bash.
9. Show the user, get OK, commit, update this file.

## F. Decisions log (short; details in PLAN.md §4)

- 2026-11-06: run `baton` in project folder; hub daemon; Ctrl+C detaches; `baton attach`.
- 2026-11-06: headless conductor owned by the hub; terminal chat + dashboard chat.
- 2026-11-06: conductor *proposes* lineup (count/model/effort/account), user approves; modes
  user/propose/auto switchable.
- 2026-11-06: global `~/.baton/config.yaml` + project `baton.config.yaml` override; hot-reload;
  dashboard editing; granular opt-in permissions; custom adapter kinds.
- 2026-11-06: msg_id dedupe, separate outbox spool, `stalled` not killed, configurable timeouts,
  process-tree kill.
- 2026-11-06: extras — approval gates, usage tracking, worktree cleanup + gc, audit log.
- 2026-11-06: local only now; Cloudflare remote access = Phase 8.

## G. Privacy rule for this repo (public)

Never commit the user's name, email, local paths, folder layout, OS/machine details, or which
accounts they own. Machine-specific notes go in `HANDOFF.local.md` (gitignored). Use generic
examples (`~/.baton/...`, `<repo>`) in docs.
