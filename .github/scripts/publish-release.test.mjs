// Unit tests for the release completeness check — the gate that decides whether
// a tag's draft release is whole enough to show to users.
//
// Run with `node --test .github/scripts/`.

import { test } from "node:test";
import assert from "node:assert/strict";

import { expectedAssets, missingAssets } from "./publish-release.mjs";

/** The eleven files v0.5.0 actually shipped with — the Arch package missing. */
const v050Assets = [
  "beta.json",
  "latest.json",
  "Tenebra_0.5.0_amd64.AppImage",
  "Tenebra_0.5.0_amd64.AppImage.sig",
  "Tenebra_0.5.0_amd64.deb",
  "Tenebra_0.5.0_amd64.deb.sig",
  "Tenebra_0.5.0_universal.dmg",
  "Tenebra_0.5.0_x64-setup.exe",
  "Tenebra_0.5.0_x64-setup.exe.sig",
  "Tenebra_universal.app.tar.gz",
  "Tenebra_universal.app.tar.gz.sig",
];

function labels(missing) {
  return missing.map((m) => m.label);
}

test("the release v0.5.0 actually published is reported as incomplete", () => {
  // The regression this gate exists for: the Arch job's upload has never once
  // succeeded, so every release since v0.4.6 went out without the one Linux
  // artifact that installs the service for you — and the pipeline still went
  // green enough to leave the release published.
  const missing = missingAssets(
    expectedAssets({ version: "0.5.0", prerelease: false }),
    v050Assets,
  );
  assert.deepEqual(labels(missing), ["Arch package"]);
});

test("a complete release passes", () => {
  const missing = missingAssets(
    expectedAssets({ version: "0.5.0", prerelease: false }),
    [...v050Assets, "tenebra-0.5.0-1-x86_64.pkg.tar.zst"],
  );
  assert.deepEqual(missing, []);
});

test("the Arch package is matched whatever its pkgrel", () => {
  // pkgrel is bumped for a packaging-only rebuild at the same upstream version,
  // so the release number cannot be pinned the way the version can.
  const missing = missingAssets(
    expectedAssets({ version: "0.5.0", prerelease: false }),
    [...v050Assets, "tenebra-0.5.0-4-x86_64.pkg.tar.zst"],
  );
  assert.deepEqual(missing, []);
});

test("an asset left over from the previous version does not count", () => {
  // A stale asset carrying the old version is exactly what a partially re-run
  // release looks like, and it is the case a plain "is anything attached?"
  // check would wave through.
  const stale = v050Assets.map((n) => n.replace("0.5.0", "0.4.6"));
  const missing = missingAssets(
    expectedAssets({ version: "0.5.0", prerelease: false }),
    [...stale, "tenebra-0.4.6-1-x86_64.pkg.tar.zst"],
  );
  assert.deepEqual(labels(missing), [
    "Windows installer",
    "Windows updater signature",
    "macOS disk image",
    "Debian package",
    "Debian package signature",
    "AppImage",
    "AppImage updater signature",
    "Arch package",
  ]);
});

test("a release with nothing attached lists everything as missing", () => {
  const expected = expectedAssets({ version: "0.5.0", prerelease: false });
  assert.equal(missingAssets(expected, []).length, expected.length);
});

test("a prerelease is not expected to carry beta.json", () => {
  // beta.json is published onto the current latest-STABLE release (that is what
  // /releases/latest/download/ resolves to), never onto the prerelease itself —
  // see scripts/publish-beta-manifest.mjs. Expecting it here would deadlock
  // every prerelease tag in draft.
  const expected = expectedAssets({ version: "0.6.0-rc.1", prerelease: true });
  assert.ok(!expected.some((a) => a.label === "beta updater manifest"));
  const attached = [
    ...v050Assets.filter((n) => n !== "beta.json").map((n) => n.replace("0.5.0", "0.6.0-rc.1")),
    "tenebra-0.6.0-rc.1-1-x86_64.pkg.tar.zst",
  ];
  assert.deepEqual(missingAssets(expected, attached), []);
});

test("a stable release must carry both updater manifests", () => {
  const expected = expectedAssets({ version: "0.5.0", prerelease: false });
  const missing = missingAssets(
    expected,
    [...v050Assets, "tenebra-0.5.0-1-x86_64.pkg.tar.zst"].filter(
      (n) => n !== "beta.json" && n !== "latest.json",
    ),
  );
  assert.deepEqual(labels(missing), [
    "stable updater manifest",
    "beta updater manifest",
  ]);
});

test("a dot in the version does not match any character", () => {
  // The version reaches the Arch matcher as part of a regular expression, since
  // pkgrel has to stay open. Unescaped, its dots would accept a package built
  // for a different version entirely.
  const arch = expectedAssets({ version: "0.5.0", prerelease: false }).find(
    (a) => a.label === "Arch package",
  );
  assert.ok(arch.pattern.test("tenebra-0.5.0-1-x86_64.pkg.tar.zst"));
  assert.ok(!arch.pattern.test("tenebra-0x5x0-1-x86_64.pkg.tar.zst"));
  assert.ok(!arch.pattern.test("tenebra-10.5.0-1-x86_64.pkg.tar.zst"));
});
