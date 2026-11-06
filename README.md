# baton

run a bunch of coding agents at once (claude code, codex, opencode, or really anything that can
run a shell command) across your accounts. one conductor agent splits up the work and keeps
things moving, you approve the important stuff, and there's a dashboard to watch it all.

still being built: the hub, agent cli, dashboard and claude code agents work. codex, opencode and the conductor don't yet.

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

phases 1-4 of 8 done, 5 in progress:

- protocol + the agent cli, with a file spool so nothing is lost when the hub is down
- background hub per project: runs, git worktrees, account rules, live config, audit log, stalled detection
- `baton mock` (below)
- the dashboard: agent list with each plan drawn as a little route, a panel per agent (answer
  questions, message, pause, stop, kill), approvals, spawn form, config, a 3d map and a
  timeline you can scrub back through. works on a phone too
- real agents: claude code runs for real now (headless, in its own worktree, reports back,
  tokens and cost show up). each agent gets a small supervisor process, so it survives a hub
  restart and kill takes its whole process tree with it

next: codex and `baton login`, then opencode, then the conductor. plan and design are in `docs/`.

## building

needs go (see `go.mod`), node 24+ (for the dashboard build and helper scripts) and git.

```
npm install
npm run check    # fmt, vet, all tests, dashboard typecheck
npm run build    # dashboard + binary -> dist/baton
npm run install:baton   # puts baton on your PATH (open a new terminal)
```

`baton up` in a repo starts the hub, prints its url and admin token and opens the dashboard.
`baton hub status` / `baton hub stop` do what they say. spawn agents from the dashboard (or
`POST /api/spawn`, see `protocol/PROTOCOL.md`).

> NOTE: worker agents run with permission checks off by default (they only touch their own
> worktree, but they can run any command). set `permissions.mode` per account in the config
> (`default`, `acceptEdits` or `permissive`) if you want them on a shorter leash.

## try it without agents

`baton mock` starts a hub with a fake conductor and fake agents on a scratch repo and opens the
dashboard, so you can click around without spending any quota. it uses its own data folder
(`~/.baton/mock`, never your project or your real config). ctrl+c stops it. `--once` stops
after the opening scenario, `--speed 5` runs it faster, `--seed N` repeats a run, `--reset`
starts from a clean folder.

working on the dashboard: run `baton mock`, then `npm run dev:web` with `BATON_HUB_URL` set to
the url it printed, and open the vite url with `#token=<admin token>`.

## docs

- `protocol/PROTOCOL.md`: how agents talk to the hub
- `docs/GO_HUB_DESIGN.md`: how it's built and why
- `docs/PLAN.md`: the plan
