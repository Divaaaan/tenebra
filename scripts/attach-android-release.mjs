// Android owns only its APK asset. Release creation, visibility and updater
// channels belong to the desktop release lifecycle, even when Android finishes
// first or uploads after publication.
import { execFile } from 'node:child_process';
import { lstatSync } from 'node:fs';
import { basename } from 'node:path';
import { pathToFileURL } from 'node:url';
import { promisify } from 'node:util';
import { setTimeout as sleepFor } from 'node:timers/promises';
import { compareVersions } from './release-lifecycle.mjs';

function validateAsset(tag, assetPath) {
  if (typeof tag !== 'string' || !tag.startsWith('v')) throw new Error('expected a v-prefixed release tag');
  compareVersions(tag.slice(1), tag.slice(1));
  if (typeof assetPath !== 'string' || basename(assetPath) !== `tenebra-${tag}.apk`) {
    throw new Error('signed APK filename does not match release tag');
  }
}

export async function attachAndroidRelease({ tag, assetPath, api,
  timeoutMs = 30 * 60 * 1000, pollMs = 15000, now = Date.now, sleep = sleepFor }) {
  validateAsset(tag, assetPath);
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0 || timeoutMs > 30 * 60 * 1000 ||
      !Number.isFinite(pollMs) || pollMs <= 0) throw new Error('invalid desktop release wait bounds');
  const deadline = now() + timeoutMs;
  while (now() < deadline) {
    const release = await api.findRelease(tag, Math.min(30000, deadline - now()));
    if (release) {
      if (!Number.isSafeInteger(release.id) || release.id <= 0 || release.tag_name !== tag) {
        throw new Error('desktop release identity does not match requested tag');
      }
      if (now() >= deadline) break;
      await api.upload(tag, assetPath, 120000);
      return { releaseId: release.id };
    }
    const remaining = deadline - now();
    if (remaining > 0) await sleep(Math.min(pollMs, remaining));
  }
  throw new Error('existing desktop release was not found before the deadline; APK was not attached');
}

const execFileAsync = promisify(execFile);
async function invokeGh(args, timeout) {
  try {
    const { stdout } = await execFileAsync('gh', args, { timeout, maxBuffer: 4 * 1024 * 1024, windowsHide: true });
    return stdout;
  } catch {
    throw new Error('GitHub APK asset operation failed or timed out');
  }
}

export function githubAndroidAssetApi(repo, invoke = invokeGh) {
  if (!/^[\w.-]+\/[\w.-]+$/.test(repo)) throw new Error('invalid GitHub repository');
  return {
    async findRelease(tag, timeout) {
      // List includes authenticated drafts; the public by-tag endpoint may not.
      // Only a new tag release is awaited, so retain a bounded recent page.
      const releases = JSON.parse(await invoke(['api', `repos/${repo}/releases?per_page=100`, '--method', 'GET'], timeout));
      if (!Array.isArray(releases)) throw new Error('invalid GitHub releases response');
      const matching = releases.filter(release => release.tag_name === tag);
      if (matching.length > 1) throw new Error('multiple releases use the requested tag');
      return matching[0] ?? null;
    },
    async upload(tag, assetPath, timeout) {
      validateAsset(tag, assetPath);
      // No create/edit call, visibility flags or clobber: an existing asset
      // collision fails instead of deleting an already delivered APK.
      await invoke(['release', 'upload', tag, assetPath, '--repo', repo], timeout);
    },
  };
}

async function main() {
  const args = process.argv.slice(2);
  if (args.length !== 2) throw new Error('usage: attach-android-release.mjs <tag> <signed-apk-path>');
  const [tag, assetPath] = args;
  validateAsset(tag, assetPath);
  const asset = lstatSync(assetPath);
  if (!asset.isFile() || asset.size === 0) throw new Error('signed APK must be a non-empty regular file');
  const api = githubAndroidAssetApi(process.env.GITHUB_REPOSITORY);
  console.log(`Waiting up to 30 minutes for the existing desktop release ${tag}.`);
  const result = await attachAndroidRelease({ tag, assetPath, api });
  console.log(`Attached ${basename(assetPath)} to release ${result.releaseId}; release visibility was not changed.`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
