// npm run build: build the dashboard, then the baton binary (dashboard embedded) into dist/.
import { binPath, buildWeb, ldflags, mkdirSync, requireGo, run } from './_lib.mjs';
import { dirname } from 'node:path';

requireGo();
buildWeb();
mkdirSync(dirname(binPath), { recursive: true });
run('go', ['build', '-ldflags', ldflags(), '-o', binPath, './cmd/baton']);
console.log(`built ${binPath}`);

