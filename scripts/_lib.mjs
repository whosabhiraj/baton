// Shared helpers for the npm task scripts (cross-platform; no shell).
import { spawnSync } from 'node:child_process';
import { readFileSync, mkdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
export const exe = process.platform === 'win32' ? '.exe' : '';
export const binPath = join(root, 'dist', `baton${exe}`);
const VERSION_PKG = 'github.com/whosabhiraj/baton/internal/cli.Version';

/** Run a command inheriting stdio; exit the process on failure. */
export function run(cmd, args, opts = {}) {
  console.log(`> ${cmd} ${args.join(' ')}`);
  const r = spawnSync(cmd, args, { cwd: root, stdio: 'inherit', ...opts });
  if (r.error) die(`${cmd}: ${r.error.message}`);
  if (r.status !== 0) process.exit(r.status ?? 1);
}

/** Run a command and capture stdout (trimmed); returns null on failure. */
export function capture(cmd, args) {
  const r = spawnSync(cmd, args, { cwd: root, encoding: 'utf8' });
  return r.status === 0 ? r.stdout.trim() : null;
}

export function die(msg) {
  console.error(`error: ${msg}`);
  process.exit(1);
}

export function requireGo() {
  if (capture('go', ['version']) === null) die('`go` not found on PATH (install Go: https://go.dev/dl/)');
}

/** package.json version, plus the short git hash when available. */
export function version() {
  const v = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')).version;
  const sha = capture('git', ['rev-parse', '--short', 'HEAD']);
  return sha ? `${v}+${sha}` : v;
}

export const ldflags = () => `-X ${VERSION_PKG}=${version()}`;
export { mkdirSync };
