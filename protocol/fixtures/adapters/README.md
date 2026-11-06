# Adapter fixtures

`claude-stream.jsonl` is a real `claude -p --output-format stream-json --verbose` stream
(Claude Code 2.1.319, one tiny task), reduced to the fields the parser reads. Account data,
local paths, MCP server lists and rate-limit details were removed; other event types keep only
their `type`.

`claude-stream.golden.json` holds the events the parser must produce from it. Regenerate with
`UPDATE_GOLDEN=1 go test ./internal/adapters/` after a deliberate parser change, and review
the diff.
