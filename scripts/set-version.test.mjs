import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync, copyFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { spawnSync } from 'node:child_process';

test('version checks include both npm lockfile copies and preserve dependency versions', t => {
  const root = mkdtempSync(join(tmpdir(), 'tenebra-version-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const files = {
    'ui-desktop/package.json': '{"name":"tenebra-desktop","version":"0.5.11"}',
    'ui-desktop/package-lock.json': JSON.stringify({
      name: 'tenebra-desktop', version: '0.1.1', lockfileVersion: 3,
      packages: { '': { name: 'tenebra-desktop', version: '0.5.11' }, 'node_modules/example': { version: '2.3.4' } },
    }),
    'ui-desktop/src-tauri/tauri.conf.json': '{"version":"0.5.11"}',
    'ui-desktop/src-tauri/Cargo.toml': 'version = "0.5.11"\n',
    'ui-desktop/src-tauri/Cargo.lock': 'name = "tenebra-desktop"\nversion = "0.5.11"\n',
    'core/buildinfo/buildinfo.go': 'const Version = "0.5.11"\n',
    'packaging/arch/PKGBUILD': 'pkgver=0.5.11\n',
  };
  for (const [file, content] of Object.entries(files)) {
    mkdirSync(dirname(join(root, file)), { recursive: true });
    writeFileSync(join(root, file), content);
  }
  mkdirSync(join(root, 'scripts'));
  copyFileSync(new URL('./set-version.mjs', import.meta.url), join(root, 'scripts/set-version.mjs'));
  const run = (...args) => spawnSync(process.execPath, [join(root, 'scripts/set-version.mjs'), ...args], { encoding: 'utf8' });
  const stale = run('--check');
  assert.equal(stale.status, 1, stale.stdout + stale.stderr);
  const update = run('v0.6.0');
  assert.equal(update.status, 0, update.stdout + update.stderr);
  const lock = JSON.parse(readFileSync(join(root, 'ui-desktop/package-lock.json'), 'utf8'));
  assert.equal(lock.version, '0.6.0');
  assert.equal(lock.packages[''].version, '0.6.0');
  assert.equal(lock.packages['node_modules/example'].version, '2.3.4');
  const check = run('0.6.0', '--check');
  assert.equal(check.status, 0, check.stdout + check.stderr);
  lock.packages[''].version = '0.5.11';
  writeFileSync(join(root, 'ui-desktop/package-lock.json'), JSON.stringify(lock));
  assert.equal(run('--check').status, 1, 'a stale root package entry must also fail');
});
