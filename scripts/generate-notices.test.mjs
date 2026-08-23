// Unit tests for the notice generator. Most of THIRD-PARTY-NOTICES.md is
// derived from manifests, so the interesting part is what no manifest mentions:
// the DPI-bypass bundle the core downloads on the first connect and unpacks on
// the user's disk. Nothing in a package file records that, which is exactly how
// it went unattributed — so it is pinned here instead.
//
// Run with `node --test scripts/`.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { renderNotices, bundleRepoFromAPI } from "./generate-notices.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const doc = renderNotices();

// The runtime section, sliced out so an assertion about it cannot pass on a
// coincidental match somewhere else in a 30 KB document.
function runtimeSection() {
  const start = doc.indexOf("## 2. Components downloaded at runtime");
  assert.notEqual(start, -1, "no runtime-components section in the notice");
  const end = doc.indexOf("\n## ", start + 1);
  return doc.slice(start, end === -1 ? undefined : end);
}

test("the bundle downloaded on the first connect is attributed", () => {
  // Each of these arrives on disk through this app and none is named by a
  // manifest: zapret itself, the packaging repo the archive comes from, the
  // packet-filter driver inside it, and the C runtime winws is built against.
  const section = runtimeSection();
  for (const needle of [
    "github.com/bol-van/zapret",
    "github.com/Flowseal/zapret-discord-youtube",
    "WinDivert.dll",
    "WinDivert64.sys",
    "cygwin1.dll",
  ]) {
    assert.ok(section.includes(needle), `runtime section does not mention ${needle}`);
  }
});

test("every runtime component carries its upstream copyright line", () => {
  const section = runtimeSection();
  for (const holder of [
    "bol-van",
    "Flowseal",
    "Basil",
    "Cygwin Authors",
  ]) {
    assert.ok(section.includes(holder), `runtime section does not credit ${holder}`);
  }
});

test("WinDivert is taken under the LGPLv3 branch, and says so", () => {
  // WinDivert is the user's-choice dual licence, and "your choice" is only an
  // answer once someone makes it. GPLv2 is the branch this project cannot use —
  // GPLv2 and GPLv3 do not combine — so the election has to be on the record.
  const section = runtimeSection();
  assert.match(section, /LGPL-3\.0|LGPLv3/);
  assert.match(section, /GPL-2\.0|GPLv2/);
  assert.match(
    section,
    /Tenebra (?:takes|elects|uses) WinDivert under (?:the )?LGPL/i,
    "the LGPLv3 election is not stated explicitly",
  );
});

test("the notice says where the bundle lands and how to stop it arriving", () => {
  // The complaint this section answers is not only "unattributed" but "silent":
  // a user cannot consent to, or clean up, something they were never told about.
  const section = runtimeSection();
  assert.match(section, /ProgramData/);
  assert.match(section, /automatic|automatically/i);
});

test("the LGPLv3 text and the Cygwin linking exception ship with the notice", () => {
  assert.ok(
    doc.includes("GNU LESSER GENERAL PUBLIC LICENSE"),
    "the LGPLv3 full text is missing",
  );
  // Matched on a phrase short enough to survive the quote block's line wrapping.
  assert.ok(
    doc.includes("conditions of LGPLv3 section 4"),
    "the Cygwin linking exception is not quoted",
  );
});

test("the MIT applies-to list covers the downloaded bundle", () => {
  // The MIT text is printed once with a list of who it covers. A component that
  // relies on that text and is missing from the list is attributed by accident.
  const start = doc.indexOf("### MIT License");
  assert.notEqual(start, -1);
  const appliesTo = doc.slice(start, start + 600);
  assert.match(appliesTo, /zapret/);
});

test("the notice tracks the release feed the core actually fetches", () => {
  // If someone repoints the updater at a fork, the attribution must follow it
  // rather than keep crediting the repo that is no longer being downloaded.
  const update = readFileSync(join(root, "core", "zapret", "update.go"), "utf8");
  const url = update.match(/releaseAPI\s*=\s*"([^"]+)"/)?.[1];
  assert.ok(url, "could not find releaseAPI in core/zapret/update.go");
  const repo = bundleRepoFromAPI(url);
  assert.ok(repo, `could not read a repository out of ${url}`);
  assert.ok(
    runtimeSection().includes(`github.com/${repo}`),
    `the notice does not name ${repo}, the repository the core downloads from`,
  );
});

test("bundleRepoFromAPI reads owner/repo out of a releases URL", () => {
  assert.equal(
    bundleRepoFromAPI("https://api.github.com/repos/Flowseal/zapret-discord-youtube/releases/latest"),
    "Flowseal/zapret-discord-youtube",
  );
  assert.equal(bundleRepoFromAPI("https://example.invalid/feed.json"), null);
  assert.equal(bundleRepoFromAPI(""), null);
});

test("the table of contents matches the sections that follow it", () => {
  // Inserting a section in the middle is exactly the edit that leaves a document
  // pointing at the wrong numbers, and every cross-reference in it is by number.
  // Only the contents block: the quoted license texts are full of numbered
  // clauses that look exactly like a table-of-contents line.
  const contents = doc.slice(doc.indexOf("Contents:"), doc.indexOf("\n## "));
  const listed = [...contents.matchAll(/^(\d+\. .+)$/gm)].map((m) => m[1]);
  const headings = [...doc.matchAll(/^## (\d+\. .+)$/gm)].map((m) => m[1]);
  assert.ok(listed.length > 0, "no contents block found");
  assert.deepEqual(listed, headings);
});

test("the committed notice is what the generator produces", () => {
  // The file carries a "generated, do not hand-edit" header, so a hand edit — or
  // a generator change nobody re-ran — has to be caught somewhere.
  const committed = readFileSync(join(root, "THIRD-PARTY-NOTICES.md"), "utf8").replace(/\r\n/g, "\n");
  assert.equal(committed, doc, "THIRD-PARTY-NOTICES.md is stale — run node scripts/generate-notices.mjs");
});
