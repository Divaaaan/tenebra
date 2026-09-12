// Publishes the tag's release — but only once every artifact the release
// promises is actually attached to it.
//
// The three tauri jobs and the Arch job all upload into ONE release for the tag,
// and they finish minutes apart. Publishing from the first of them (the old
// `releaseDraft: false`) put a download page in front of users while two thirds
// of the files were still building, and put it in front of the winget workflow's
// `released` trigger at the same moment. Worse, a build job that failed outright
// left that published release permanently short a file: v0.4.6 and v0.5.0 both
// shipped without the Arch package because the job attaching it had never once
// succeeded, and nothing in the pipeline was looking.
//
// So every job now builds into a DRAFT, and this is the only step that makes it
// visible. It compares what is attached against what this tag is supposed to
// carry and publishes only on a complete set; anything missing leaves the
// release a draft and fails the run. A draft is one click away from being
// published by hand, which is the recoverable direction to fail in.
//
//   node .github/scripts/publish-release.mjs <tag> [--prepare-only]
// prepare-only runs the same completeness and signature gates and stages the
// stable legacy manifest, leaving publication and both live channels untouched.
//
// Authenticates through gh via GITHUB_TOKEN and reads the repository from
// GITHUB_REPOSITORY. The Android APK is deliberately not in the expected set:
// it is built by a separate workflow (.github/workflows/android.yml) on its own
// schedule, and that workflow answers for itself when it cannot produce one.

import { fileURLToPath, pathToFileURL } from "node:url";

function escapeRegExp(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Every asset a release for `version` must carry before it may be published.
 *
 * The names are the bundlers' own output names, which carry the version — that
 * is what makes this a real check rather than a head count: a re-run that
 * attached the previous version's installer reads as missing, not as present.
 * The Arch package is matched by pattern because its file name also carries
 * pkgrel, which is bumped for a packaging-only rebuild at the same version.
 *
 * `beta.json` is expected only on a stable release. On a prerelease the beta
 * manifest is published onto the current latest-STABLE release instead — that
 * is the release /releases/latest/download/ resolves to — so requiring it here
 * would hold every prerelease tag in draft forever. See
 * scripts/publish-beta-manifest.mjs.
 */
export function expectedAssets({ version, prerelease }) {
  const v = version;
  const assets = [
    { label: "Windows installer", want: `Tenebra_${v}_x64-setup.exe` },
    { label: "Windows updater signature", want: `Tenebra_${v}_x64-setup.exe.sig` },
    { label: "macOS disk image", want: `Tenebra_${v}_universal.dmg` },
    { label: "macOS updater bundle", want: "Tenebra_universal.app.tar.gz" },
    { label: "macOS updater signature", want: "Tenebra_universal.app.tar.gz.sig" },
    { label: "Debian package", want: `Tenebra_${v}_amd64.deb` },
    { label: "Debian package signature", want: `Tenebra_${v}_amd64.deb.sig` },
    { label: "AppImage", want: `Tenebra_${v}_amd64.AppImage` },
    { label: "AppImage updater signature", want: `Tenebra_${v}_amd64.AppImage.sig` },
    {
      label: "Arch package",
      want: `tenebra-${v}-<pkgrel>-x86_64.pkg.tar.zst`,
      pattern: new RegExp(`^tenebra-${escapeRegExp(v)}-\\d+-x86_64\\.pkg\\.tar\\.zst$`),
    },
    { label: "stable updater manifest", want: "latest.json" },
  ];
  if (!prerelease) {
    assets.push({ label: "beta updater manifest", want: "beta.json" });
  }
  return assets;
}

/** The expected assets that `attached` does not satisfy, in declaration order. */
export function missingAssets(expected, attached) {
  const names = new Set(attached);
  return expected.filter((asset) =>
    asset.pattern
      ? ![...names].some((name) => asset.pattern.test(name))
      : !names.has(asset.want),
  );
}

export function parsePublishArgs(args) {
  const [tag, mode] = args;
  if (!tag?.startsWith('v') || args.length > 2 || (mode !== undefined && mode !== '--prepare-only')) {
    throw new Error('usage: publish-release.mjs <tag> [--prepare-only]');
  }
  return { tag, prepareOnly: mode === '--prepare-only' };
}

async function main() {
  const { tag, prepareOnly } = parsePublishArgs(process.argv.slice(2));
  const repo = process.env.GITHUB_REPOSITORY;
  if (!tag || !repo) throw new Error('usage: GITHUB_REPOSITORY=owner/repo node .github/scripts/publish-release.mjs <tag>');
  const { publishCompleteRelease } = await import('../../scripts/release-lifecycle.mjs');
  const { githubReleaseApi } = await import('../../scripts/release-api.mjs');
  const result = await publishCompleteRelease({ tag, repo, api: githubReleaseApi(repo, tag), prepareOnly });
  if (result.prepared) {
    console.log(`publish-release: ${tag} verified and held as a draft; native acceptance required before publication`);
    return;
  }
  console.log(`publish-release: ${tag} complete and public; beta ${result.switched ? 'updated atomically' : 'already at this or a newer version'}`);
}

// Run only when invoked as a script, so the pure helpers can be unit-tested.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}

// Referenced by the test runner without triggering main().
export const _scriptPath = fileURLToPath(import.meta.url);
