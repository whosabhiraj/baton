// npm run install:baton: go install, then say where it landed and how to put it on PATH.
import { existsSync } from 'node:fs';
import { delimiter, join } from 'node:path';
import { capture, exe, ldflags, requireGo, run } from './_lib.mjs';

requireGo();
run('go', ['install', '-ldflags', ldflags(), './cmd/baton']);
const bin = capture('go', ['env', 'GOBIN']) || join(capture('go', ['env', 'GOPATH']).split(delimiter)[0], 'bin');
const target = join(bin, `baton${exe}`);
console.log(`installed ${target}${existsSync(target) ? '' : ' (not found?)'}`);
const onPath = (process.env.PATH ?? '').split(delimiter).some((p) => p && p.replace(/[\/]+$/, '') === bin.replace(/[\/]+$/, ''));
if (!onPath) {
  console.log(`${bin} is not on your PATH. Add it:`);
  if (process.platform === 'win32') console.log(`  PowerShell: [Environment]::SetEnvironmentVariable('Path', $env:Path + ';${bin}', 'User')`);
  else console.log(`  echo 'export PATH="$PATH:${bin}"' >> ~/.profile   # then restart the shell`);
}
