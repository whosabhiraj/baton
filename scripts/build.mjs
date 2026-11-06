// npm run build: build the baton binary into dist/ with the version stamped in.
import { binPath, ldflags, mkdirSync, requireGo, run } from './_lib.mjs';
import { dirname } from 'node:path';

requireGo();
mkdirSync(dirname(binPath), { recursive: true });
run('go', ['build', '-ldflags', ldflags(), '-o', binPath, './cmd/baton']);
console.log(`built ${binPath}`);

