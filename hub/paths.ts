// Where Baton keeps runtime data. Decision (2026-11-06): in the user's home, never in the project.
//   <BATON_HOME>                       default ~/.baton (override with env BATON_HOME)
//   <BATON_HOME>/projects/<name>-<h8>/ one dir per target repo (h8 = sha256 of its real path)
//     runs/<run_id>/                   run.json, events.jsonl, outbox.jsonl, outbox.offset, inbox/
import { createHash } from 'node:crypto';
import { realpathSync } from 'node:fs';
import { homedir } from 'node:os';
import { basename, join, resolve } from 'node:path';

export function batonHome(): string {
  return process.env.BATON_HOME ? resolve(process.env.BATON_HOME) : join(homedir(), '.baton');
}

/** Stable per-project data dir for a repo path. */
export function projectDataDir(repoPath: string, home: string = batonHome()): string {
  let real = resolve(repoPath);
  try { real = realpathSync.native(real); } catch { /* path may not exist yet */ }
  const key = process.platform === 'win32' ? real.toLowerCase() : real;
  const h8 = createHash('sha256').update(key).digest('hex').slice(0, 8);
  const name = (basename(real) || 'root').replace(/[^A-Za-z0-9._-]/g, '_').slice(0, 40);
  return join(home, 'projects', `${name}-${h8}`);
}

export function runsDir(dataDir: string): string {
  return join(dataDir, 'runs');
}

export function runDir(dataDir: string, runId: string): string {
  return join(runsDir(dataDir), runId);
}

/** File layout inside one run dir. Shared by the hub and the CLI. */
export const RUN_FILES = {
  record: 'run.json',
  events: 'events.jsonl',
  outbox: 'outbox.jsonl',
  outboxOffset: 'outbox.offset',
  inboxDir: 'inbox',
  inboxRead: '.read', // inside inboxDir
} as const;

export function inboxFileName(n: number): string {
  return `${String(n).padStart(6, '0')}.json`;
}
