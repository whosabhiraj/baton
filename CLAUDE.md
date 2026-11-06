# Baton

Before doing anything, read `HANDOFF.md` (current status + how to resume), then `PROGRESS.md`
(live log of the current phase — the previous session may have died mid-phase), then
`docs/PLAN.md` (authoritative plan; overrides `docs/BATON_BUILD_PROMPT.md` where they differ).

Rules: ask the user instead of assuming; work phase by phase and wait for approval before
starting the next phase; commit progress continuously (stable, tested pieces; WIP commits are
fine, so an interrupted session loses nothing); ask before spending model quota; update `HANDOFF.md`
at the end of every phase; update `PROGRESS.md` after every meaningful step (decision, agent
finished, test result) so work can resume if the session dies.
