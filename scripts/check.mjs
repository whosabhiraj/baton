// npm run check: gofmt (must be clean), go vet, dashboard types in sync + typecheck, then npm test.
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { capture, die, hasWebDeps, requireGo, root, run, runTool } from './_lib.mjs';

requireGo();
// Like go's own ./..., skip dot folders (e.g. agent worktrees under .claude/) and node_modules.
const goDirs = readdirSync(root, { withFileTypes: true })
  .filter((d) => d.isDirectory() && !d.name.startsWith('.') && d.name !== 'node_modules')
  .map((d) => d.name);
const unformatted = capture('gofmt', ['-l', ...goDirs]);
if (unformatted === null) die('gofmt failed');
if (unformatted) die(`gofmt needed on:\n${unformatted}`);
console.log('gofmt: clean');
run('go', ['vet', './...']);
if (hasWebDeps()) {
  const gen = join(root, 'web', 'src', 'protocol.gen.ts');
  const before = existsSync(gen) ? readFileSync(gen, 'utf8') : '';
  run('node', ['scripts/gen-types.mjs']);
  if (readFileSync(gen, 'utf8') !== before) die('web/src/protocol.gen.ts was out of date (now regenerated): review and commit it');
  runTool('typescript', 'bin/tsc', ['-p', 'web']);
  console.log('dashboard: types in sync, typecheck clean');
} else {
  console.log('dashboard: skipped (run `npm install` first)');
}
run('node', ['scripts/test.mjs']);
