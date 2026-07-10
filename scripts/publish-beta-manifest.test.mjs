// Unit tests for the beta-manifest target resolution — the publish-time cascade
// that lets a beta user always see the newest of the beta and stable channels.
// Run with `node --test scripts/`.

import { test } from "node:test";
import assert from "node:assert/strict";

import { resolveBetaTarget } from "./publish-beta-manifest.mjs";

test("a stable build attaches beta.json to its own release", () => {
  // The stable tag becomes the new /releases/latest/, so beta.json lives there
  // too — a beta user reading it sees the freshly shipped stable.
  assert.equal(
    resolveBetaTarget({
      tag: "v0.4.0",
      prerelease: false,
      latestStable: "v0.3.0",
    }),
    "v0.4.0",
  );
});

test("a prerelease attaches beta.json to the current latest-stable release", () => {
  // The prerelease is kept out of /latest/ by GitHub, so beta.json goes on the
  // stable release that /releases/latest/download/ actually resolves to.
  assert.equal(
    resolveBetaTarget({
      tag: "v0.4.0-beta.1",
      prerelease: true,
      latestStable: "v0.3.0",
    }),
    "v0.3.0",
  );
});

test("a prerelease with no stable release yet has nowhere to publish", () => {
  // Nothing backs /releases/latest/ until a stable ships, so there is no beta
  // channel to serve; the caller skips rather than guessing a target.
  assert.equal(
    resolveBetaTarget({
      tag: "v0.4.0-beta.1",
      prerelease: true,
      latestStable: null,
    }),
    null,
  );
});
