// npm run check: gofmt (must be clean), go vet, dashboard types in sync + typecheck, then npm test.
import { capture, die, hasWebDeps, requireGo, run, runTool } from './_lib.mjs';

requireGo();
const unformatted = capture('gofmt', ['-l', '.']);
if (unformatted === null) die('gofmt failed');
if (unformatted) die(`gofmt needed on:\n${unformatted}`);
console.log('gofmt: clean');
run('go', ['vet', './...']);
if (hasWebDeps()) {
  run('node', ['scripts/gen-types.mjs']);
  if (capture('git', ['diff', '--quiet', '--', 'web/src/protocol.gen.ts']) === null)
    die('web/src/protocol.gen.ts is out of date: run `npm run gen:types` and commit it');
  runTool('typescript', 'bin/tsc', ['-p', 'web']);
  console.log('dashboard: types in sync, typecheck clean');
} else {
  console.log('dashboard: skipped (run `npm install` first)');
}
run('node', ['scripts/test.mjs']);
