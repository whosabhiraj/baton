// npm run check: gofmt (must be clean), go vet, then npm test.
import { capture, die, requireGo, run } from './_lib.mjs';

requireGo();
const unformatted = capture('gofmt', ['-l', '.']);
if (unformatted === null) die('gofmt failed');
if (unformatted) die(`gofmt needed on:\n${unformatted}`);
console.log('gofmt: clean');
run('go', ['vet', './...']);
run('node', ['scripts/test.mjs']);
