# baton

run a bunch of coding agents at once (claude code, codex, opencode, or really anything that can
run a shell command) across your accounts. one conductor agent splits up the work and keeps
things moving, you approve the important stuff, and there's a dashboard to watch it all.

still being built: everything up to the conductor is written and tested. claude code agents have
done real runs; codex and the conductor haven't been tried against the real clis yet.

## the idea

- run `baton` in a repo and tell the conductor what you want done
- the conductor (claude code, model and effort set in the config) proposes a team: how many
  agents, which account, which model, a plan for each. you ok it or edit it
- every agent gets its own git worktree and branch, so nobody steps on anyone
- agents report with a tiny cli (`baton step s1 done --summary "..."`), ask questions when
  stuck, and the conductor answers, reviews diffs and asks you before merging
- if the hub dies, agents keep going and it catches up after, no duplicates

it only runs the official clis and never touches your login tokens. everything stays on
127.0.0.1.

## status

phases 1-6 of 8 written, real runs of codex and the conductor still to do:

- protocol + the agent cli, with a file spool so nothing is lost when the hub is down
- background hub per project: runs, git worktrees, account rules, live config, audit log, stalled detection
- `baton mock` (below)
- the dashboard: agent list with each plan drawn as a little route, a panel per agent (answer
  questions, message, pause, stop, kill), approvals, spawn form, config, a 3d map and a
  timeline you can scrub back through. works on a phone too
- agents: claude code, codex and custom kinds from the config, each with a small supervisor
  process, so it survives a hub restart and kill takes its whole process tree with it.
  `baton login <account>` logs an account in with its own config folder (a name that isn't in
  the config yet gets added, it asks claude or codex). opencode is built but
  stays off until it has an api-key provider (it never uses a claude subscription)
- the conductor: a headless claude code session the hub owns. it talks to the hub through
  `baton mcp` (spawn, read events, answer, review diffs, merge), keeps its conversation across
  restarts, and gets nudged when an agent asks something or finishes while it's idle
- chat with it in the terminal (`baton` / `baton attach`) or from the dashboard's chat sheet
- lineups: outside `auto` mode the conductor proposes the team as an approval; you can change
  each agent's account, model, effort or task, or drop one, before it spawns
- merges are real `--no-ff` merge commits, only after you approve, and refused if they'd
  conflict or the base checkout has uncommitted changes

next: hardening (phase 7). plan and design are in `docs/`.

## building

needs go (see `go.mod`), node 24+ (for the dashboard build and helper scripts) and git.

```
npm install
npm run check    # fmt, vet, all tests, dashboard typecheck
npm run build    # dashboard + binary -> dist/baton
npm run release  # binaries for windows/macos/linux (amd64 + arm64) + SHA256SUMS -> dist/release/
npm run install:baton   # puts baton on your PATH (open a new terminal)
```

## using it

```
baton login my-claude    # once per account; new names get added to ~/.baton/config.yaml
baton                    # in a repo: hub + conductor, dashboard, and the chat in your terminal
```

- `baton` starts the hub in the background (or reuses it), starts the conductor and opens the
  dashboard and the chat. ctrl+c only leaves the chat; `baton attach` comes back to it.
  `--no-chat` skips it
- `baton up` is the same without the conductor, if you'd rather spawn agents yourself from the
  dashboard (or `POST /api/spawn`, see `protocol/PROTOCOL.md`)
- `baton hub status` / `baton hub stop` do what they say
- `baton gc` removes worktrees your cleanup policy allows and ended run folders older than `hub.runs_keep_days` (it lists them and asks first; `--yes` skips, `--force` also takes worktrees with uncommitted changes; branches and live runs are never touched)
- in the chat: `/agents`, `/approvals`, `/approve`, `/reject`, `/mode`, `/stop`, `/help`

the conductor's defaults live under `conductor:` in the config (account, model, effort,
`auto_wake`), and `hub.mode` picks whether its lineups wait for your ok (`user`, `propose`) or
it just spawns (`auto`).

> NOTE: worker agents are sandboxed by default, except claude on native windows, which can't
> be. read the security section below.

## security

worker agents run with `permissions.mode: sandboxed` unless you change it:

- they can write only to their own worktree and run folder (and the bits of the repo's `.git`
  a commit on their own branch needs, so they can't move `main`)
- they can't read `~/.baton` (account logins, the admin token, other runs) or anything you list
  in `permissions.deny_read`, and they have no network. `baton` still works: it reports through
  files in the run folder
- codex uses its own sandbox (on windows the `mxc` one, no admin setup). claude uses claude
  code's sandbox, which only exists on macos, linux and wsl2: on native windows a sandboxed
  claude account refuses to start and tells you so

`permissions.mode: permissive` turns all of that off for an account: the agent runs as you and
can do anything you can, including reading baton's admin token and approving its own merge.
use it only for repos and tasks you'd trust a script with. `acceptEdits` and `default` use the
cli's own prompts, which nobody can answer in a headless run.

the conductor is restricted either way: it can only read the project and call baton's tools,
its token can't approve anything, and it never sees the agents' tokens. the hub only listens on
127.0.0.1 and its data folders are private to your user.

coming next: agents asking you (in the dashboard) for network or files outside their area,
and running claude agents inside wsl2 on windows.

## try it without agents

`baton mock` starts a hub with a fake conductor and fake agents on a scratch repo and opens the
dashboard, so you can click around without spending any quota. the chat echoes what you send
and it proposes one lineup you can edit and approve. it uses its own data folder
(`~/.baton/mock`, never your project or your real config). ctrl+c stops it. `--once` stops
after the opening scenario, `--speed 5` runs it faster, `--seed N` repeats a run, `--reset`
starts from a clean folder.

working on the dashboard: run `baton mock`, then `npm run dev:web` with `BATON_HUB_URL` set to
the url it printed, and open the vite url with `#token=<admin token>`.

## docs

- `protocol/PROTOCOL.md`: how agents and the conductor talk to the hub
- `docs/GO_HUB_DESIGN.md`: how it's built and why
- `docs/PLAN.md`: the plan
