// Release gate and atomic beta channel switch. All mutation goes through the
// injectable API boundary so ordering, partial delivery and races are tested.
import { expectedAssets, missingAssets } from '../.github/scripts/publish-release.mjs';

const platforms = ['windows-x86_64', 'windows-x86_64-nsis',
  'darwin-x86_64', 'darwin-aarch64', 'darwin-x86_64-app', 'darwin-aarch64-app',
  'linux-x86_64', 'linux-x86_64-appimage', 'linux-x86_64-deb'];

function semver(value) {
  const m = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.exec(value);
  if (!m) throw new Error(`invalid release version: ${value}`);
  const pre = m[4]?.split('.');
  if (pre?.some(p => /^0\d+$/.test(p))) throw new Error(`invalid prerelease version: ${value}`);
  return { core: m.slice(1, 4).map(BigInt), pre };
}
export function compareVersions(a, b) {
  const x = semver(a), y = semver(b);
  for (let i = 0; i < 3; i++) if (x.core[i] !== y.core[i]) return x.core[i] > y.core[i] ? 1 : -1;
  if (!x.pre || !y.pre) return x.pre ? -1 : y.pre ? 1 : 0;
  for (let i = 0; i < Math.max(x.pre.length, y.pre.length); i++) {
    const l = x.pre[i], r = y.pre[i];
    if (l === r) continue;
    if (l === undefined) return -1;
    if (r === undefined) return 1;
    const ln = /^\d+$/.test(l), rn = /^\d+$/.test(r);
    if (ln && rn) return BigInt(l) > BigInt(r) ? 1 : -1;
    if (ln !== rn) return ln ? -1 : 1;
    return l > r ? 1 : -1;
  }
  return 0;
}

export function validateManifest(manifest, { tag, repo, assets }) {
  if (manifest.version !== tag.replace(/^v/, '')) throw new Error('updater manifest version does not match release tag');
  const attached = new Set(assets.map(a => a.name));
  const urls = new Set();
  for (const key of platforms) {
    const entry = manifest.platforms?.[key];
    if (!entry || !entry.signature?.trim()) throw new Error(`missing signed updater platform: ${key}`);
    const url = new URL(entry.url);
    const prefix = `/${repo}/releases/download/${tag}/`;
    if (url.origin !== 'https://github.com' || !decodeURIComponent(url.pathname).startsWith(prefix) || url.search || url.hash)
      throw new Error(`untrusted updater URL for ${key}`);
    const name = decodeURIComponent(url.pathname).slice(prefix.length);
    if (name.includes('/') || !attached.has(name) || !attached.has(`${name}.sig`))
      throw new Error(`updater asset or signature missing for ${key}`);
    urls.add(entry.url);
  }
  return [...urls];
}

export async function publishCompleteRelease({ tag, repo, api, prepareOnly = false }) {
  if (!tag.startsWith('v')) throw new Error('release tag must start with v');
  const version = tag.slice(1); semver(version);
  const prerelease = Boolean(semver(version).pre);
  const release = await api.getRelease(tag);
  if (prepareOnly && !release.isDraft) throw new Error('release is already public; cannot prepare a draft');
  if (release.isPrerelease !== prerelease) throw new Error('release channel disagrees with tag');
  // Legacy beta is staged only on a stable draft, after every platform job.
  const expected = expectedAssets({ version, prerelease }).filter(a => a.want !== 'beta.json');
  const missing = missingAssets(expected, release.assets.filter(a => a.state === 'uploaded' && a.size > 0).map(a => a.name));
  if (missing.length) throw new Error(`incomplete release: ${missing.map(a => a.want).join(', ')}`);
  const manifest = await api.readManifest(tag);
  const urls = validateManifest(manifest, { tag, repo, assets: release.assets });
  for (const url of urls) {
    const name = decodeURIComponent(new URL(url).pathname.split('/').pop());
    const signature = (await api.readAssetText(tag, `${name}.sig`)).trim();
    for (const entry of Object.values(manifest.platforms)) {
      if (entry.url === url && entry.signature.trim() !== signature) throw new Error(`manifest signature differs from ${name}.sig`);
    }
  }
  if (!prerelease && release.isDraft) await api.seedLegacyBeta(tag, manifest);
  // A held release has the same complete signed assets and stable legacy
  // manifest, but remains private until the exact installer is accepted.
  if (prepareOnly) return { prepared: true, switched: false };
  if (release.isDraft) await api.publish(tag);
  const visible = await api.getRelease(tag);
  if (visible.isDraft) throw new Error('release is still a draft; beta pointer preserved');
  // Unauthenticated probes catch a draft/private/unavailable download before
  // the public pointer is changed. No platform build may invoke this switch.
  for (const asset of release.assets) {
    await api.assertPublicAsset(`https://github.com/${repo}/releases/download/${encodeURIComponent(tag)}/${encodeURIComponent(asset.name)}`);
  }
  for (let attempt = 0; attempt < 3; attempt++) {
    const current = await api.readChannel();
    if (current && compareVersions(current.manifest.version, version) >= 0) return { switched: false };
    try {
      await api.compareAndSwapChannel(manifest, current?.sha);
      return { switched: true };
    } catch (error) {
      if (![409, 422].includes(error.status) || attempt === 2) throw error;
    }
  }
}
