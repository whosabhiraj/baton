# baton

run a bunch of coding agents at once (claude code, codex, opencode, or really anything that can
run a shell command) across your accounts. one conductor agent splits up the work and keeps
things moving, you approve the important stuff, and there's a dashboard to watch it all.

still being built, not usable yet.

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

phase 1 of 8: protocol, the agent cli and the hub basics. plan and design are in `docs/`.

## building

needs go (see `go.mod`), node 24+ and git.

```
npm install
npm run check    # fmt, vet, all tests
npm run build    # -> dist/baton
```

## docs

- `protocol/PROTOCOL.md`: how agents talk to the hub
- `docs/GO_HUB_DESIGN.md`: how it's built and why
- `docs/PLAN.md`: the plan
