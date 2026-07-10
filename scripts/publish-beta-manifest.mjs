// Publishes the beta-channel updater manifest (beta.json) to the GitHub release
// whose assets back the /releases/latest/download/ URL the client polls.
//
// Tauri's updater serves one manifest per endpoint, and GitHub's
// /releases/latest/download/ always resolves to the newest NON-prerelease
// release. So the stable channel reads latest.json (published by tauri-action on
// the stable release, unchanged) and the beta channel reads beta.json, which we
// place on that same latest-stable release. beta.json is a copy of the manifest
// tauri-action generated for THIS build:
//
//   - stable tag  -> this release becomes the new /latest/; copy its latest.json
//                    to beta.json on the same release, so a beta user always sees
//                    the newest stable (the beta+stable cascade, at publish time).
//   - prerelease  -> GitHub keeps this release out of /latest/; copy its manifest
//                    to beta.json on the current latest-stable release, so beta
//                    users pick up the prerelease while stable users — reading
//                    latest.json on that same release — do not.
//
// The manifest is byte-for-byte tauri-action's signed output: same version, same
// minisign signature, same installer URL (a per-tag asset URL that stays
// reachable even for a prerelease). Verification is therefore identical on both
// channels, and the stable flow is never touched.
//
//   node scripts/publish-beta-manifest.mjs <tag> <prerelease>
//
// where <tag> is the pushed git tag (e.g. v0.4.0-beta.1) and <prerelease> is
// "true" or "false". Authenticates through gh via GITHUB_TOKEN.

import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

/**
 * The release whose assets beta.json must be uploaded to, so that
 * /releases/latest/download/beta.json resolves to it.
 *
 * A stable build owns the new "latest" release, so beta.json goes on the tag
 * itself. A prerelease is excluded from "latest" by GitHub, so beta.json goes on
 * the current latest-stable release. When a prerelease is cut before any stable
 * release exists there is nowhere `/releases/latest/` can point, so there is
 * nothing to publish and the caller skips.
 */
export function resolveBetaTarget({ tag, prerelease, latestStable }) {
  if (!prerelease) {
    return tag;
  }
  return latestStable ?? null;
}

/** Look up the latest non-prerelease release tag, or null when none exists. */
function latestStableTag(repo) {
  try {
    const out = execFileSync(
      "gh",
      ["api", `repos/${repo}/releases/latest`, "--jq", ".tag_name"],
      { encoding: "utf8" },
    );
    const tag = out.trim();
    return tag.length > 0 ? tag : null;
  } catch {
    // 404 when the repo has no full (non-prerelease) release yet.
    return null;
  }
}

function main() {
  const [tag, prereleaseArg] = process.argv.slice(2);
  if (!tag || prereleaseArg === undefined) {
    console.error(
      "usage: node scripts/publish-beta-manifest.mjs <tag> <true|false>",
    );
    process.exit(1);
  }
  const prerelease = prereleaseArg === "true";
  const repo = process.env.GITHUB_REPOSITORY;
  if (!repo) {
    console.error("publish-beta-manifest: GITHUB_REPOSITORY is not set");
    process.exit(1);
  }

  const latestStable = prerelease ? latestStableTag(repo) : null;
  const target = resolveBetaTarget({ tag, prerelease, latestStable });
  if (!target) {
    // A prerelease with no stable release to attach to: /releases/latest/ has
    // nowhere to resolve, so there is no beta channel to serve yet. Nothing to
    // do — the next stable release seeds it.
    console.log(
      "publish-beta-manifest: no latest-stable release yet; skipping beta.json",
    );
    return;
  }

  // Copy the manifest tauri-action just published for THIS build, renamed to
  // beta.json, then attach it to the target release (replacing any prior one).
  const dir = mkdtempSync(join(tmpdir(), "tenebra-beta-"));
  execFileSync(
    "gh",
    [
      "release",
      "download",
      tag,
      "--repo",
      repo,
      "--pattern",
      "latest.json",
      "--dir",
      dir,
      "--clobber",
    ],
    { stdio: "inherit" },
  );
  const manifest = readFileSync(join(dir, "latest.json"));
  const betaPath = join(dir, "beta.json");
  writeFileSync(betaPath, manifest);
  execFileSync(
    "gh",
    ["release", "upload", target, betaPath, "--repo", repo, "--clobber"],
    { stdio: "inherit" },
  );

  const version = JSON.parse(manifest.toString("utf8")).version;
  console.log(
    `publish-beta-manifest: beta.json (${version}) published to ${target}`,
  );
}

// Run only when invoked as a script, so the pure helper can be unit-tested.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main();
}

// Referenced by the test runner without triggering main().
export const _scriptPath = fileURLToPath(import.meta.url);
