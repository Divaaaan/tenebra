import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as lifecycle from './release-lifecycle.mjs';

const version = '0.6.0-beta.1';
const names = [
  `Tenebra_${version}_x64-setup.exe`, `Tenebra_${version}_x64-setup.exe.sig`,
  `Tenebra_${version}_universal.dmg`, 'Tenebra_universal.app.tar.gz', 'Tenebra_universal.app.tar.gz.sig',
  `Tenebra_${version}_amd64.deb`, `Tenebra_${version}_amd64.deb.sig`,
  `Tenebra_${version}_amd64.AppImage`, `Tenebra_${version}_amd64.AppImage.sig`,
  `tenebra-${version}-1-x86_64.pkg.tar.zst`, 'latest.json',
];
function fixture(releaseVersion = version) {
  const actualNames = names.map(n => n.replace(version, releaseVersion));
  const assets = actualNames.map((name) => ({ name, state: 'uploaded', size: 12 }));
  const platforms = {};
  for (const [keys, asset] of [
    [['windows-x86_64', 'windows-x86_64-nsis'], actualNames[0]],
    [['darwin-x86_64', 'darwin-aarch64', 'darwin-x86_64-app', 'darwin-aarch64-app'], actualNames[3]],
    [['linux-x86_64', 'linux-x86_64-appimage'], actualNames[7]],
    [['linux-x86_64-deb'], actualNames[5]],
  ]) for (const key of keys) platforms[key] = { url: `https://github.com/owner/repo/releases/download/v${releaseVersion}/${asset}`, signature: 'signed' };
  let channel = { sha: 'old-sha', manifest: { version: '0.5.11' } };
  let release = { isDraft: true, isPrerelease: releaseVersion.includes('-'), assets };
  const events = [];
  const api = {
    getRelease: async () => structuredClone(release),
    readManifest: async () => ({ version: releaseVersion, platforms }),
    readAssetText: async () => 'signed',
    assertPublicAsset: async (url) => { assert.equal(release.isDraft, false); events.push('ready'); },
    seedLegacyBeta: async () => { assert.equal(release.isDraft, true); events.push('legacy'); },
    publish: async () => { events.push('publish'); release.isDraft = false; },
    readChannel: async () => structuredClone(channel),
    compareAndSwapChannel: async (manifest, sha) => {
      assert.equal(release.isDraft, false);
      assert.equal(sha, channel.sha);
      events.push('switch'); channel = { sha: 'new-sha', manifest };
    },
  };
  return { api, events, channel: () => channel, release, setChannel: (value) => { channel = value; } };
}
const run = (api) => lifecycle.publishCompleteRelease({ tag: `v${version}`, repo: 'owner/repo', api });

test('all public assets are ready before the only atomic channel switch', async () => {
  const f = fixture(); await run(f.api);
  assert.equal(f.events[0], 'publish');
  assert.equal(f.events.at(-1), 'switch');
  assert.equal(f.events.filter((e) => e === 'switch').length, 1);
  assert.equal(f.channel().manifest.version, version);
});
test('downstream missing or failed upload preserves draft and previous pointer', async () => {
  for (const change of [r => r.assets.pop(), r => r.assets[0].state = 'starter']) {
    const f = fixture(); change(f.release);
    await assert.rejects(run(f.api));
    assert.equal(f.release.isDraft, true);
    assert.equal(f.channel().sha, 'old-sha');
    assert.deepEqual(f.events, []);
  }
});
test('publication or public-download failure leaves prior channel unchanged', async () => {
  for (const key of ['publish', 'assertPublicAsset']) {
    const f = fixture(); f.api[key] = async () => { throw new Error('injected failure'); };
    await assert.rejects(run(f.api), /injected failure/);
    assert.equal(f.channel().sha, 'old-sha');
  }
});
test('wrong version, partial platform coverage, and foreign asset URLs fail closed', async () => {
  for (const mutate of [m => m.version = '0.5.0', m => delete m.platforms['darwin-aarch64'], m => m.platforms['windows-x86_64'].url = 'https://example.com/setup.exe']) {
    const f = fixture(); const manifest = await f.api.readManifest(); mutate(manifest);
    f.api.readManifest = async () => manifest;
    await assert.rejects(run(f.api));
    assert.equal(f.channel().sha, 'old-sha'); assert.deepEqual(f.events, []);
  }
});
test('older concurrent publisher cannot roll the channel back', async () => {
  const f = fixture(); f.setChannel({ sha: 'newer', manifest: { version: '0.7.0' } });
  await run(f.api); assert.equal(f.channel().manifest.version, '0.7.0');
  assert.ok(!f.events.includes('switch'));
});
test('CAS collision re-reads pointer and yields to newer publication', async () => {
  const f = fixture(); let calls = 0;
  f.api.compareAndSwapChannel = async () => { calls++; f.setChannel({ sha: 'race', manifest: { version: '0.7.0' } }); throw Object.assign(new Error('conflict'), { status: 409 }); };
  await run(f.api); assert.equal(calls, 1); assert.equal(f.channel().sha, 'race');
});
test('CAS retry is bounded and never deletes the existing pointer', async () => {
  const f = fixture(); let calls = 0;
  f.api.compareAndSwapChannel = async () => { calls++; throw Object.assign(new Error('conflict'), { status: 409 }); };
  await assert.rejects(run(f.api), /conflict/); assert.equal(calls, 3); assert.equal(f.channel().sha, 'old-sha');
});
test('numeric prerelease ordering and stable promotion obey SemVer', () => {
  assert.ok(lifecycle.compareVersions('0.6.0-beta.10', '0.6.0-beta.2') > 0);
  assert.ok(lifecycle.compareVersions('0.6.0', '0.6.0-rc.9') > 0);
  assert.equal(lifecycle.compareVersions('0.6.0+one', '0.6.0+two'), 0);
});

test('stable legacy manifest is prepared inside the draft before publication', async () => {
  const f = fixture('0.6.0');
  await lifecycle.publishCompleteRelease({ tag: 'v0.6.0', repo: 'owner/repo', api: f.api });
  assert.deepEqual(f.events.slice(0, 2), ['legacy', 'publish']);
  assert.equal(f.events.at(-1), 'switch');
});
test('rerunning a published stable never clobbers its live legacy asset', async () => {
  const f = fixture('0.6.0'); f.release.isDraft = false;
  await lifecycle.publishCompleteRelease({ tag: 'v0.6.0', repo: 'owner/repo', api: f.api });
  assert.ok(!f.events.includes('legacy')); assert.ok(!f.events.includes('publish'));
});
test('mismatched updater signature fails before publication', async () => {
  const f = fixture(); f.api.readAssetText = async () => 'different-signature';
  await assert.rejects(run(f.api), /signature differs/);
  assert.deepEqual(f.events, []); assert.equal(f.channel().sha, 'old-sha');
});

test('prepare-only verifies and completes a stable draft without public or channel operations', async () => {
  const f = fixture('0.6.0');
  f.api.readChannel = async () => { throw new Error('prepare must not read the public channel'); };
  const result = await lifecycle.publishCompleteRelease({ tag: 'v0.6.0', repo: 'owner/repo', api: f.api, prepareOnly: true });
  assert.deepEqual(result, { prepared: true, switched: false });
  assert.deepEqual(f.events, ['legacy']);
  assert.equal(f.release.isDraft, true);
  assert.equal(f.channel().sha, 'old-sha');
});

test('prepare-only preserves prerelease channel and refuses an already public release', async () => {
  const f = fixture();
  await lifecycle.publishCompleteRelease({ tag: `v${version}`, repo: 'owner/repo', api: f.api, prepareOnly: true });
  assert.deepEqual(f.events, []);
  f.release.isDraft = false;
  await assert.rejects(lifecycle.publishCompleteRelease({ tag: `v${version}`, repo: 'owner/repo', api: f.api, prepareOnly: true }), /already public/);
  assert.deepEqual(f.events, []);
});

test('prepare-only does not bypass any asset, manifest or signature gate', async () => {
  for (const change of [
    f => f.release.assets.pop(),
    f => f.release.assets[0].state = 'starter',
    f => f.release.assets[0].size = 0,
    f => f.release.isPrerelease = true,
    f => { f.api.readManifest = async () => ({ version: '0.5.11', platforms: {} }); },
    f => { f.api.readAssetText = async () => 'wrong-signature'; },
  ]) {
    const f = fixture('0.6.0'); change(f);
    await assert.rejects(lifecycle.publishCompleteRelease({ tag: 'v0.6.0', repo: 'owner/repo', api: f.api, prepareOnly: true }));
    assert.deepEqual(f.events, []);
    assert.equal(f.release.isDraft, true);
    assert.equal(f.channel().sha, 'old-sha');
  }
});
