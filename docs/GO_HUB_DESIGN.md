# Baton — Go hub design

Status: **approved 2026-11-06** (decisions in §0 below). Once approved, this document overrides
`docs/PLAN.md` §3 (runtime), §4.7 (implementation decisions), §5 (repo layout) and §6 (phase
table) wherever they differ. Everything else in PLAN.md (non-negotiables, UX, config features,
protocol amendments A1–A11) still applies unchanged.

---

## 0. Decisions (2026-11-06)

| §11 Q | Decision |
|---|---|
| 1 Go CLI too | **Yes, one binary.** TS CLI = reference oracle during Phase 1, then deleted. |
| 2 Rulebook | **JSON Schema files are master**; zod + generator removed after final regeneration. |
| 3 CI | **Yes**, GitHub Actions on Windows + macOS + Linux from Phase 1. |
| 4 Port | Prefer configured port, fall back to next free, remember per project. Default **7777** (2222 avoided: it is the common alternate SSH port — Vagrant, Docker sshd, DirectAdmin). Configurable. |
| 5 `hub stop` | Ask → `stop` agents, kill after grace period; `--keep-agents` leaves them running, re-attach on next start. |
| 6 Module | **`github.com/whosabhiraj/baton`** |
| 7 A12 | **Approved.** |
| 8 Admin token | **Persistent per project**, `baton hub rotate-token`. |

Toolchain installed for development: Go 1.27 (doc text saying 1.26 means "current stable").

## Summary (plain language)

- **What changes:** the hub (the background program that receives agent reports, stores them,
  runs agents and serves the dashboard) will be written in **Go** instead of TypeScript. I also
  recommend that the `baton` command itself be Go, so Baton ships as **one program file**
  (`baton.exe` on Windows, `baton` elsewhere) that works the same in PowerShell, cmd, Git Bash,
  macOS and Linux terminals, starts in a few milliseconds (agents call it constantly), and needs
  no Node.js to run.
- **What stays:** the protocol (`protocol/PROTOCOL.md`, amendments A1–A11) is unchanged; the
  run-folder layout under `~/.baton/projects/...` is unchanged; the dashboard stays TypeScript +
  Vite + three.js and is baked into the Go program at build time. All non-negotiables stay.
- **How the two languages stay in agreement:** the JSON Schema files in `protocol/schema/`
  become the single rulebook. Go checks messages against those exact files, the dashboard gets
  its TypeScript types generated from them, and a shared folder of example messages
  (`protocol/fixtures/`) is tested on both sides. A test fails the moment they drift.
- **What becomes obsolete:** the TypeScript hub pieces (`hub/*.ts`), the zod file
  (`protocol/types.ts`), the schema generator, the npm-link install, and — if you agree — the
  TypeScript CLI now being written (it is used as a reference to check the Go one, then removed).
- **What you need to decide:** see §11 — chiefly (1) Go CLI too, yes/no; (2) JSON Schema as the
  rulebook; (3) automated tests on all three OSes via GitHub Actions; (4) port policy;
  (5) what `baton hub stop` does to running agents; (6) the Go module name.

---

## 1. Scope of Go

### 1.1 Component table

| Component | Language | Phase | Notes |
|---|---|---|---|
| Hub daemon (start, discovery, lock, stop) | Go | 2 | `internal/daemon` |
| HTTP API (A11) + SSE + auth | Go | 1 | `internal/api`, stdlib `net/http` |
| Per-run store (run.json, events.jsonl, seq, dedupe, inbox) | Go | 1 | `internal/store` |
| Outbox spool tailing | Go | 1 | `internal/spool` (reused for adapter stdout in Phase 5) |
| Redaction | Go | 1 | port of `hub/redact.ts` (RE2 changes, §4.1) |
| Protocol validation | Go | 1 | validates against `protocol/schema/*.json` (§2) |
| Config (global + project YAML, validation, hot-reload, write-back) | Go | 2 | `internal/config` |
| Runs, worktrees, git-init prompt | Go | 2 | `internal/runs`, via the `git` CLI |
| Stalled detection, audit log, structured logging | Go | 2 | |
| `baton mock` | Go | 3 | writes through the real store, so the dashboard sees real data paths |
| Process supervision, adapters, env isolation, usage parsing, `baton login` | Go | 5 | `internal/proc`, `internal/supervise`, `internal/adapters` |
| MCP server (conductor tools) | Go | 6 | `baton mcp` over stdio, official Go SDK |
| Headless conductor management + terminal chat (`baton`, `baton attach`) | Go | 6 | |
| `baton gc` | Go | 7 | |
| **`baton` agent CLI** (hello/plan/step/…/done/error) | **Go (recommended)** | 1 | see §1.2; open question Q1 |
| Dashboard | **TypeScript** (Vite + three.js) | 4 | built to `web/dist`, embedded in the binary |
| Dashboard types | TS, **generated** from JSON Schema | 4 | §2 |
| Build/task runner | npm scripts (thin) + `go` | 1 | §8 |

### 1.2 Should the `baton` CLI also be Go? — **Recommendation: yes, one binary.**

| Factor | TS CLI (Node 24 type-stripping) | Go CLI |
|---|---|---|
| Startup per call | ~80–200 ms on Windows (Node boot + TS strip + zod import), every call | ~5–15 ms |
| Calls per agent run | dozens to hundreds (every step, heartbeat, inbox, ask polls) | same |
| Runtime requirement on the agent's machine | Node 24 on PATH for every agent process | none |
| Install | `npm link` → npm shims (`baton.cmd`, `baton.ps1`, sh shim) | one `baton(.exe)` on PATH, no shims |
| Windows quirks | `.cmd` shims re-parse arguments through cmd.exe (quotes, `%`, `^`, `&` in `--summary` text can be mangled); PowerShell execution policy can block `.ps1` shims | native exe: arguments passed as-is |
| Code sharing with hub | would need a TS copy of validation and paths → second implementation to keep in sync | shares `internal/proto`, `internal/paths`, `internal/ids` with the hub |
| Toolchains needed to build | Node + Go | Go (Node only for the dashboard) |
| Human commands (`baton`, `attach`, `up`, `hub stop`, `mock`, `login`, `gc`) | would have to start/locate a Go binary anyway | same binary, `baton up` simply runs the hub in-process |

The argument-mangling row matters most: agents pass free text (`--summary "fixed 100% of ^tests & docs"`)
and a `.cmd` shim on Windows can corrupt it. A native exe avoids the whole class of bugs.

**What happens to the TS CLI being written now** (`cli/baton.ts`, `bin/baton.js`,
`test/cli.test.ts`): it becomes the **reference oracle** for Phase 1. Its black-box test
(`test/cli.test.ts`) only spawns a binary and checks stdout/exit codes/files, so it is ported
1:1 to Go (`e2e/cli_test.go`) and the Go CLI must pass the same cases with the same output
strings. After the Go CLI passes, the TS CLI, its wrapper and its tests are deleted (open
question Q1 offers keeping it). Its `baton up` (which imports the TS hub) is not finished.

---

## 2. Protocol source of truth across languages

### 2.1 Options

| Option | How | Pros | Cons |
|---|---|---|---|
| A. zod stays the master | `protocol/types.ts` → generate JSON Schema → Go validates with it | nice authoring | changing the protocol needs Node; zod becomes a third representation nobody runs at hub runtime |
| **B. JSON Schema files are the master** (recommended) | hand-edited `protocol/schema/*.json`; Go validates against these exact files (embedded); dashboard TS types generated from them | language-neutral; *the same file* is what third-party agents read, what Go enforces and what TS types come from; no generator in the critical path | JSON Schema is more verbose to edit than zod |
| C. Go structs are the master | generate JSON Schema from Go structs (e.g. `jsonschema.For[T]`) | Go-first | constraints (`minLength`, `0–100`, unions like `plan`) are awkward in struct tags; schema output changes with library versions |

**Recommendation: B.** The schemas already exist (generated by zod 4, draft 2020-12). We run
`npm run schemas` one last time, check the output in as the master copy, then delete
`protocol/types.ts` and `scripts/gen-schemas.ts`. From then on, a protocol change = edit the
schema file + add fixtures + update PROTOCOL.md.

### 2.2 What each side does

- **Go** (`internal/proto`): small hand-written structs for the envelope, stored event, inbox
  message, run record and plan step (typed access). **Payloads are kept as generic JSON**
  (`map[string]any`), because unknown types and unknown fields must be stored and forwarded
  (A6, "loose" objects). Validation is done **by the JSON Schema validator against the embedded
  schema files**, so Go never re-implements the rules by hand. Defaults that JSON Schema does
  not apply (missing `payload` → `{}`) are applied in Go code and covered by fixtures.
- **Dashboard** (`web/`): `npm run gen:types` runs `json-schema-to-typescript` over
  `protocol/schema/*.json` → `web/src/protocol.gen.ts` (committed). The dashboard does not
  validate at runtime; it trusts the hub.
- **New schemas for API responses** (A11): `api.health`, `api.ingest-response`, `api.error`,
  `api.state` (RunSummary), `api.inbox`, `api.events`. Needed so dashboard types are generated
  too. Added in Phase 1 (T1).

### 2.3 Fixtures (shared, language-neutral)

```
protocol/fixtures/
  valid/<schema-name>/*.json        must validate
  invalid/<schema-name>/*.json      must fail; file has {"input": ..., "code": "INVALID_PAYLOAD"}
  ingest/*.json                     {"incoming": [...envelopes], "expect": [...stored, normalised]}
                                    (hub-only type rejected, unknown type stored, plan --confirm filled, dup msg_id)
  outbox/*.case/                    outbox.jsonl (raw bytes: CRLF, BOM, partial last line, no msg_id,
                                    bad JSON, wrong run_id) + expect.json (stored lines, warnings, final offset)
  redact/*.json                     [{"in": "...", "out": "..."}]
```

Normalisation in `expect`: `ts` and generated `msg_id` replaced with `"<any>"` before comparison.

### 2.4 Drift checks (run by `npm run check` locally and in CI)

1. `go test ./internal/proto/...`:
   - every schema file compiles;
   - every `valid/` fixture validates and every `invalid/` fixture fails with the stated code;
   - **struct-vs-schema test**: for each registered pair (schema name ↔ Go struct), reflect the
     struct's `json` tags and compare with the schema's `properties` and `required`
     (non-`omitempty` field ⇔ required). Fails if a field is added on one side only.
2. `go test ./internal/store/... ./internal/spool/... ./internal/redact/...` run the `ingest/`,
   `outbox/` and `redact/` fixtures through the real code.
3. `npm run gen:types && git diff --exit-code web/src/protocol.gen.ts` — fails if someone
   edited a schema without regenerating dashboard types.
4. `tsc --noEmit -p web` — the dashboard still compiles against the regenerated types.

---

## 3. Go project layout

Module at the repo root: `module github.com/<owner>/baton` (placeholder; Q6). `go 1.26` in
`go.mod` (current stable at time of writing; the dev machine has 1.26.x).

```
go.mod, go.sum
cmd/baton/main.go            os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
protocol/
  PROTOCOL.md                unchanged contract (+ proposed A12, §5.6)
  embed.go                   package protocol; //go:embed schema/*.json → var Schemas embed.FS
  schema/*.json              MASTER (§2)
  fixtures/...               shared fixtures (§2.3)
internal/
  proto/       types, error codes, Validate* (uses protocol.Schemas)          [Phase 1]
  ids/         run ids (ULID), UUIDv4, tokens, qids, token hashing             [1]
  paths/       BATON_HOME, project data dir, run-dir file names (port of hub/paths.ts) [1]
  redact/      String / Value + redacting slog handler                          [1]
  store/       Store, Run: run.json, events.jsonl, seq, dedupe, inbox, recovery, derived state [1]
  spool/       generic line tailer with byte offsets (outbox; later adapter stdout) [1]
  bus/         global event broker, ring buffer, subscriptions                 [1]
  api/         http.Handler: routes (A11), auth, SSE, JSON errors, recover      [1]
  hub/         wiring: Start(ctx, Options) / Close; lock file                   [1]
  cli/         argv parsing, agent commands, human command dispatch, hub HTTP client [1]
  daemon/      hub.json, detached spawn (_windows/_unix.go), stale detection, stop [2]
  config/      YAML load/merge/validate, polling reload, comment-preserving write-back [2]
  runs/        run lifecycle, worktrees, git helpers, stalled detector          [2]
  audit/       append-only audit.jsonl                                          [2]
  logx/        slog setup, size-based rotation                                  [2]
  mock/        baton mock generator                                             [3]
  proc/        spawn with env isolation, process-tree kill (_windows/_unix.go)  [5]
  supervise/   per-agent supervisor process (`baton __supervise`)               [5]
  adapters/    claude, codex, generic, opencode, custom (+ parsers)             [5]
  usage/       token/cost extraction from adapter output                        [5]
  briefing/    brief template (embedded .md)                                    [5]
  mcp/         MCP server (stdio) over the hub REST API                         [6]
  conductor/   headless conductor process, history, chat relay                  [6]
  chat/        terminal chat UI for `baton` / `baton attach`                    [6]
web/
  (no package.json here — one root package.json, see §8)
  index.html, src/..., vite.config.ts, tsconfig.json
  embed.go                   package web; //go:embed all:dist → var Dist embed.FS
  dist/.gitkeep              committed so `go build` works before the dashboard exists
e2e/                         Go black-box tests (build the binary, spawn it)
scripts/                     tiny Node helpers for npm scripts only (no logic the hub needs)
```

**Why `protocol/embed.go` and `web/embed.go`:** `//go:embed` can only reach files in the
package's own directory or below, so the embedding package must live next to the files.

**Serving the dashboard:**
- Release/normal build: `internal/api` serves `web.Dist` (sub-FS `dist`) at `/`, with
  `index.html` fallback for client routes. If `dist/index.html` is missing (fresh clone), `/`
  serves a one-line page: "Dashboard not built — run `npm run build`". The hub still works.
- Dev: run `baton up --dev` and `npm run dev:web`; **Vite's `server.proxy` forwards `/api` to
  the hub** (URL read from env `BATON_HUB_URL`, default `http://127.0.0.1:7777`). No Go-side
  reverse proxy needed; SSE passes through Vite's proxy.
- `.gitignore`: replace `dist/` with `web/dist/*` + `!web/dist/.gitkeep`.

---

## 4. Libraries

Rule: stdlib first; every dependency must be pure Go (so `CGO_ENABLED=0` cross-compiles to
all targets from any machine). Versions below were checked on the web at design time; the
implementer must re-check with `go list -m -versions <module>` before adding.

| Need | Pick | Why / notes |
|---|---|---|
| HTTP routing | stdlib `net/http.ServeMux` (Go 1.22+ patterns: `POST /api/runs/{id}/answer`, `r.PathValue("id")`) | enough for ~20 routes. Mux 404/405 bodies are plain text: wrap with a catch-all `/api/` JSON 404 and a tiny ResponseWriter wrapper that converts mux 405 to JSON `{ok:false,code:"METHOD_NOT_ALLOWED"}` |
| SSE | hand-rolled (~120 lines) with `http.ResponseController` (`Flush`, `SetWriteDeadline`) | no library needed; full control over ring buffer and reset (§5.3) |
| JSON | `encoding/json` (v1) | `Decoder.UseNumber()` for payloads (no float rounding of big ints), `Encoder.SetEscapeHTML(false)` so stored lines stay readable. `encoding/json/v2` is still behind `GOEXPERIMENT` — revisit when GA |
| JSON Schema validation | `github.com/santhosh-tekuri/jsonschema/v6` (v6.0.x) | passes the official JSON-Schema-Test-Suite for 2020-12; pure Go; mature. Compile schemas **lazily** (only the one a CLI call needs) to keep CLI startup tiny. Alternative: `github.com/google/jsonschema-go` (arrives with the MCP SDK in Phase 6, but v0.x) |
| YAML | `go.yaml.in/yaml/v3` | `gopkg.in/yaml.v3` was marked unmaintained in 2025; the YAML organisation maintains this API-compatible fork (adopted by Kubernetes, Prometheus). Its `yaml.Node` API lets the dashboard config editor **write back without losing the user's comments**. `go.yaml.in/yaml/v4` was still a release candidate at design time — revisit |
| Config hot-reload | **polling** (`os.Stat` mtime+size every 1 s, then content hash; configurable) | 2 files, negligible cost, identical on all OSes, survives editors' atomic save (write temp + rename), network drives and WSL paths. `github.com/fsnotify/fsnotify` (v1.10, 2026) is the upgrade path if instant reload is wanted — then watch the *directory*, not the file, and debounce |
| ULID / UUID / tokens | stdlib `crypto/rand` in `internal/ids` (~60 lines + tests) | ULID = 48-bit ms time + 80 random bits, Crockford base32, 26 chars. Avoids a dependency for trivial code. (`github.com/oklog/ulid/v2` is the fallback if preferred) |
| MCP server | `github.com/modelcontextprotocol/go-sdk` (v1.x; v1.0 promised API stability; v1.7.0 was latest at design time) | official SDK, has stdio server transport and streamable HTTP. We use **stdio** (§6.5) |
| Windows APIs | `golang.org/x/sys/windows` | Job Objects, `LockFileEx`, `OpenProcess`/`GetExitCodeProcess`, console mode |
| Terminal | `golang.org/x/term` | `IsTerminal`, raw mode for the chat UI |
| Logging | stdlib `log/slog` (JSON handler) | wrapped by a redacting handler (§5.7) |
| CLI argument parsing | hand-rolled (~100 lines) in `internal/cli` | stdlib `flag` stops at the first positional, but agents write `baton step s1 done --summary ..` (flags after positionals). Must mirror `node:util parseArgs` strictness of the reference CLI: `--x=v` and `--x v`, `--` terminator, unknown option → exit 2 |
| Git | the `git` CLI via `os/exec` (not go-git) | respects the user's git config/hooks/credential setup; full `git worktree` support (go-git's is incomplete); one less big dependency |
| File lock (one hub per project) | hand-rolled `flock` (unix) / `LockFileEx` (windows), ~40 lines | or `github.com/gofrs/flock` if preferred |
| Tests | stdlib `testing`, `net/http/httptest`, **`testing/synctest`** (GA since Go 1.25) | synctest gives a fake clock for heartbeat/stalled/ask-timeout tests without real sleeps. No testify |
| Terminal chat UI (Phase 6) | plain line-based REPL first (stdlib + x/term) | Bubble Tea later only if wanted |
| Cross-compiling | `CGO_ENABLED=0 GOOS=… GOARCH=… go build` | windows/amd64+arm64, darwin/amd64+arm64, linux/amd64+arm64 |

Phase 1 adds exactly one third-party module (jsonschema/v6) plus `golang.org/x/sys` (lock on
Windows).

### 4.1 Porting `hub/redact.ts` to Go (RE2 differences)

Go's `regexp` is RE2 (linear time — good), but has **no lookahead**. The key=value rule uses
`(?!\[REDACTED\])`; in Go, match without it and, in `ReplaceAllStringFunc`, return the match
unchanged if the value part already starts with `[REDACTED]`. Everything else (`\b`, lazy
`[\s\S]*?`, `(?i)`) ports directly. Replacement callbacks become `ReplaceAllStringFunc` with
`FindStringSubmatch`. The rules and the sensitive-key list move to `internal/redact`, and the
test cases become `protocol/fixtures/redact/*.json`.

---

## 5. Key internal designs

### 5.1 Per-run store (`internal/store`)

**Source of truth:** `events.jsonl` is the truth for everything derived (status, step states,
open questions, last seq, seen `msg_id`s). `run.json` holds identity and control only
(ids, callsign, role, kind, model, account, created/ended, `token_sha256`, `token_revoked`,
conductor `plan`, status snapshot for quick listing). On startup, derived state is rebuilt by
replaying `events.jsonl` through a pure function `Derive(events) State`.

**Concurrency model: one mutex per run** (not a goroutine-per-run actor). Simpler to read and
test; contention is tiny (one agent per run).

```go
// Append is the ONLY path into events.jsonl (CLI ingest, spool, hub events, adapters, mock).
func (r *Run) Append(in proto.Envelope, src proto.Source) (seq int64, dup bool, err *proto.Error) {
    r.mu.Lock(); defer r.mu.Unlock()
    if r.seen[in.MsgID] { return r.seenSeq[in.MsgID], true, nil }   // A1
    if r.ended && src != proto.SrcHub { return 0, false, ErrRunEnded }
    ev := r.toStored(in, src, r.lastSeq+1)    // seq, client_seq, ts default, src
    ev.Payload = redact.Value(ev.Payload)      // before it ever touches disk or the bus
    line := marshalLine(ev)                    // one '\n'-terminated []byte
    if _, werr := r.events.Write(line); werr != nil { return 0, false, ErrStorage }  // single Write call
    r.lastSeq++; r.seen[ev.MsgID] = true; r.index = append(r.index, offset)
    r.state.Apply(ev)                          // same reducer as Derive
    r.pub.Publish(ev)                          // non-blocking (§5.3)
    return ev.Seq, false, nil
}
```

- **Single writer:** within the process, the mutex; across processes, the hub lock file
  (§5.5) guarantees only one hub per data dir. Agents never write `events.jsonl` (A3).
- **Write:** file opened `O_APPEND|O_WRONLY|O_CREATE`, each event written with one `Write`.
  **fsync policy** (config `store.fsync: never|on_end|always`, default `on_end`): a hub process
  crash loses nothing (data is in the OS cache); only an OS crash could lose the tail, and
  then the CLI's spool + `msg_id` dedupe make re-delivery safe.
- **Crash recovery:** on open, scan `events.jsonl`; if the last line has no `\n` or does not
  parse, truncate the file back to the last good newline and log a warning. Rebuild
  `lastSeq`, the `msg_id` set and derived state.
- **Memory:** keep only the `msg_id` set, derived state and a `[]int64` line-offset index;
  `Events(since)` reads lines from the file via the index. (Activity-heavy runs in Phase 5
  would otherwise grow memory without bound.)
- **Atomic file writes** (`run.json`, `outbox.offset`, inbox files): write `name.tmp-<rand>` in
  the same dir → `Sync` → `os.Rename`. On Windows, rename can fail transiently (antivirus,
  indexer holding the file): retry up to 5× with 20 ms backoff.
- **Inbox delivery** (`Deliver(type, payload) (n, err)`): validate (`ValidateInboxPayload`) →
  `n = lastInbox+1` → atomically write `inbox/%06d.json` (temp name `.tmp-*`, which the CLI's
  `^\d{6}\.json$` filter ignores) → append hub event `to_agent {n,type,payload}`. On recovery,
  any inbox file without a matching `to_agent` event gets one appended (crash between the two).
- **Tokens:** `CreateRun` returns the plaintext token once; only `sha256` hex is stored.
  `CheckToken(tok)` = `subtle.ConstantTimeCompare(sha256(tok), stored)`. Revoked when `done`
  is ingested (A10) — later phases also on exit/kill.
- **`plan --confirm`:** `Append` fills `steps` from `run.json.plan` (A5).

### 5.2 Outbox tailing (`internal/spool`)

A generic line tailer, reused in Phase 5 for adapter stdout files.

- **Polling**, not file-watch events: every `spool_poll_ms` (default 250 ms; tests 50–100 ms)
  `Stat` each live run's `outbox.jsonl`; if `size > offset`, read `[offset, size)`.
- **Startup:** all non-ended runs' outboxes are ingested **before the listener opens** (A3).
- **Complete lines only:** split on `\n`; an unterminated tail is left unread (offset not
  advanced past it) and retried next poll.
- **Normalisation per line:** strip a trailing `\r` (CRLF from Windows editors); strip a UTF-8
  BOM at the start of a line; skip empty/whitespace lines. If the bytes look like UTF-16
  (BOM `FF FE`/`FE FF` or many NULs — what Windows PowerShell 5.1's `>>` produces), skip with
  warning `SPOOL_UTF16` (docs tell hand-writers to use `Add-Content -Encoding utf8`).
- **Line cap:** lines over 1 MiB (configurable) are skipped with a warning.
- **IDs:** a line without `msg_id` gets `spool@<byte offset of line start>` (A3).
- **Validation:** parse → `proto.ParseIncoming`; `run_id`/`agent_id` must match the run;
  hub-only types rejected; failures are **hub warnings** (logged + a hub `warning` on the bus,
  not stored as run events).
- **Offset commit:** after the batch is appended, write `outbox.offset` atomically. If the hub
  dies between append and commit, the lines are re-read on restart and dedupe (real `msg_id`
  or `spool@offset`) drops them — **dedupe is what makes this crash-safe.**
- **Truncation/replacement:** if `size < offset`, the file was replaced: warn, reset offset to 0,
  rely on dedupe.
- **Late lines after `done`:** skipped with warning `RUN_ENDED`.

### 5.3 SSE fan-out (`internal/bus` + `internal/api`)

- `Broker.Publish(ev)` assigns a **global, in-memory, per-hub-instance** counter `n` and stores
  `{n, ev}` in a ring buffer (config `sse_buffer`, default 5000).
- `Subscribe(lastID)` holds the broker lock while it (a) decides replay vs reset, (b) copies the
  replay slice, (c) registers the subscriber channel — so no event can fall in the gap.
  If `lastID` is older than the ring start **or greater than the current n** (hub restarted),
  the response starts with `event: reset` and the client re-fetches `/api/state`.
- Each subscriber has a buffered channel (1024). `Publish` never blocks: if a subscriber's
  channel is full, it is dropped (its stream gets `event: reset` and closes; the browser
  reconnects).
- Stream format: headers `Content-Type: text/event-stream`, `Cache-Control: no-cache`,
  `X-Accel-Buffering: no`; first `retry: 2000`; each event `id: <n>\nevent: baton\ndata: <json>\n\n`;
  `: ping` comment every 15 s; a per-write deadline (`ResponseController.SetWriteDeadline`,
  10 s) so a stuck client cannot pin a goroutine.
- Proposed additive amendment **A12**: every stream starts with
  `event: hello` / `data: {"instance":"<hub instance id>"}`, and `/api/health` returns
  `instance` too, so a client can detect a hub restart unambiguously. (Needs your OK; §11 Q7.)

### 5.4 Auth middleware

- **Admin token** `bt_adm_<base64url(32 random bytes)>`, stored in `<data dir>/admin.token`
  (file mode 0600 on Unix; on Windows the user-profile ACL already restricts it). **Persistent**
  across hub restarts so the dashboard stays logged in; `baton hub rotate-token` replaces it.
- **Run token** (A10): looked up by the run id in the URL or body, compared in constant time.
- Comparison: always `subtle.ConstantTimeCompare` on the sha256 of both sides (equal length).
- Route classes: `none` (health), `run-or-admin` (inbox, events), `run` (ingest), `admin`
  (everything else). `?token=` accepted **only** on `GET /api/stream` (EventSource cannot send
  headers); request logging strips the query string.
- **DNS-rebinding guard:** reject requests whose `Host` is not `127.0.0.1:<port>`,
  `localhost:<port>` or `[::1]:<port>`. No CORS headers (dashboard is same-origin).
- Body size limit (`http.MaxBytesReader`, 1 MiB default, configurable); `ReadHeaderTimeout` 10 s.
- Tokens are a `type Secret string` with `LogValue()` returning `[REDACTED]`, so a token can't
  be logged by accident.

### 5.5 Daemon discovery and lifecycle (`internal/daemon`, Phase 2; lock in Phase 1)

Files in the project data dir `~/.baton/projects/<name>-<h8>/`:

| File | Content |
|---|---|
| `hub.lock` | exclusively locked for the hub's whole life (flock / LockFileEx). The OS drops the lock if the hub crashes → **the lock, not the pid, decides "is a hub running"** |
| `hub.json` | `{ pid, port, url, instance, version, exe, started_at }`, written atomically **after** outbox recovery and after the listener is open; removed on clean shutdown |
| `admin.token` | admin token (§5.4) |
| `port` | last port used, reused next time |
| `logs/hub.log` | rotated at 10 MiB, keeps 3 |

**Discovery (`baton`, `baton attach`, `baton hub stop`):** read `hub.json` → `GET /api/health`
→ running only if it answers **and** `instance` matches. Otherwise try to take `hub.lock`: if it
can be taken, the hub is dead (stale `hub.json` is deleted); if it can't, a hub is starting —
wait up to 10 s for `hub.json`.

**Project identity:** the project path is the **git top-level** of the current folder (so running
`baton` in a subfolder finds the same hub), resolved with `filepath.EvalSymlinks`, cleaned, and
lower-cased on Windows **and macOS** (both default to case-insensitive filesystems) before
hashing. Same `<name>-<h8>` scheme as `hub/paths.ts`.

**Port selection** (Q4): try the configured `hub.port` (default 7777); if taken, try the
remembered `port`, then the next 20 ports; persist the winner. `--port 0` (tests) = OS-assigned.

**Starting detached:** `baton` re-executes itself as `baton hub serve --project <path>`:
- Unix: `SysProcAttr{Setsid: true}`, stdin `/dev/null`, stdout/stderr → `logs/hub.log`,
  `cmd.Process.Release()`.
- Windows: `CreationFlags: CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW` (+
  `CREATE_BREAKAWAY_FROM_JOB`, retried without it on `ERROR_ACCESS_DENIED`). `CREATE_NO_WINDOW`
  rather than `DETACHED_PROCESS` gives the hub a hidden console that its children (git, claude,
  codex) inherit — otherwise **each child pops up its own console window**. Breakaway matters
  because some terminals (e.g. editor-integrated ones) run shells inside a kill-on-close Job
  Object, which would kill the hub when the terminal closes. *Verify both at build time.*
- The parent waits (≤10 s) for `hub.json` + healthy `/api/health`, then continues.

**Stopping:** `baton hub stop` → `POST /api/hub/shutdown` (admin) on all OSes (Windows has no
SIGTERM to send). Fallback after 10 s: kill the pid from `hub.json`, only if `hub.lock` is still
held (avoids killing a recycled pid).

### 5.6 Graceful shutdown

Triggered by `signal.NotifyContext(os.Interrupt, syscall.SIGTERM)` or `/api/hub/shutdown`:
1. stop accepting connections (`srv.Shutdown`, 5 s); SSE handlers watch a hub-wide `done`
   context and return immediately (Shutdown does not interrupt long-lived handlers by itself);
2. stop the stalled detector and tailers, do one final outbox drain;
3. `Sync` + close all `events.jsonl`, write `run.json`s;
4. (Phase 5+) agents: per Q5;
5. delete `hub.json`, release `hub.lock`, flush logs.

### 5.7 Logging, errors, "never crash"

- `log/slog` JSON to `logs/hub.log` (+ text to stderr in `baton up`), wrapped in a handler that
  runs `redact.String` over the message and every string attribute.
- **Every goroutine** starts via `safego.Go(name, fn)` which `recover()`s, logs the stack and
  publishes a hub `warning` — in Go, a panic in any background goroutine otherwise kills the
  whole process. HTTP handlers get a recover middleware returning JSON
  `500 {ok:false, code:"INTERNAL"}`.
- Config errors keep the last good config and surface the errors (API + dashboard + CLI).
- Disk errors on one run mark that run `degraded` (status flag) and keep the hub serving others.

---

## 6. Process supervision (Phase 5) and conductor (Phase 6)

### 6.1 Agents must survive a hub restart — so the hub is not their direct parent

Phase 2's acceptance ("kill the hub mid-run → agent keeps working") and the detach-on-Ctrl+C UX
mean agents outlive the hub. With plain pipes that fails: when the hub dies, the agent's stdout
pipe breaks (EPIPE) and the restarted hub cannot `Wait()` for a process it didn't start, so the
exit code is lost. Design:

- **Per-agent supervisor**: the hub spawns `baton __supervise` (same binary, hidden command),
  detached (Unix `Setsid`; Windows `CREATE_NEW_PROCESS_GROUP|CREATE_NO_WINDOW`). The hub writes
  the launch spec (argv, env, cwd) as JSON to the supervisor's **stdin and closes it** — the
  spec contains `BATON_TOKEN`, so it never touches disk or argv.
- The supervisor starts the agent with **stdin = the brief file**, **stdout → `<run>/stdout.jsonl`**,
  **stderr → `<run>/stderr.log`** (files, not pipes — they survive hub death), records
  `supervisor.json {pid, child_pid, pgid, started_at}`, waits, then writes
  `exit.json {code, signal, ended_at, duration_s}` and exits.
- The hub **tails `stdout.jsonl` with the same `spool` tailer** (byte offset in
  `stdout.offset`); adapter-derived events get `msg_id = adapter@<offset>`, so re-tailing after
  a restart never duplicates. The hub polls for `exit.json` to emit `exit` (with diff stat).
- After a hub restart, live runs are re-adopted from `supervisor.json` + liveness check
  (pid alive **and** process start time matches, to defeat pid reuse).

### 6.2 Spawning and env isolation (`internal/proc`)

- Environment is **built from an allowlist, not inherited**: `PATH`, `HOME`/`USERPROFILE`,
  `SystemRoot`, `ComSpec`, `PATHEXT`, `TEMP`/`TMP`, `APPDATA`/`LOCALAPPDATA` (Node-based CLIs need
  them on Windows), `LANG`/`LC_*`, `TERM`, plus configurable `env_passthrough`, then the
  account's `env` (with `~` expanded), then `BATON_*`. This keeps e.g. a stray
  `ANTHROPIC_API_KEY` in the user's shell from silently switching a subscription account to API
  billing, and never leaks the admin token. On Windows, keys are de-duplicated
  case-insensitively.
- The hub prepends **its own executable's directory** to the agent's `PATH` (and sets
  `BATON_BIN`), so agents call the exact same `baton` version as the hub.
- **Never pass the brief as an argument**: long text in argv hits the Windows 32 K command-line
  limit and, if the target is a `.cmd` shim, cmd.exe re-parses it (injection risk). The brief
  goes via stdin (`claude -p` and `codex exec -` read the prompt from stdin — verify flags).
  Prefer resolving native executables; if only a `.cmd`/`.bat` shim exists, Go's `os/exec`
  rejects unsafe arguments, which we surface as a clear spawn error.
- OpenCode refusal check (non-negotiable #2) runs in `Preflight` before anything is spawned.

### 6.3 Process-tree kill

| OS | Mechanism |
|---|---|
| Linux/macOS | agent started with `Setpgid: true`; supervisor on SIGTERM sends `SIGTERM` to `-pgid`, waits `kill_grace_s` (default 5), then `SIGKILL` to `-pgid`. Hub fallback: signal `-pgid` itself from `supervisor.json` |
| Windows | supervisor creates a **Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`** and assigns the agent immediately after start. Kill: hub writes `<run>/control/kill`; the supervisor (polling 500 ms) calls `TerminateJobObject` and writes `exit.json`. Fallback: hub terminates the supervisor process → its job handle closes → the OS kills the whole tree. Last resort: `taskkill /T /F /PID <child>` |

Kill-on-close is safe here because the job belongs to the tiny supervisor, not the hub — a hub
crash leaves the agent alone. The short window between start and job assignment is acceptable
(CLIs spawn children much later); *verify behaviour with a fake agent that spawns grandchildren
on all three OSes* (Phase 5 test, no quota).

### 6.4 Exit, diff stat, worktrees — via the `git` CLI

- Worktree: `git -C <repo> worktree add -b baton/<agent_id> <data dir>/worktrees/<agent_id> <base>`.
- Diff stat for `exit`: `git -C <worktree> diff --shortstat <base>...HEAD` plus
  `git status --porcelain` count for uncommitted changes.
- Cleanup / gc: `git worktree remove`, `git worktree prune`, `git branch -D` per policy.
- Windows: set `-c core.longpaths=true` on worktree commands (data dir paths are long).
- All git calls go through one helper with a timeout and `GIT_TERMINAL_PROMPT=0`.

### 6.5 Headless conductor (Phase 6)

- The conductor `claude` process is a **direct child of the hub with pipes** (stdin
  `--input-format stream-json`, stdout `--output-format stream-json`; verify exact flags with
  `claude --help`). Every line in and out is appended to `<data dir>/conductor/history.jsonl`,
  which `baton attach` replays.
- If the hub restarts, the conductor is restarted with `--resume <session_id>` (session id taken
  from its stream-json output; *verify that resume works together with stream-json input*).
  Agents keep running meanwhile (§6.1).
- **MCP transport: stdio.** The conductor's `--mcp-config` names the absolute path of the
  `baton` binary: `{"command": "<baton>", "args": ["mcp", "--project", "<path>"], "env":
  {"BATON_CONDUCTOR_TOKEN": "…"}}`. `baton mcp` is an MCP stdio server (official Go SDK) whose
  tools are **thin calls to the hub REST API** (spawn, message, answer, pause/resume/stop/kill,
  review_diff, merge, and `wait_for_events` as a long-poll `GET /api/wait`). Why stdio rather
  than the hub serving MCP over HTTP: there are reports of Claude Code not sending configured
  `headers` for HTTP MCP servers, and stdio works with every MCP client; making MCP a client of
  the REST API also guarantees the dashboard and the conductor go through the *same* checks,
  approval gates and audit log. A conductor-scoped token (not the admin token) is minted per
  conductor session.
- Terminal chat (`baton`, `baton attach`) is just another HTTP client:
  `GET /api/conductor/stream` (SSE) + `POST /api/conductor/send`. Ctrl+C closes the client only.

### 6.6 Usage parsing

Adapters' parsers extract usage from stdout lines (claude stream-json `result` event:
`total_cost_usd`, `usage.*`; codex `--json` turn-completion usage — *verify shapes at build
time*) and emit hub `usage` events. Display only.

---

## 7. Testing strategy

### 7.1 Layers

| Layer | Where | What |
|---|---|---|
| Unit | `internal/*/..._test.go` | ids, paths, redact (fixtures), proto (fixtures + struct-vs-schema), store (seq, dedupe, recovery from truncated line, inbox/to_agent repair), spool (outbox fixtures), bus (replay/reset/slow subscriber), api (httptest: auth matrix, Host guard, error shapes), cli (in-process `cli.Main` with fake env + httptest hub) |
| Timing | same, with `testing/synctest` | ask timeout → `NO_ANSWER`, heartbeat during blocking ask, stalled emitted/cleared (Phase 2) |
| E2E (black box) | `e2e/` | builds the real binary once in `TestMain` (`go build -o <tmp>/baton[.exe] ./cmd/baton`) and spawns it |
| Dashboard | `web/` | `tsc --noEmit`; optional `node --test` for pure TS modules |

### 7.2 The Phase 1 acceptance test — **Go drives it**

`e2e/lifecycle_test.go` is a 1:1 port of `test/lifecycle.test.ts`:
- the hub runs **in-process** via `hub.Start(ctx, hub.Options{DataDir: tmp, Port: 0, Dev: true,
  AdminToken: "bt_adm_test_lifecycle", SpoolPoll: 100*time.Millisecond, Logger: discard})` —
  the options mirror the TS `startHub` options deliberately;
- the agent side is the **real `baton` binary** spawned with the env returned by
  `POST /api/dev/runs` (+ `BATON_POLL_INTERVAL_S=0.1`, `BATON_HEARTBEAT_INTERVAL_S=0.3`);
- same assertions: blocking ask answered via the API while an unrelated message stays unread;
  hub closed → CLI prints `spooled …`, exit 0; a hand-written outbox line without `msg_id`;
  restart → outbox recovered, `src: spool`; second restart → seq is exactly 1..n, no duplicate
  `msg_id`, expected type sequence, `plan --confirm` filled, `to_agent` recorded, status `done`,
  step states done, token revoked, CLI still exits 0, plaintext token absent from `run.json`.
- Plus `e2e/crash_test.go`: runs `baton up --port 0 --dev` as a **separate process**, kills it
  hard (`Process.Kill` = SIGKILL / TerminateProcess) mid-run, restarts, asserts no loss/no dups.
  (Needs `baton up` to print its URL as the first stdout line and to accept `--data-dir`,
  `--admin-token-file`.)

`e2e/cli_test.go` ports `test/cli.test.ts` (spool mode for every command, usage errors exit 2,
help without env, inbox read-marking, ask timeout + heartbeats, fake hub 200/dup/500/410/400,
inbox API fallback). **Transition step:** before deleting the TS CLI, run the existing
`test/cli.test.ts` once against the Go binary by making its `BIN` configurable
(`BATON_TEST_BIN`), proving output parity with the reference.

Why Go drives: backend contributors need only Go; the in-process hub gives deterministic access
to internals; `go test ./...` is the one command. The old `test/lifecycle.test.ts` imports a TS
`startHub` that will never exist — it is replaced, not adapted.

### 7.3 Cross-platform concerns

- Tests use `t.TempDir()`, `filepath.Join`, never shell scripts; binary name gets `.exe` on
  Windows (`runtime.GOOS`).
- Windows: files can't be deleted while open — tests close everything before `TempDir` cleanup;
  Defender may scan the freshly built binary (first spawn slow) → generous timeouts.
- macOS `TMPDIR` is under `/var/folders` → symlink to `/private/var` — compare paths after
  `EvalSymlinks`.
- `-race` needs cgo and a C compiler; run it in CI on Linux/macOS, optional on Windows.
- CI (Q3): GitHub Actions matrix `windows-latest`, `macos-latest`, `ubuntu-latest`:
  `npm ci && npm run check` (§8).

---

## 8. Build, install, dev workflow

Node is still needed for the dashboard, so **npm scripts are the cross-platform task runner**
(no Makefile, no bash). One root `package.json` (dashboard deps: `vite`, `three`, `typescript`,
`json-schema-to-typescript`); `web/` is the Vite root.

| Command | Does |
|---|---|
| `npm run build:web` | `vite build web` → `web/dist` |
| `npm run build` | `build:web` then `go build -o dist/ ./cmd/baton` (binary with dashboard embedded) |
| `npm run install:baton` | `build:web` then `go install ./cmd/baton` → `$(go env GOPATH)/bin/baton[.exe]` |
| `npm run dev:web` | Vite dev server, proxies `/api` to the hub |
| `npm run gen:types` | JSON Schema → `web/src/protocol.gen.ts` |
| `npm test` | `go test ./...` |
| `npm run check` | `go vet ./...` + `go test ./...` + gen:types drift check + `tsc -p web` |
| `npm run release` (Phase 7) | `node scripts/release.mjs`: cross-compiles 6 targets with `CGO_ENABLED=0 -trimpath -ldflags "-s -w -X …version=…"`, writes checksums |

Go-only contributors can skip Node entirely: `go build ./cmd/baton` and `go test ./...` work on
a fresh clone (the dashboard shows the "not built" page).

**Getting `baton` on PATH:**
- Windows: the Go MSI installer adds `%USERPROFILE%\go\bin` to the user PATH → `baton` works in
  PowerShell, cmd and Git Bash (Git Bash resolves `baton` → `baton.exe`). No shims.
- macOS/Linux: `go install` puts it in `~/go/bin`, which the installers do **not** add to PATH;
  `install:baton` prints the one line to add to the shell profile if `baton` isn't found.
- Later (Phase 7): prebuilt release binaries — download and put on PATH.

`bin/baton.js`, the `bin` field in `package.json`, and `npm link` go away.

---

## 9. Impact on the plan

### 9.1 New phase table (replaces PLAN.md §6)

| # | Phase | Acceptance |
|---|---|---|
| 0 | Plan + repo init | done |
| 1 | **Go foundations + protocol + `baton` CLI + spool + minimal hub**: schemas frozen as master + fixtures + API schemas; `internal/proto, ids, paths, redact, store, spool, bus, api, hub, cli`; `baton up [--port] [--dev] [--data-dir]` (foreground hub, holds `hub.lock`); full A11 incl. SSE | `go test ./...` green on Windows (and on macOS/Linux CI if Q3 = yes); `e2e/lifecycle_test.go` (port of the TS test) passes; `e2e/cli_test.go` passes and the TS `test/cli.test.ts` passes against the Go binary; `baton` works from PowerShell, cmd and Git Bash after `npm run install:baton`; `baton heartbeat` with hub down returns in < 50 ms |
| 2 | **Hub core**: daemon (`hub.json`, detached spawn per OS, discovery, `baton hub stop`, `baton` start-or-connect, `baton attach` placeholder), runs + worktrees (+ git-init prompt), config (YAML, validation, polling hot-reload, timeouts, comment-preserving write-back), stalled detection, audit log, logging + redaction, dashboard placeholder served | Hard-kill the hub process mid-run → agent (CLI) keeps working → restart recovers outbox, no duplicates; `stalled` emitted and cleared (synctest); config edit applied live; invalid config reported while hub keeps running; one hub per project enforced |
| 3 | **`baton mock`** (Go), writes through the real store into a separate mock data dir; `--seed`, `--speed` | Mock drives every status the dashboard must show |
| 4 | **Dashboard** (TS): types generated from schemas, served embedded, Vite proxy in dev | as before (spec §7 against the mock); `npm run build` produces one binary with the dashboard inside |
| 5 | **Adapters** + supervisor + env allowlist + process-tree kill + usage + `baton login` | as before (real tiny task per adapter — **quota, ask first**), plus (no quota): a fake agent that spawns grandchildren is fully killed on all 3 OSes; an agent survives a hub restart and its exit code is still reported |
| 6 | **Conductor**: REST endpoints for all tools, `baton mcp` (stdio, Go SDK), headless conductor with history + resume, terminal chat, lineup flow | as before (**quota, ask first**) |
| 7 | **Hardening + release**: error paths, redaction tests, OpenCode refusal, gc, cross-compiled release binaries | as before |
| 8 | Remote access (future) | only when asked |

### 9.2 PLAN.md decisions replaced

| PLAN.md | Replaced by |
|---|---|
| §3 "Node 24+ (native TS type-stripping)" | Go (current stable) to build; end users need only the binary + git (+ the agent CLIs). Node only to build the dashboard |
| §4.4 "Validated with zod" | Go validation of config (structs + explicit checks with path-based errors) |
| §4.7 TS run by type-stripping, no build step | Go binary; `go build` |
| §4.7 `npm link` + `bin/baton.js` | `go install` / native binary on PATH |
| §4.7 tests with `node:test` | `go test` (+ `tsc` / optional `node --test` for `web/`) |
| §4.7 JSON Schemas generated from zod | JSON Schema files are the master (§2) |
| §4.7 dependency list (hono, @hono/node-server, zod, yaml, @modelcontextprotocol/sdk) | §4 table |
| §4.6 "Process-tree kill on Windows (`taskkill /T /F`)" | Job Objects via a per-agent supervisor, `taskkill` as last resort (§6.3) |
| §5 repo layout | §3 of this document |
| Kept unchanged | CLI exit codes (A9), per-run token hashing (A10), Vite dashboard, run-dir layout, data in `~/.baton/projects/<name>-<h8>/` |

### 9.3 Files in the repo

| File | Fate |
|---|---|
| `protocol/PROTOCOL.md` | stays; T1 updates the "Machine-readable schemas" line under Amendments and adds A12 if approved |
| `protocol/schema/*.json` | stays, becomes master (regenerate once from zod first) |
| `protocol/types.ts`, `scripts/gen-schemas.ts` | delete after the final regeneration |
| `hub/paths.ts` | port to `internal/paths`, then delete |
| `hub/redact.ts` | port to `internal/redact` (+ fixtures), then delete |
| `cli/baton.ts`, `bin/baton.js` | reference oracle during Phase 1, then delete (Q1) |
| `test/cli.test.ts` | used once against the Go binary (configurable BIN), ported to `e2e/cli_test.go`, then delete |
| `test/lifecycle.test.ts` | ported to `e2e/lifecycle_test.go`, then delete |
| `package.json` | keep as task runner + dashboard deps; drop `bin`, `hono`, `@hono/node-server`, `zod`, `yaml`; `engines.node` stays (dashboard build) |
| `tsconfig.json` | moves to `web/tsconfig.json` (scope = dashboard only); root one deleted |
| `.gitignore` | `dist/` → `web/dist/*` + `!web/dist/.gitkeep`, add `/dist/` (build output) |
| `package-lock.json` | regenerate after dependency changes |

---

## 10. Phase 1 work breakdown (for implementation agents)

Four tasks with disjoint file ownership. **T1 lands first** (it is small): its first commit is
the exported API below as compiling stubs, so T2–T4 can build against it immediately.
T2, T3, T4 then run in parallel. Nobody edits files outside their ownership; if a change is
needed in another task's package, ask that task's owner (or the architect).

Shared rules for all tasks: Go 1.26, `gofmt`, no cgo, no dependency beyond those listed;
cross-platform (no shell scripts, `filepath` everywhere); every exported func documented;
tests must pass on Windows locally.

### T1 — Protocol + foundations

**Owns:** `go.mod`, `go.sum`, `protocol/embed.go`, `protocol/schema/api.*.json` (new),
`protocol/fixtures/**`, `internal/proto/**`, `internal/ids/**`, `internal/paths/**`,
`internal/redact/**`. (Also: run `npm run schemas` once before starting and commit the output
unchanged as the master copy.)

**API (exact):**
```go
// package protocol
var Schemas embed.FS // schema/*.json

// package proto
const Version = 1
type Source string            // "cli" | "spool" | "hub" | "adapter"
const (SrcCLI Source = "cli"; SrcSpool = "spool"; SrcHub = "hub"; SrcAdapter = "adapter")
type RunStatus string         // starting running waiting blocked paused stalled rate_limited done failed killed exited
type PlanStep struct { ID string `json:"id"`; Title string `json:"title"`; Acceptance string `json:"acceptance"` }
type Envelope struct {        // incoming
    V int; RunID, AgentID string; Seq *int64; TS string; Type string
    Payload map[string]any; MsgID string
    Extra map[string]json.RawMessage // unknown top-level fields, preserved
} // with MarshalJSON/UnmarshalJSON handling Extra; json names per schema
type StoredEvent struct {
    V int; RunID, AgentID string; Seq int64; TS, Type string; Payload map[string]any
    MsgID string; Src Source; ClientSeq *int64; Extra map[string]json.RawMessage
}
type InboxMessage struct { V int; N int; TS, Type string; Payload map[string]any }
type RunRecord struct { /* fields of run-record.json */ }
type Error struct { Status int; Code, Msg string }   // implements error
// codes: INVALID_ENVELOPE INVALID_PAYLOAD HUB_ONLY_TYPE UNKNOWN_INBOX_TYPE UNAUTHORIZED
//        RUN_MISMATCH AGENT_MISMATCH RUN_ENDED NOT_FOUND INTERNAL
var AgentTypes, HubOnlyTypes, InboxTypes []string
func IsHubOnly(t string) bool
func ParseIncoming(b []byte) (Envelope, *Error)           // UseNumber; payload default {}; schema envelope.incoming
func ValidateAgentPayload(typ string, p map[string]any) (map[string]any, *Error)
func ValidateInboxPayload(typ string, p map[string]any) (map[string]any, *Error)
func ValidateHubPayload(typ string, p map[string]any) (map[string]any, *Error)
func MarshalLine(v any) []byte                            // compact, no HTML escaping, '\n'-terminated

// package ids
func RunID() string           // "r_" + 26-char ULID
func UUID() string            // v4
func RunToken() string        // "bt_run_" + base64url(32 bytes)
func AdminToken() string      // "bt_adm_" + base64url(32 bytes)
func QID() string             // "q_" + 6 base36
func HashToken(tok string) string            // sha256 hex
func TokenMatches(hashHex, tok string) bool  // constant time

// package paths
func BatonHome() string                       // $BATON_HOME or ~/.baton
func ProjectDataDir(repoPath, home string) string
func RunsDir(dataDir string) string; func RunDir(dataDir, runID string) string
const RunRecordFile="run.json", EventsFile="events.jsonl", OutboxFile="outbox.jsonl",
      OutboxOffsetFile="outbox.offset", InboxDir="inbox", InboxReadFile=".read"
func InboxFileName(n int) string; func ParseInboxFileName(name string) (int, bool)

// package redact
func String(s string) string
func Value(v any) any        // deep walk; sensitive keys → "[REDACTED]"; depth cap 50
```

**Acceptance:** fixtures exist for every schema (≥1 valid, ≥1 invalid per agent/inbox type;
ingest, outbox and redact cases listed in §2.3); conformance + struct-vs-schema tests pass;
`ids` tests (format, uniqueness, ULID time-sortable); `paths` tests (Windows lower-casing,
macOS lower-casing, name sanitising); redact fixtures ported from the current TS rules incl. the
lookahead rewrite (§4.1); `go vet` clean.

### T2 — Hub core (store, spool, bus, API, hub wiring)

**Owns:** `internal/store/**`, `internal/spool/**`, `internal/bus/**`, `internal/api/**`,
`internal/hub/**` (incl. lock file).

**API (exact, used by T3/T4):**
```go
// package hub
type Options struct {
    DataDir    string
    Bind       string        // default "127.0.0.1"; non-loopback refused in Phase 1
    Port       int           // 0 = OS-assigned
    Dev        bool          // enables POST /api/dev/runs
    AdminToken string        // "" = load/create <DataDir>/admin.token
    SpoolPoll  time.Duration // default 250ms
    Logger     *slog.Logger  // nil = discard
}
type Hub struct { URL, AdminToken, DataDir string /* unexported fields */ }
func Start(ctx context.Context, o Options) (*Hub, error) // lock → open store → ingest outboxes → listen
func (h *Hub) Close(ctx context.Context) error           // §5.6; idempotent
func (h *Hub) Done() <-chan struct{}                     // closed after shutdown completes
```
Internals (store `Run.Append/Deliver/Events/Inbox/Summary/CheckToken`, spool tailer, broker
`Publish/Subscribe`) as designed in §5.1–5.4; their exact shape is T2's choice.

**HTTP behaviour:** exactly A11 (routes, auth classes, status codes, error JSON), plus Host
guard, body limit, recover middleware, `event: reset` semantics, `GET /api/state` RunSummary
(`last_seq`, `last_event_ts`, `open_questions`, `step_states`). `POST /api/dev/runs` returns
`env` with `BATON_URL, BATON_TOKEN, BATON_RUN_ID, BATON_AGENT_ID, BATON_RUN_DIR, BATON_BRIEF`.

**Acceptance (unit tests in T2's packages):** seq 1..n under 50 concurrent ingests; dup msg_id
→ `duplicate:true`, stored once; truncated last line recovered; inbox file without `to_agent`
repaired on open; all `ingest/` and `outbox/` fixtures pass; ring replay vs reset (old id,
future id), slow subscriber dropped without blocking `Append`; auth matrix (no token / wrong /
other run's token / admin / revoked → expected codes); Host guard; second `hub.Start` on the
same DataDir fails with a clear "hub already running" error.

### T3 — `baton` CLI

**Owns:** `cmd/baton/**`, `internal/cli/**`.

**API:**
```go
// package cli
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int
```
**Behaviour:** identical to the reference `cli/baton.ts` and PROTOCOL §3.4 + A1–A9: every
agent command; same one-line outputs (`ok #12`, `ok #12 (dup)`, `spooled (hub unreachable)`,
`qid q_xxxxxx`, `inbox empty`, `[n] answer <qid>: <text>`, `[n] message (<from>): <text>`,
`[n] plan_update: s1 T; s2 T`, `[n] pause: <reason>`, `NO_ANSWER`); same exit codes (0 / 2;
`400` from hub → 2, `401/403/410` → 0 with `rejected` on stderr, 5xx/unreachable/3 s timeout →
spool); payload validated locally with `proto.ValidateAgentPayload` before sending; spool line
written with a single `Write` (`O_APPEND`); `.read` handling as A7; `ask --blocking` polling,
timeout, heartbeats per A8 env vars; inbox falls back to `GET /api/runs/:id/inbox` when the run
dir is missing; `plan` from `--file`, piped stdin, bare array, `--confirm` (if stdin is a
terminal and no `--file` → exit 2; stdin read capped at 10 s so a Git Bash/mintty pipe can't
hang the agent); HTTP client with `Proxy: nil`. Human commands in Phase 1: `help`, `version`,
`up [--port N] [--dev] [--data-dir D] [--admin-token-file F]` → `hub.Start`, prints
`baton hub: <url>` as the **first** stdout line, then admin token + data dir, runs until
Ctrl+C. Unknown human commands from later phases (`attach`, `mock`, `hub`, `login`, `gc`)
print "not available yet (Phase N)" and exit 2.

**Acceptance:** in-process unit tests of `Main` (argument parser edge cases, every usage
error → 2, help without env); cold start of `baton heartbeat` with hub down < 50 ms on Windows
(measured in a test log, not asserted in CI).

### T4 — E2E tests, build tooling, transition

**Owns:** `e2e/**`, `package.json` (scripts/deps edits), `.gitignore`, `scripts/*.mjs` (new
helpers only), `web/embed.go`, `web/dist/.gitkeep`, and the transitional edit to
`test/cli.test.ts` (configurable `BATON_TEST_BIN`). Deletion of obsolete TS files happens only
after the user approves Phase 1 (§9.3 list).

**Deliverables:** `e2e/main_test.go` (builds the binary once), `e2e/lifecycle_test.go` (§7.2),
`e2e/cli_test.go` (port of every case in `test/cli.test.ts`, using `httptest` fake hubs),
`e2e/crash_test.go` (separate `baton up` process, hard kill, restart); npm scripts from §8
(`build`, `install:baton`, `test`, `check`); a short "how to build/run/test" block for
`HANDOFF.md` (handed to the main session, which owns HANDOFF.md).

**Acceptance:** `go test ./...` and `npm run check` pass on Windows; `node --test
test/cli.test.ts` with `BATON_TEST_BIN=<go binary>` passes; manual check script prints
`baton version` from PowerShell, cmd and Git Bash.

### Interfaces between tasks (summary)

```
T3 cli  ──uses──▶ T1 proto, ids, paths      T3 cli (up) ──uses──▶ T2 hub.Start/Close
T2 hub  ──uses──▶ T1 proto, ids, paths, redact
T4 e2e  ──uses──▶ T2 hub.Start (in-process), the built binary from T3, fixtures from T1
```

---

## 11. Open questions for the user

1. **Go CLI too (one binary)?**
   (a) Yes — the `baton` agent CLI is rewritten in Go; the TS CLI being written now is used as
   the reference to check the Go one, then deleted. **← recommended**
   (b) Yes, but keep the TS CLI in the repo permanently as a reference implementation.
   (c) No — keep the TS CLI for agents (needs Node + npm link on every machine running agents);
   only the hub is Go.
2. **Protocol rulebook:** (a) JSON Schema files are the master; zod is removed **← recommended**;
   (b) keep zod as the authoring format and generate the schemas (protocol changes need Node);
   (c) Go structs are the master and the schemas are generated from them.
3. **Automated tests on Windows + macOS + Linux** (GitHub Actions, free for public repos), from
   Phase 1? (a) Yes **← recommended** (the project must work on all three and you develop on
   one); (b) later; (c) no.
4. **Port policy:** (a) prefer the configured port (default 7777), fall back to the next free one
   and remember it per project **← recommended**; (b) always fixed — fail if taken;
   (c) always random.
5. **(Phase 2/5) `baton hub stop` while agents are running:**
   (a) ask, then tell agents to `stop` and kill them after a grace period; `--keep-agents`
   leaves them running, and the next hub start re-attaches to them **← recommended**;
   (b) never touch agents — they always keep running and are re-attached;
   (c) always kill them.
6. **Go module name / GitHub location:** `github.com/<owner>/baton` — which owner and repo
   name? (Only you can answer; it appears in every import, so it's best fixed now.)
7. **Small additive protocol amendment A12:** SSE streams start with `event: hello
   {instance}` and `/api/health` returns `instance`, so the dashboard can tell the hub
   restarted (§5.3). Also new routes `POST /api/hub/shutdown` (admin) and, in Phase 6,
   `GET /api/wait` and `/api/conductor/*`. (a) approve **← recommended**; (b) discuss.
8. **Admin token lifetime:** (a) persistent per project (dashboard stays logged in across hub
   restarts), `baton hub rotate-token` to change it **← recommended**; (b) new token every hub
   start.

Verify at build time (not questions, reminders): current versions of `jsonschema/v6`,
`go.yaml.in/yaml/v3` (and whether v4 is final), MCP `go-sdk`; Windows `CREATE_BREAKAWAY_FROM_JOB`
behaviour and Job Object kill of grandchildren; `claude` flags for stdin prompt, stream-json
input + `--resume`, `--mcp-config` stdio form; `codex exec` stdin prompt and `--json` usage
shape.
