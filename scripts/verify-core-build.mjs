// Inspect a binary's embedded Go build metadata without executing the binary.
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { pathToFileURL } from 'node:url';
export function verifyBuildInfo(text, { goVersion, revision, os, arch }) {
  const actualGo = /^.+:\s+go(\S+)$/m.exec(text)?.[1];
  if (actualGo !== goVersion) throw new Error(`core Go version ${actualGo} differs from pinned ${goVersion}`);
  const fields = new Map([...text.matchAll(/^\s*build\s+([^=\s]+)=(.+)$/gm)].map(m => [m[1], m[2]]));
  for (const [key, want] of [['GOOS', os], ['GOARCH', arch], ['vcs.revision', revision], ['vcs.modified', 'false']]) {
    if (fields.get(key) !== want) throw new Error(`core ${key}=${fields.get(key)}; expected ${want}`);
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [binary, os, arch, output] = process.argv.slice(2);
    if (!binary || !os || !arch || !output) throw new Error('usage: verify-core-build.mjs <binary> <GOOS> <GOARCH> <report.json>');
    const goVersion = readFileSync('.go-version', 'utf8').trim();
    const revision = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
    const metadata = execFileSync('go', ['version', '-m', binary], { encoding: 'utf8' });
    verifyBuildInfo(metadata, { goVersion, revision, os, arch });
    const sha256 = createHash('sha256').update(readFileSync(binary)).digest('hex');
    writeFileSync(output, JSON.stringify({ binary, goVersion, revision, os, arch, sha256, metadata }, null, 2) + '\n');
    console.log(`verified core ${os}/${arch}: Go ${goVersion}, source ${revision}, sha256 ${sha256}`);
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
