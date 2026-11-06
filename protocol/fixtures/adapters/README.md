# Adapter fixtures

`claude-stream.jsonl` is a real `claude -p --output-format stream-json --verbose` stream
(Claude Code 2.1.319, one tiny task), reduced to the fields the parser reads. Account data,
local paths, MCP server lists and rate-limit details were removed; other event types keep only
their `type`.

`claude-stream.golden.json` holds the events the parser must produce from it. Regenerate with
`UPDATE_GOLDEN=1 go test ./internal/adapters/` after a deliberate parser change, and review
the diff.

`codex-stream.jsonl` is hand-written to the expected shape of `codex exec --json` (codex-cli
0.167.0) and is only an approximation. Replace it with a real capture (sanitized like the claude
one) and regenerate `codex-stream.golden.json` once one exists.

`opencode-stream.jsonl` is hand-written to the shape of `opencode run --format json` (opencode
1.19.4: one `{type, timestamp, sessionID, part}` object per line; event names read from the
binary, not from a captured run) and is only an approximation. Replace it with a real capture
(sanitized, API-key provider only) and regenerate `opencode-stream.golden.json` once one exists.
