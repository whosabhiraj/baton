// npm run install:baton: go install, then say where it landed and how to put it on PATH.
import { existsSync } from 'node:fs';
import { delimiter, join } from 'node:path';
import { capture, exe, ldflags, requireGo, run } from './_lib.mjs';

requireGo();
run('go', ['install', '-ldflags', ldflags(), './cmd/baton']);
const bin = capture('go', ['env', 'GOBIN']) || join(capture('go', ['env', 'GOPATH']).split(delimiter)[0], 'bin');
const target = join(bin, `baton${exe}`);
console.log(`installed ${target}${existsSync(target) ? '' : ' (not found?)'}`);
const norm = (p) => {
  const t = p.trim().replace(/[\\/]+$/, "");
  return process.platform === "win32" ? t.toLowerCase() : t;
};
const has = (pathList) => (pathList ?? "").split(delimiter).some((p) => p && norm(p) === norm(bin));
// Full path: powershell.exe is not always on PATH (e.g. in some Git Bash setups).
const powershell = join(process.env.SystemRoot ?? "C:\\Windows", "System32", "WindowsPowerShell", "v1.0", "powershell.exe");
if (has(process.env.PATH)) {
  console.log("on PATH: run `baton version` to check");
} else if (process.platform === "win32" && has(capture(powershell, ["-NoProfile", "-Command",
  "[Environment]::GetEnvironmentVariable('Path','User') + ';' + [Environment]::GetEnvironmentVariable('Path','Machine')"]))) {
  // Saved PATH has it; this terminal (or the app that launched it) started before it was added.
  console.log(`${bin} is on your saved PATH but not in this terminal yet: restart the terminal app (all windows).`);
} else {
  console.log(`${bin} is not on your PATH. Add it:`);
  if (process.platform === "win32") console.log(`  PowerShell: [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path','User') + ';${bin}', 'User')   # then restart the terminal app`);
  else console.log(`  echo 'export PATH="$PATH:${bin}"' >> ~/.profile   # then restart the shell`);
}
