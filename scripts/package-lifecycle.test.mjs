import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { chmod, link, mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';

const helper = '/usr/lib/tenebra/tenebra-helper';
const standaloneUnit = '/etc/systemd/system/tenebra.service';
const packageUnit = '/usr/lib/systemd/system/tenebra.service';
const unit = await readFile(new URL('../internal/installtx/systemd/tenebra.service', import.meta.url));

async function fixture(t) {
  assert.equal(typeof process.getuid, 'function');
  assert.notEqual(process.getuid(), 0, 'script fixtures run without root');
  const parent = await realpath(tmpdir());
  const root = await mkdtemp(join(parent, 'tenebra-package-test-'));
  t.after(async () => {
    assert.equal(await realpath(root), root);
    assert.equal(dirname(root), parent);
    await rm(root, { recursive: true, force: true });
  });
  const bin = join(root, 'bin');
  await mkdir(bin);
  const trace = join(root, 'trace');
  const request = join(root, 'request.json');
  await writeFile(trace, '');
  for (const [name, body] of [
    ['systemctl', 'printf "systemctl %s\\n" "$*" >> "$TRACE"\nexit "${SYSTEMCTL_EXIT:-0}"'],
    ['helper', 'printf "helper %s\\n" "$*" >> "$TRACE"\ncase "$*" in *--request-stdin*) cat > "$REQUEST" ;; esac\nexit "${HELPER_EXIT:-0}"'],
    ['stat', 'if [ "$1" = -c ] && [ "$2" = %u ]; then printf "%s\\n" "${FIXTURE_UID:-0}"; else exec /usr/bin/stat "$@"; fi'],
  ]) {
    await writeFile(join(bin, name), '#!/bin/sh\nset -eu\n' + body + '\n', { mode: 0o755 });
  }
  async function run(name, args, environment = {}) {
    let body = await readFile(new URL('../packaging/debian/' + name, import.meta.url), 'utf8');
    body = body.replaceAll(helper, join(bin, 'helper'))
      .replaceAll(standaloneUnit, join(root, 'standalone.service'))
      .replaceAll(packageUnit, join(root, 'package.service'));
    const result = spawnSync('/bin/sh', ['-c', body, name, ...args], {
      encoding: 'utf8', timeout: 2000,
      env: { PATH: `${bin}:/usr/bin:/bin`, TRACE: trace, REQUEST: request, ...environment },
    });
    assert.equal(result.error, undefined);
    assert.equal(result.signal, null);
    return result;
  }
  return { root, run, trace: () => readFile(trace, 'utf8'), request };
}

test('package preflight recognizes the exact protected unit', async t => {
  const f = await fixture(t);
  assert.equal((await f.run('preinst', ['install'])).status, 0);
  await writeFile(join(f.root, 'package.service'), unit, { mode: 0o644 });
  assert.equal((await f.run('preinst', ['upgrade', '0.6.0', '0.6.1'])).status, 0);
  assert.equal(await f.trace(), '');
  const source = await readFile(new URL('../packaging/debian/preinst', import.meta.url), 'utf8');
  assert.ok(source.includes(createHash('sha256').update(unit).digest('hex')));
});

for (const problem of ['standalone', 'custom', 'symlink', 'hardlink', 'writable', 'foreign-owner']) {
  test(`package preflight preserves ${problem} configuration`, async t => {
    const f = await fixture(t);
    const target = join(f.root, 'package.service');
    if (problem === 'standalone') {
      await writeFile(join(f.root, 'standalone.service'), 'foreign');
    } else if (problem === 'symlink') {
      await writeFile(join(f.root, 'other.service'), unit);
      await symlink(join(f.root, 'other.service'), target);
    } else {
      await writeFile(target, problem === 'custom' ? 'foreign' : unit, { mode: 0o644 });
      if (problem === 'hardlink') await link(target, join(f.root, 'alias'));
      if (problem === 'writable') await chmod(target, 0o666);
    }
    const before = problem === 'standalone' ? null : await readFile(target);
    const result = await f.run('preinst', ['upgrade'], problem === 'foreign-owner' ? { FIXTURE_UID: '1000' } : {});
    assert.equal(result.status, 1);
    if (before) assert.deepEqual(await readFile(target), before);
    assert.equal(await f.trace(), '');
  });
}

test('configure calls only the canonical package staging entry', async t => {
  const f = await fixture(t);
  for (let count = 0; count < 2; count++) assert.equal((await f.run('postinst', ['configure'])).status, 0);
  assert.equal(await f.trace(), ('systemctl --system --no-ask-password daemon-reload\nhelper stage-package --manager=dpkg\n').repeat(2));
});

test('upgrade and its unwind never stop or restart a service', async t => {
  const f = await fixture(t);
  for (const [name, action] of [
    ['prerm', 'upgrade'], ['prerm', 'failed-upgrade'], ['postrm', 'upgrade'],
    ['postrm', 'failed-upgrade'], ['postinst', 'abort-upgrade'], ['preinst', 'abort-upgrade'],
  ]) assert.equal((await f.run(name, [action, '0.6.2'])).status, 0);
  assert.equal(await f.trace(), '');
});

test('remove submits a fresh bounded keep-profiles request to the helper', async t => {
  const f = await fixture(t);
  const ids = new Set();
  for (let count = 0; count < 2; count++) {
    assert.equal((await f.run('prerm', ['remove'])).status, 0);
    const request = JSON.parse(await readFile(f.request, 'utf8'));
    assert.deepEqual(Object.keys(request).sort(), ['action', 'keep_profiles', 'operation_id', 'schema']);
    assert.equal(request.schema, 1);
    assert.equal(request.action, 'remove');
    assert.equal(request.keep_profiles, true);
    assert.match(request.operation_id, /^so1_[a-f0-9]{32}$/);
    ids.add(request.operation_id);
  }
  assert.equal(ids.size, 2);
  assert.equal(await f.trace(), 'helper remove --keep-profiles --request-stdin\n'.repeat(2));
});

test('post-remove and purge retain profile data', async t => {
  const f = await fixture(t);
  const profiles = join(f.root, 'profiles.json');
  await writeFile(profiles, 'synthetic profile bytes');
  for (const action of ['remove', 'purge']) assert.equal((await f.run('postrm', [action])).status, 0);
  assert.equal(await readFile(profiles, 'utf8'), 'synthetic profile bytes');
  assert.equal(await f.trace(), 'systemctl --system --no-ask-password daemon-reload\n'.repeat(2));
});

test('maintainer scripts propagate helper and native manager failures', async t => {
  const f = await fixture(t);
  assert.equal((await f.run('postinst', ['configure'], { HELPER_EXIT: '23' })).status, 23);
  assert.equal((await f.run('postinst', ['configure'], { SYSTEMCTL_EXIT: '17' })).status, 17);
  assert.equal((await f.run('prerm', ['remove'], { HELPER_EXIT: '23' })).status, 23);
  for (const name of ['preinst', 'postinst', 'prerm', 'postrm']) assert.equal((await f.run(name, ['unknown'])).status, 1);
});
