# Adapter fixtures

`claude-stream.jsonl` is a hand-written approximation of `claude -p --output-format stream-json
--verbose` output. Replace it with a captured real stream in the first real run, then update
`claude-stream.golden.json` (the events the parser must produce) to match.

It deliberately includes a non-JSON line and an unknown event type (both must be ignored) and two
assistant lines with the same message id (usage must not be double counted).
