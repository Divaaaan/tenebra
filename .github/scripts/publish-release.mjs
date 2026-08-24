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
//   node .github/scripts/publish-release.mjs <tag>
//
// Authenticates through gh via GITHUB_TOKEN and reads the repository from
// GITHUB_REPOSITORY. The Android APK is deliberately not in the expected set:
// it is built by a separate workflow (.github/workflows/android.yml) on its own
// schedule, and that workflow answers for itself when it cannot produce one.

import { execFileSync } from "node:child_process";
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

/** The release for `tag`, looked up in a way that also finds it while a draft. */
function fetchRelease(repo, tag) {
  // gh falls back to a list-and-match when the by-tag endpoint 404s, which is
  // what it does for a draft: GitHub only exposes drafts by id.
  const out = execFileSync(
    "gh",
    ["release", "view", tag, "--repo", repo, "--json", "isDraft,isPrerelease,assets"],
    { encoding: "utf8" },
  );
  return JSON.parse(out);
}

function main() {
  const [tag] = process.argv.slice(2);
  if (!tag) {
    console.error("usage: node .github/scripts/publish-release.mjs <tag>");
    process.exit(1);
  }
  const repo = process.env.GITHUB_REPOSITORY;
  if (!repo) {
    console.error("publish-release: GITHUB_REPOSITORY is not set");
    process.exit(1);
  }

  const version = tag.replace(/^v/, "");
  // Same rule the build jobs resolve the channel with: a SemVer prerelease
  // suffix marks the release prerelease.
  const prerelease = tag.includes("-");

  const release = fetchRelease(repo, tag);
  const attached = release.assets.map((a) => a.name).sort();
  const missing = missingAssets(expectedAssets({ version, prerelease }), attached);

  if (missing.length > 0) {
    console.error(
      `publish-release: ${tag} is missing ${missing.length} expected asset(s); leaving it a draft`,
    );
    for (const asset of missing) {
      console.error(`  missing: ${asset.label} (${asset.want})`);
    }
    console.error(`  attached: ${attached.join(", ") || "(nothing)"}`);
    process.exit(1);
  }

  if (!release.isDraft) {
    // A re-run of a release that already went out: the set is complete, so
    // there is nothing to publish and nothing to complain about.
    console.log(
      `publish-release: ${tag} is already published, with all ${attached.length} expected assets`,
    );
    return;
  }

  execFileSync("gh", ["release", "edit", tag, "--repo", repo, "--draft=false"], {
    stdio: "inherit",
  });
  console.log(
    `publish-release: ${tag} published with ${attached.length} assets: ${attached.join(", ")}`,
  );
}

// Run only when invoked as a script, so the pure helpers can be unit-tested.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main();
}

// Referenced by the test runner without triggering main().
export const _scriptPath = fileURLToPath(import.meta.url);
