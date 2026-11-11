import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { appendFile, mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { gunzipSync } from 'node:zlib';
import { prepareServicePayload } from './prepare-service-payload.mjs';

const roles = ['geoip-ru.srs', 'geosite-ads.srs', 'geosite-ru.srs', 'sing-box', 'tenebra-core'];
const digest = bytes => createHash('sha256').update(bytes).digest('hex');

async function fixture(t) {
  const parent = await realpath(tmpdir());
  const root = await mkdtemp(join(parent, 'tenebra-payload-'));
  t.after(async () => {
    const resolved = await realpath(root);
    assert.equal(resolved, root);
    assert.equal(dirname(resolved), parent);
    await rm(resolved, { recursive: true, force: true });
  });
  const input = join(root, 'input');
  await mkdir(input);
  for (const role of roles) await writeFile(join(input, role), `fixture ${role}\n`);
  return { root, input, target: 'darwin-universal', version: '0.6.1-beta.1', sourceSha: 'a'.repeat(40) };
}

test('hashes sealed payload bytes before compiling and hashes the sealed helper afterward', async t => {
  const request = await fixture(t);
  const events = [];
  const ports = {
    async seal(path, role, target) {
      assert.equal(target, 'darwin-universal');
      events.push(`seal ${role}`);
      await appendFile(path, `sealed ${role}\n`);
    },
    async buildHelper(path, { target, payloadSha, archiveSha }) {
      events.push('compile helper');
      const dir = dirname(path);
      const body = await readFile(join(dir, 'payload/payload.json'));
      const manifest = JSON.parse(body);
      assert.equal(target, manifest.target);
      assert.deepEqual(manifest.files.map(file => file.name), roles);
      assert.equal(payloadSha, digest(body));
      assert.equal(archiveSha, digest(await readFile(join(dir, 'payload.tar.gz'))));
      for (const entry of manifest.files) {
        const bytes = await readFile(join(dir, 'payload', entry.name));
        assert.equal(entry.sha256, digest(bytes));
        assert.equal(entry.size, bytes.length);
      }
      await writeFile(path, `bound ${payloadSha} ${archiveSha}\n`);
    },
  };
  const output = join(request.root, 'result');
  const binding = await prepareServicePayload({ ...request, output }, ports);
  assert.deepEqual(events, ['seal sing-box', 'seal tenebra-core', 'compile helper', 'seal tenebra-helper']);
  assert.deepEqual(JSON.parse(await readFile(join(output, 'binding.json'))), binding);
  const sealedHelper = await readFile(join(output, 'tenebra-helper'));
  const blob = await readFile(join(output, 'helper.gz'));
  const archive = await readFile(join(output, 'payload.tar.gz'));
  assert.deepEqual(gunzipSync(blob), sealedHelper);
  assert.equal(binding.helper_sha256, digest(sealedHelper));
  assert.equal(binding.helper_blob_sha256, digest(blob));
  assert.equal(binding.payload_archive_sha256, digest(archive));
  assert.equal(blob.readUInt32LE(4), 0);
  assert.equal(archive.readUInt32LE(4), 0);
  assert.match(gunzipSync(archive).toString('utf8'), /fixture tenebra-core\nsealed tenebra-core\n/);
  const repeated = join(request.root, 'repeat');
  assert.deepEqual(await prepareServicePayload({ ...request, output: repeated }, ports), binding);
  assert.deepEqual(await readFile(join(repeated, 'payload.tar.gz')), archive);
  assert.deepEqual(await readFile(join(repeated, 'helper.gz')), blob);
});

test('rejects a wrong target or missing role before compiling the helper', async t => {
  const request = await fixture(t);
  const ports = { seal() {}, buildHelper() { assert.fail('unverified inputs reached helper build'); } };
  await assert.rejects(prepareServicePayload({ ...request, target: 'darwin-amd64', output: join(request.root, 'bad-target') }, ports), /invalid service payload/);
  await assert.rejects(prepareServicePayload({ ...request, version: undefined, output: join(request.root, 'no-version') }, ports), /invalid service payload/);
  await rm(join(request.input, 'geosite-ru.srs'));
  await assert.rejects(prepareServicePayload({ ...request, output: join(request.root, 'missing') }, ports), { code: 'ENOENT' });
});

test('the explicit sidecar core is copied into the Linux package payload', async t => {
  const request = await fixture(t);
  const core = join(request.root, 'verified-sidecar');
  await writeFile(core, 'explicit sidecar core bytes\n');
  const output = join(request.root, 'linux-package');
  const ports = {
    seal() {},
    async buildHelper(path) { await writeFile(path, 'synthetic helper\n'); },
  };
  await prepareServicePayload({ ...request, core, output, target: 'linux-amd64' }, ports);
  assert.deepEqual(await readFile(join(output, 'payload/tenebra-core')), await readFile(core));
  const manifest = JSON.parse(await readFile(join(output, 'payload/payload.json'), 'utf8'));
  assert.equal(manifest.files.find(file => file.name === 'tenebra-core').sha256, digest(await readFile(core)));
  await assert.rejects(prepareServicePayload({ ...request, core: 7, output: join(request.root, 'invalid') }, ports), /invalid service payload/);
});
