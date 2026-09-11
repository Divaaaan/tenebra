// GitHub adapter. Tests inject an in-memory adapter into release-lifecycle;
// this module is called only by the final publish job, never platform builds.
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

export function githubReleaseApi(repo, tag) {
  if (!/^[\w.-]+\/[\w.-]+$/.test(repo)) throw new Error('invalid GitHub repository');
  const base = `repos/${repo}`;
  const branch = 'update-channels';
  function gh(args, input) {
    try { return execFileSync('gh', args, { encoding: 'utf8', input, stdio: ['pipe', 'pipe', 'pipe'] }); }
    catch (error) {
      const detail = String(error.stderr ?? '');
      const status = Number(/HTTP (\d{3})/.exec(detail)?.[1]);
      throw Object.assign(new Error(`GitHub release operation failed${status ? ` (HTTP ${status})` : ''}`), { status });
    }
  }
  function request(path, method = 'GET', payload) {
    const args = ['api', path, '--method', method];
    if (payload) args.push('--input', '-');
    const output = gh(args, payload ? JSON.stringify(payload) : undefined);
    return output.trim() ? JSON.parse(output) : undefined;
  }
  async function assetText(tag, name) {
    const dir = mkdtempSync(join(tmpdir(), 'tenebra-release-'));
    try {
      gh(['release', 'download', tag, '--repo', repo, '--pattern', name, '--dir', dir]);
      return readFileSync(join(dir, name), 'utf8');
    } finally { rmSync(dir, { recursive: true, force: true }); }
  }
  async function ensureBranch() {
    try { request(`${base}/git/ref/heads/${branch}`); return; }
    catch (e) { if (e.status !== 404) throw e; }
    const sha = request(`${base}/commits/${encodeURIComponent(tag)}`).sha;
    try { request(`${base}/git/refs`, 'POST', { ref: `refs/heads/${branch}`, sha }); }
    catch (e) { if (e.status !== 422) throw e; }
    // Recheck a racing bootstrap: a 422 must not mask another API failure.
    request(`${base}/git/ref/heads/${branch}`);
  }
  return {
    async getRelease(tag) {
      const { databaseId } = JSON.parse(gh(['release', 'view', tag, '--repo', repo, '--json', 'databaseId']));
      const release = request(`${base}/releases/${databaseId}`);
      return { isDraft: release.draft, isPrerelease: release.prerelease, assets: release.assets };
    },
    async readManifest(tag) { return JSON.parse(await assetText(tag, 'latest.json')); },
    readAssetText: assetText,
    async seedLegacyBeta(tag, manifest) {
      const dir = mkdtempSync(join(tmpdir(), 'tenebra-legacy-beta-'));
      try {
        const path = join(dir, 'beta.json');
        writeFileSync(path, JSON.stringify(manifest, null, 2) + '\n');
        gh(['release', 'upload', tag, path, '--repo', repo, '--clobber']);
      } finally { rmSync(dir, { recursive: true, force: true }); }
    },
    async publish(tag) { gh(['release', 'edit', tag, '--repo', repo, '--draft=false']); },
    async assertPublicAsset(url) {
      const response = await fetch(url, { method: 'HEAD', redirect: 'follow', signal: AbortSignal.timeout(15000) });
      if (!response.ok) throw new Error(`release download is not public/ready (HTTP ${response.status})`);
    },
    async readChannel() {
      try {
        const result = request(`${base}/contents/beta.json?ref=${branch}`);
        return { sha: result.sha, manifest: JSON.parse(Buffer.from(result.content, 'base64').toString('utf8')) };
      } catch (error) { if (error.status === 404) return null; throw error; }
    },
    async compareAndSwapChannel(manifest, sha) {
      await ensureBranch();
      request(`${base}/contents/beta.json`, 'PUT', {
        branch, message: `release: publish beta channel ${manifest.version}`,
        content: Buffer.from(JSON.stringify(manifest, null, 2) + '\n').toString('base64'),
        ...(sha ? { sha } : {}),
      });
    },
  };
}
