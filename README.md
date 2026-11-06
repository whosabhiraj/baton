# baton

run a bunch of coding agents at once (claude code, codex, opencode, or really anything that can
run a shell command) across your accounts. one conductor agent splits up the work and keeps
things moving, you approve the important stuff, and there's a dashboard to watch it all.

still being built: the hub, agent cli and a mock work, the dashboard and real agents don't yet.

## the idea

- run `baton` in a repo and say what you want done
- the conductor (whatever model you set it to) proposes a team: how many agents, which
  account, which model, a plan for each. you ok it or edit it
- every agent gets its own git worktree and branch, so nobody steps on anyone
- agents report with a tiny cli (`baton step s1 done --summary "..."`), ask questions when
  stuck, and the conductor answers, reviews diffs and asks you before merging
- if the hub dies, agents keep going and it catches up after, no duplicates

it only runs the official clis and never touches your login tokens. everything stays on
127.0.0.1.

## status

phases 1-3 of 8 done:

- protocol + the agent cli, with a file spool so nothing is lost when the hub is down
- background hub per project: runs, git worktrees, account rules, live config, audit log, stalled detection
- `baton mock` (below)

next is phase 4, the dashboard. then real agent adapters, then the conductor. plan and design are in `docs/`.

## building

needs go (see `go.mod`), node 24+ (only for the helper scripts) and git.

```
npm run check    # fmt, vet, all tests
npm run build    # -> dist/baton
npm run install:baton   # puts baton on your PATH (open a new terminal)
```

what works today: `baton up` in a repo starts the hub and prints its url and admin token,
`baton hub status` / `baton hub stop`, and the http api (`protocol/PROTOCOL.md`), e.g. spawning
an agent run with `POST /api/spawn`. no agent process starts yet, that's phase 5.

## try it without agents

`baton mock` starts a hub with a fake conductor and six fake agents on a scratch repo, so the dashboard can be built and tried without spending any quota. until the dashboard exists, poke it through the api (`GET /api/state`, answer questions, pause or kill runs, approve merges). it uses its own data folder (`~/.baton/mock`, never your project or your real config). ctrl+c stops it. `--once` stops after the opening scenario, `--speed 5` runs it faster, `--seed N` repeats a run, `--reset` starts from a clean folder.

## docs

- `protocol/PROTOCOL.md`: how agents talk to the hub
- `docs/GO_HUB_DESIGN.md`: how it's built and why
- `docs/PLAN.md`: the plan
