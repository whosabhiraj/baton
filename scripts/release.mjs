// npm run release: build the dashboard once, then cross-compile baton for every target into
// dist/release/<version>/ with a SHA256SUMS file. Static binaries (CGO off), no local paths.
// On a commit tagged v<package.json version> the version is plain; otherwise it gets +<sha>.
import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { buildWeb, capture, die, hasWebDeps, mkdirSync, requireGo, root, run } from './_lib.mjs';

const TARGETS = [
  ['windows', 'amd64'], ['windows', 'arm64'],
  ['darwin', 'amd64'], ['darwin', 'arm64'],
  ['linux', 'amd64'], ['linux', 'arm64'],
];

requireGo();
if (!hasWebDeps()) die('run `npm install` first: a release must include the dashboard');

const pkgVersion = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')).version;
const tag = capture('git', ['describe', '--exact-match', '--tags', 'HEAD']);
const sha = capture('git', ['rev-parse', '--short', 'HEAD']);
const dirty = (capture('git', ['status', '--porcelain', '--untracked-files=no']) ?? '') !== '';
let version = tag === `v${pkgVersion}` ? pkgVersion : `${pkgVersion}+${sha ?? 'unknown'}`;
if (dirty) {
  version += '.dirty';
  console.log('note: uncommitted changes; the version is marked .dirty');
}

const out = join(root, 'dist', 'release', version.replace(/\+/g, '_'));
rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });

buildWeb();
const ldflags = `-s -w -X github.com/whosabhiraj/baton/internal/cli.Version=${version}`;
for (const [goos, goarch] of TARGETS) {
  const name = `baton_${version.replace(/\+/g, '_')}_${goos}_${goarch}${goos === 'windows' ? '.exe' : ''}`;
  run('go', ['build', '-trimpath', '-ldflags', ldflags, '-o', join(out, name), './cmd/baton'], {
    env: { ...process.env, CGO_ENABLED: '0', GOOS: goos, GOARCH: goarch },
  });
}

const sums = readdirSync(out)
  .filter((f) => f.startsWith('baton_'))
  .sort()
  .map((f) => `${createHash('sha256').update(readFileSync(join(out, f))).digest('hex')}  ${f}`);
writeFileSync(join(out, 'SHA256SUMS'), sums.join('\n') + '\n');
console.log(`release ${version}: ${sums.length} binaries + SHA256SUMS in ${out}`);
