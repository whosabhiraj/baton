// npm test: Go tests, then the TS reference CLI tests pointed at the freshly built Go binary.
import { binPath, requireGo, run } from './_lib.mjs';

requireGo();
run('go', ['test', './...']);
run('node', ['scripts/build.mjs']);
run('node', ['--test', 'test/cli.test.ts'], { env: { ...process.env, BATON_TEST_BIN: binPath } });
