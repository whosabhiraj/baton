// npm test: all Go tests (unit, protocol conformance, end-to-end), then a version-stamped build.
import { requireGo, run } from './_lib.mjs';

requireGo();
run('go', ['test', './...']);
run('node', ['scripts/build.mjs']);
