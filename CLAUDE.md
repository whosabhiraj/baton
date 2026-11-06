# Baton

Before doing anything, read `HANDOFF.md` (current status + how to resume), then `PROGRESS.local.md`
if it exists (local live log, not committed — the previous session may have died mid-phase),
then `docs/PLAN.md` (authoritative plan) and `docs/GO_HUB_DESIGN.md`.

Rules: ask the user instead of assuming; work phase by phase and wait for approval before
starting the next phase; commit progress continuously (stable, tested pieces; WIP commits are
fine, so an interrupted session loses nothing); ask before spending model quota; update `HANDOFF.md`
at the end of every phase; update `PROGRESS.local.md` after every meaningful step (decision, agent
finished, test result) so work can resume if the session dies.

Never commit personal material: the user's messages or prompts, quotes or paraphrases of them,
working notes or reasoning, machine details. Those stay in `*.local.md` files (gitignored).
