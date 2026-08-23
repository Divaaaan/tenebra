// Invariants the CI configuration itself has to hold. Every check here exists
// because the pipeline once reported success while shipping nothing, or shipped
// a release with files missing — failures that no amount of application testing
// can catch, because the defect is in the workflow, not in the product.
//
// Run with `node --test .github/scripts/`.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

const workflowDir = fileURLToPath(new URL("../workflows/", import.meta.url));

function workflow(name) {
  return readFileSync(new URL(`../workflows/${name}`, import.meta.url), "utf8");
}

function allWorkflows() {
  return readdirSync(workflowDir)
    .filter((f) => f.endsWith(".yml") || f.endsWith(".yaml"))
    .map((name) => ({ name, text: workflow(name) }));
}

/**
 * The `- ` items under every `steps:` key, each returned as its raw text.
 *
 * Reading the file as text rather than as parsed YAML keeps this suite free of
 * a parser dependency, and the checks below are about what the file literally
 * says anyway. Steps in these workflows are indented six spaces, which is what
 * the two-space job / four-space key / six-space list nesting produces; a
 * reformat that breaks that assumption makes these tests fail loudly rather
 * than pass vacuously, which is the failure mode worth having.
 */
function steps(text) {
  const out = [];
  let inSteps = false;
  let cur = null;
  const flush = () => {
    if (cur) out.push(cur.join("\n"));
    cur = null;
  };
  for (const line of text.split(/\r?\n/)) {
    if (/^ {4}steps:\s*$/.test(line)) {
      flush();
      inSteps = true;
      continue;
    }
    if (!inSteps) continue;
    if (line.trim() === "") {
      if (cur) cur.push(line);
      continue;
    }
    if (/^ {6}- /.test(line)) {
      flush();
      cur = [line];
      continue;
    }
    if (/^ {7,}/.test(line)) {
      if (cur) cur.push(line);
      continue;
    }
    // Dedented back out of the steps list.
    flush();
    inSteps = false;
  }
  flush();
  return out;
}

/** The top-level jobs of a workflow, keyed by job id. */
function jobs(text) {
  const map = new Map();
  const lines = text.split(/\r?\n/);
  let seenJobsKey = false;
  let name = null;
  let buf = [];
  const flush = () => {
    if (name) map.set(name, buf.join("\n"));
  };
  for (const line of lines) {
    if (/^jobs:\s*$/.test(line)) {
      seenJobsKey = true;
      continue;
    }
    if (!seenJobsKey) continue;
    const m = /^ {2}([A-Za-z0-9_-]+):\s*$/.exec(line);
    if (m) {
      flush();
      name = m[1];
      buf = [];
      continue;
    }
    if (name) buf.push(line);
  }
  flush();
  return map;
}

function stepNamed(text, name) {
  const found = steps(text).filter((s) => s.includes(`name: ${name}`));
  assert.equal(found.length, 1, `expected exactly one step named "${name}"`);
  return found[0];
}

test("the Arch attach step names the repository instead of asking git", () => {
  // The build step chowns the checkout to `builder` so makepkg can run, and this
  // step runs as root: gh's own repository resolution shells out to git, git
  // refuses a repository it does not own ("detected dubious ownership"), and gh
  // exits 1 before it ever reaches the API. Naming the repository through GH_REPO
  // keeps git out of the path entirely — the same reason
  // scripts/publish-beta-manifest.mjs passes --repo. The package never reached a
  // release until this was set; v0.4.6's was uploaded by hand.
  const step = stepNamed(workflow("release.yml"), "Attach the package to the release");
  assert.match(step, /GH_REPO: \$\{\{ github\.repository \}\}/);
});

test("the Arch attach step refuses to succeed on an empty glob", () => {
  // An unmatched glob expands to the literal pattern; gh would then either
  // upload nothing or fail obscurely. Fail on purpose instead, and run under
  // set -eu so an earlier command in the block cannot be skipped silently.
  const step = stepNamed(workflow("release.yml"), "Attach the package to the release");
  assert.match(step, /set -eu/);
  assert.match(step, /exit 1/);
});

test("no tauri job publishes the release before the assets are complete", () => {
  // All three tauri jobs upload into ONE release for the tag. Publishing from
  // the first of them puts a download page in front of users, and in front of
  // the winget workflow's `released` trigger, while two thirds of the files are
  // still building.
  const drafts = workflow("release.yml").match(/releaseDraft: \w+/g) ?? [];
  assert.equal(drafts.length, 3, "expected one releaseDraft per tauri job");
  for (const d of drafts) {
    assert.equal(d, "releaseDraft: true");
  }
});

test("a final job publishes the draft only after every build job", () => {
  const release = jobs(workflow("release.yml"));
  const publish = release.get("publish");
  assert.ok(publish, "release.yml has no publish job");
  for (const dep of ["windows", "macos", "linux", "arch-package"]) {
    assert.match(
      publish,
      new RegExp(`needs:.*\\b${dep}\\b`),
      `the publish job does not wait for ${dep}`,
    );
  }
  assert.match(publish, /publish-release\.mjs/);
});

test("the Android release cannot report success without building anything", () => {
  // A missing ANDROID_KEYSTORE_B64 used to resolve to signable=false, which
  // skipped the release job — and a skipped job counts as success, so a tag
  // finished green with no APK anywhere and no signal that one was expected.
  const android = workflow("android.yml");
  assert.doesNotMatch(
    android,
    /needs\.release-gate\.outputs/,
    "the release job is still gated on an output that lets it skip quietly",
  );
  const gate = jobs(android).get("release-gate");
  assert.ok(gate, "android.yml has no release-gate job");
  assert.doesNotMatch(gate, /^ {4}outputs:/m, "the gate still resolves a skip flag");
  assert.match(gate, /exit 1/, "the gate does not fail when the key is absent");
  assert.match(gate, /::error/, "the gate does not annotate the run");
});

test("every upload-artifact step fails when its glob matches nothing", () => {
  // Without if-no-files-found: error a mistyped path is a green job with no
  // artifact attached — the same class of silent success as the Android gate.
  for (const { name, text } of allWorkflows()) {
    for (const step of steps(text)) {
      if (!/uses: actions\/upload-artifact/.test(step)) continue;
      assert.match(
        step,
        /if-no-files-found: error/,
        `${name}: an upload-artifact step tolerates an empty glob:\n${step}`,
      );
    }
  }
});

test("no workflow makes an assertion that cannot fail", () => {
  // `grep -q ... || true` reads like a check in the log and is decorative: it
  // reports the same result whether or not the thing it looks for is there.
  for (const { name, text } of allWorkflows()) {
    for (const line of text.split(/\r?\n/)) {
      assert.doesNotMatch(
        line,
        /\bgrep\b.*\|\|\s*true\s*$/,
        `${name}: grep guarded by || true can never fail: ${line.trim()}`,
      );
    }
  }
});
