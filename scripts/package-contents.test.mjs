import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { promisify } from 'node:util';
import { linuxPackageConfig, withBuildConfig } from './tauri-build.mjs';

const repository = new URL('../', import.meta.url);
const readJSON = async path => JSON.parse(await readFile(new URL(path, repository), 'utf8'));
const run = promisify(execFile);
const hash = bytes => createHash('sha256').update(bytes).digest('hex');

function tarEntries(body) {
  const entries = new Map();
  const text = bytes => new TextDecoder('utf-8', { fatal: true }).decode(bytes).split('\0', 1)[0];
  const number = bytes => {
    const value = text(bytes).trim();
    assert.match(value, /^[0-7]+$/);
    return Number.parseInt(value, 8);
  };
  let longName;
  for (let offset = 0; offset + 512 <= body.length;) {
    const header = body.subarray(offset, offset + 512);
    if (header.every(byte => byte === 0)) break;
    const expected = number(header.subarray(148, 156));
    const checksum = header.reduce((sum, byte, index) => sum + (index >= 148 && index < 156 ? 32 : byte), 0);
    assert.equal(checksum, expected, 'tar header checksum');
    const size = number(header.subarray(124, 136));
    const end = offset + 512 + size;
    assert.ok(Number.isSafeInteger(size) && size >= 0 && end <= body.length);
    const content = body.subarray(offset + 512, end);
    const type = header[156];
    offset += 512 + Math.ceil(size / 512) * 512;
    if (type === 76) {
      assert.ok(size < 65536 && longName === undefined);
      longName = text(content);
      continue;
    }
    const ustar = header.subarray(257, 263).equals(Buffer.from('ustar\0'));
    const prefix = ustar ? text(header.subarray(345, 500)) : '';
    let name = longName ?? ((prefix ? prefix + '/' : '') + text(header.subarray(0, 100)));
    longName = undefined;
    name = name.replace(/^\.\//, '').replace(/\/$/, '');
    if (!name || name === '.') continue;
    assert.ok(!name.startsWith('/') && !name.split('/').includes('..') && !entries.has(name), 'bounded unique relative tar entry');
    assert.ok([0, 48, 49, 50, 53].includes(type), 'supported GNU tar entry');
    entries.set(name, { content, type, mode: number(header.subarray(100, 108)) & 0o7777,
      uid: number(header.subarray(108, 116)), gid: number(header.subarray(116, 124)) });
  }
  assert.equal(longName, undefined);
  return entries;
}

function regular(entries, name, mode) {
  const file = entries.get(name);
  assert.ok(file, 'package is missing ' + name);
  assert.ok(file.type === 0 || file.type === 48, name + ' must be a regular file');
  assert.deepEqual([file.mode, file.uid, file.gid], [mode, 0, 0], name + ' ownership and mode');
  return file.content;
}

function elf(bytes) {
  assert.ok(bytes.length >= 20);
  assert.deepEqual(bytes.subarray(0, 6), Buffer.from([0x7f, 0x45, 0x4c, 0x46, 2, 1]));
  assert.equal(bytes.readUInt16LE(18), 62);
  assert.ok([2, 3].includes(bytes.readUInt16LE(16)));
}

async function verifyCompletePackage(data, control) {
  const files = tarEntries(data);
  const scripts = tarEntries(control);
  const controlText = regular(scripts, 'control', 0o644).toString('utf8');
  const dependencyLine = /^Depends: (.*)$/m.exec(controlText);
  assert.ok(dependencyLine, 'package dependency metadata');
  const dependencies = dependencyLine[1].split(',').map(value => value.trim().split(/[ (]/, 1)[0]);
  for (const dependency of ['systemd', 'pkexec', 'libwebkit2gtk-4.1-0']) {
    assert.ok(dependencies.includes(dependency), 'package dependency ' + dependency);
  }
  elf(regular(files, 'usr/bin/tenebra-desktop', 0o755));
  elf(regular(files, 'usr/lib/tenebra/tenebra-helper', 0o755));
  const payload = 'usr/lib/tenebra/package-payload/';
  const manifestBytes = regular(files, payload + 'payload.json', 0o644);
  assert.ok(manifestBytes.length <= 65536);
  const manifest = JSON.parse(manifestBytes.toString('utf8'));
  assert.equal(JSON.stringify(manifest, null, 2) + '\n', manifestBytes.toString('utf8'));
  assert.deepEqual(Object.keys(manifest), ['schema', 'kind', 'version', 'source_sha', 'target', 'files']);
  assert.equal(manifest.schema, 1);
  assert.equal(manifest.kind, 'tenebra-install-payload');
  assert.equal(manifest.target, 'linux-amd64');
  assert.match(manifest.source_sha, /^[a-f0-9]{40}$/);
  assert.equal(manifest.version, (await readJSON('ui-desktop/src-tauri/tauri.conf.json')).version);
  assert.deepEqual(manifest.files.map(file => file.name),
    ['geoip-ru.srs', 'geosite-ads.srs', 'geosite-ru.srs', 'sing-box', 'tenebra-core']);
  for (const entry of manifest.files) {
    assert.deepEqual(Object.keys(entry), ['name', 'sha256', 'size', 'mode']);
    const mode = entry.name.endsWith('.srs') ? 0o644 : 0o755;
    assert.equal(entry.mode, mode);
    const content = regular(files, payload + entry.name, mode);
    assert.equal(entry.size, content.length);
    assert.equal(entry.sha256, hash(content));
    if (!entry.name.endsWith('.srs')) elf(content);
  }
  for (const [name, source] of [
    ['usr/lib/systemd/system/tenebra.service', 'internal/installtx/systemd/tenebra.service'],
    ['usr/share/polkit-1/actions/ru.tenebra.maintenance.policy', 'deploy/linux/ru.tenebra.maintenance.policy'],
    ['usr/lib/modules-load.d/tenebra.conf', 'deploy/linux/tenebra.conf'],
  ]) {
    assert.deepEqual(regular(files, name, 0o644), await readFile(new URL(source, repository)));
  }
  for (const name of ['preinst', 'postinst', 'prerm', 'postrm']) {
    assert.deepEqual(regular(scripts, name, 0o755), await readFile(new URL('packaging/debian/' + name, repository)));
  }
  const desktopFiles = [...files.keys()].filter(name => name.startsWith('usr/share/applications/') && name.endsWith('.desktop'));
  assert.equal(desktopFiles.length, 1);
  const desktop = regular(files, desktopFiles[0], 0o644).toString('utf8');
  assert.match(desktop, /^Exec=tenebra-desktop %u$/m);
  assert.match(desktop, /^MimeType=.*x-scheme-handler\/tenebra/m);
}

test('Linux package template includes the fixed helper and full payload', async () => {
  const config = await readJSON('ui-desktop/src-tauri/tauri.linux.conf.json');
  assert.equal(config.bundle.linux.deb.files?.['/usr/lib/tenebra/tenebra-helper'],
    'resources/service-payload/package/tenebra-helper');
  assert.equal(config.bundle.linux.deb.files?.['/usr/lib/tenebra/package-payload'],
    'resources/service-payload/package/payload');
});

test('Linux package template includes the canonical host and administrative policy', async () => {
  const config = await readJSON('ui-desktop/src-tauri/tauri.linux.conf.json');
  assert.equal(config.bundle.linux.deb.files?.['/usr/lib/systemd/system/tenebra.service'],
    '../../internal/installtx/systemd/tenebra.service');
  assert.equal(config.bundle.linux.deb.files?.['/usr/share/polkit-1/actions/ru.tenebra.maintenance.policy'],
    '../../deploy/linux/ru.tenebra.maintenance.policy');
  assert.equal(config.bundle.linux.deb.files?.['/usr/lib/modules-load.d/tenebra.conf'],
    '../../deploy/linux/tenebra.conf');
  assert.deepEqual(config.bundle.linux.deb.depends, ['systemd', 'pkexec']);
});

test('Linux package template runs the actual protected lifecycle scripts', async () => {
  const { bundle: { linux: { deb } } } = await readJSON('ui-desktop/src-tauri/tauri.linux.conf.json');
  assert.deepEqual([deb.preInstallScript, deb.postInstallScript, deb.preRemoveScript, deb.postRemoveScript],
    ['preinst', 'postinst', 'prerm', 'postrm'].map(name => '../../packaging/debian/' + name));
});

test('the normal desktop build entry prepares the service package', async () => {
  const desktop = await readJSON('ui-desktop/package.json');
  assert.equal(desktop.scripts.tauri, 'node ../scripts/tauri-build.mjs');
});

test('each generated package config refers to its own immutable payload', async () => {
  const first = await linuxPackageConfig('resources/service-payload/build-a/package');
  const second = await linuxPackageConfig('resources/service-payload/build-b/package');
  assert.equal(first.bundle.linux.deb.files['/usr/lib/tenebra/tenebra-helper'],
    'resources/service-payload/build-a/package/tenebra-helper');
  assert.equal(second.bundle.linux.deb.files['/usr/lib/tenebra/package-payload'],
    'resources/service-payload/build-b/package/payload');
  assert.equal(first.bundle.linux.deb.preInstallScript, '../../packaging/debian/preinst');
  for (const invalid of ['/tmp/foreign', '../foreign', 'resources/service-payload/../foreign']) {
    await assert.rejects(linuxPackageConfig(invalid));
  }
});

test('generated Tauri config precedes the Cargo argument separator', () => {
  const config = { bundle: { linux: { deb: { section: 'net' } } } };
  assert.deepEqual(withBuildConfig(['build', '--bundles', 'deb', '--', '--locked'], config),
    ['build', '--bundles', 'deb', '--config', JSON.stringify(config), '--', '--locked']);
  assert.deepEqual(withBuildConfig(['info'], config), ['info', '--config', JSON.stringify(config)]);
});

test('an empty package cannot satisfy complete service contents', async () => {
  await assert.rejects(verifyCompletePackage(Buffer.alloc(1024), Buffer.alloc(1024)), /package is missing control/);
});

test('the produced Tauri deb contains the GUI and complete protected service', {
  skip: !process.env.TENEBRA_DEB && 'set TENEBRA_DEB to the actual configured Tauri package',
}, async () => {
  const options = { encoding: 'buffer', maxBuffer: 512 << 20, timeout: 120_000 };
  const data = await run('dpkg-deb', ['--fsys-tarfile', process.env.TENEBRA_DEB], options);
  const control = await run('dpkg-deb', ['--ctrl-tarfile', process.env.TENEBRA_DEB], options);
  await verifyCompletePackage(data.stdout, control.stdout);
});
