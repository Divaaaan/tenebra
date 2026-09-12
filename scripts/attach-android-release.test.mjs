import { test } from 'node:test';
import assert from 'node:assert/strict';
import { attachAndroidRelease, githubAndroidAssetApi } from './attach-android-release.mjs';

const tag = 'v0.6.0';
const assetPath = '/runner/tenebra-v0.6.0.apk';

test('APK attach waits for the existing desktop release and never mutates visibility', async () => {
  for (const draft of [true, false]) {
    let now = 0, reads = 0;
    const uploads = [];
    const release = { id: 123, tag_name: tag, draft };
    const result = await attachAndroidRelease({ tag, assetPath, now: () => now, sleep: async ms => { now += ms; },
      api: { findRelease: async () => ++reads < 3 ? null : release, upload: async (...args) => uploads.push(args) } });
    assert.equal(result.releaseId, 123);
    assert.equal(reads, 3);
    assert.deepEqual(uploads, [[tag, assetPath, 120000]]);
    assert.equal(release.draft, draft);
  }
});

test('missing desktop release has a deadline and cannot create one', async () => {
  let now = 0, uploaded = false;
  await assert.rejects(attachAndroidRelease({ tag, assetPath, timeoutMs: 30, pollMs: 10,
    now: () => now, sleep: async ms => { now += ms; },
    api: { findRelease: async () => null, upload: async () => { uploaded = true; } },
  }), /desktop release.*deadline/);
  assert.equal(now, 30);
  assert.equal(uploaded, false);
});

test('wrong tag, APK filename or release identity fails before upload', async () => {
  for (const invalid of [
    { tag: 'v0.6.0;evil' }, { tag: '0.6.0' },
    { assetPath: '/runner/tenebra-v0.5.11.apk' },
    { assetPath: '/runner/tenebra-v0.6.0.apk.sig' },
    { release: { id: 123, tag_name: 'v0.5.11', draft: true } },
    { release: { id: '123', tag_name: tag, draft: true } },
  ]) {
    let uploaded = false;
    await assert.rejects(attachAndroidRelease({ tag, assetPath, ...invalid,
      api: { findRelease: async () => invalid.release ?? { id: 123, tag_name: tag }, upload: async () => { uploaded = true; } },
    }));
    assert.equal(uploaded, false);
  }
});

test('API failures and upload failures are not converted into success', async () => {
  for (const failing of ['findRelease', 'upload']) {
    const api = { findRelease: async () => ({ id: 123, tag_name: tag }), upload: async () => {} };
    api[failing] = async () => { throw new Error('injected failure'); };
    await assert.rejects(attachAndroidRelease({ tag, assetPath, api }), /injected failure/);
  }
});

test('GitHub adapter can only list releases and upload the exact existing-tag asset', async () => {
  const commands = [];
  const api = githubAndroidAssetApi('owner/repo', async (args, timeout) => {
    commands.push({ args, timeout });
    return args[0] === 'api' ? JSON.stringify([{ id: 123, tag_name: tag, draft: false }]) : '';
  });
  assert.equal((await api.findRelease(tag, 5000)).id, 123);
  await api.upload(tag, assetPath, 120000);
  assert.deepEqual(commands, [
    { args: ['api', 'repos/owner/repo/releases?per_page=100', '--method', 'GET'], timeout: 5000 },
    { args: ['release', 'upload', tag, assetPath, '--repo', 'owner/repo'], timeout: 120000 },
  ]);
  assert.ok(commands.every(({ args }) => !args.some(arg => ['create', 'edit', 'delete', 'PATCH', '--draft', '--draft=false', '--clobber'].includes(arg))));
});

test('ambiguous same-tag releases fail instead of choosing an arbitrary draft', async () => {
  const api = githubAndroidAssetApi('owner/repo', async () => JSON.stringify([{ id: 1, tag_name: tag }, { id: 2, tag_name: tag }]));
  await assert.rejects(api.findRelease(tag, 5000), /multiple releases/);
});
