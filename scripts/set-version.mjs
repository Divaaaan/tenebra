// Single source of truth for the release version. Rewrites (or, with --check,
// verifies) the version string in every file that carries it, so a release can
// never ship with the copies out of sync.
//
//   node scripts/set-version.mjs 1.2.3          # write 1.2.3 into every file
//   node scripts/set-version.mjs --check        # assert all files already agree
//   node scripts/set-version.mjs 1.2.3 --check  # assert every file is 1.2.3
//
// The files are the desktop package manifest, the Tauri bundle config, the Rust
// crate manifest and its lockfile entry. The release workflow reads the version
// from tauri.conf.json and the updater's latest.json inherits it, so a stale
// copy would advertise the wrong version to installed clients or leave the
// lockfile behind (build with --locked to catch that). Run this instead of
// editing the four files by hand.

import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join } from "node:path";

const root = fileURLToPath(new URL("..", import.meta.url));

// Each target names a file and a regex whose three capture groups bracket the
// version literal as (prefix)(version)(suffix); replacing with `$1<new>$3` swaps
// only the number and leaves the surrounding formatting untouched. The patterns
// are anchored so they match the package's own version and nothing else — the
// Cargo.toml one uses a line-start anchor to skip inline dependency versions,
// and the Cargo.lock one is tied to this crate's package block.
const targets = [
  { file: "ui-desktop/package.json", re: /("version":\s*")([^"]+)(")/ },
  { file: "ui-desktop/src-tauri/tauri.conf.json", re: /("version":\s*")([^"]+)(")/ },
  { file: "ui-desktop/src-tauri/Cargo.toml", re: /(^version = ")([^"]+)(")/m },
  {
    file: "ui-desktop/src-tauri/Cargo.lock",
    re: /(name = "tenebra-desktop"\r?\nversion = ")([^"]+)(")/,
  },
  // The Go core's own copy: the daemon reports it in every State snapshot so a
  // GUI can spot a stale hand-installed daemon (the macOS LaunchDaemon path).
  { file: "core/buildinfo/buildinfo.go", re: /(^const Version = ")([^"]+)(")/m },
];

const SEMVER = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;

function fail(message) {
  console.error(`set-version: ${message}`);
  process.exit(1);
}

const args = process.argv.slice(2);
const check = args.includes("--check");
// Accept a v-prefixed value (e.g. a git tag like v1.2.3) and normalise it to the
// bare version the files store, so the release workflow can pass the tag as-is.
const rawWanted = args.find((a) => !a.startsWith("-"));
const wanted = rawWanted ? rawWanted.replace(/^v/, "") : undefined;

if (!wanted && !check) {
  fail("usage: node scripts/set-version.mjs <version> [--check]");
}
if (wanted && !SEMVER.test(wanted)) {
  fail(`not a valid semantic version: "${rawWanted}"`);
}

// Read every file up front and pull out its current version, so both modes work
// from the same view and a moved/renamed field is reported instead of silently
// skipped.
const files = targets.map((t) => {
  const path = join(root, t.file);
  const text = readFileSync(path, "utf8");
  const match = t.re.exec(text);
  if (!match) {
    fail(`could not find a version field in ${t.file}`);
  }
  return { ...t, path, text, current: match[2] };
});

if (check) {
  const target = wanted ?? files[0].current;
  const mismatched = files.filter((f) => f.current !== target);
  for (const f of files) {
    const ok = f.current === target ? "ok" : "MISMATCH";
    console.log(`  ${f.current.padEnd(12)} ${f.file}  [${ok}]`);
  }
  if (mismatched.length > 0) {
    fail(
      wanted
        ? `expected every file to be ${wanted}`
        : "version files disagree; run `node scripts/set-version.mjs <version>` to sync them",
    );
  }
  console.log(`set-version: all files are ${target}`);
} else {
  let changed = 0;
  for (const f of files) {
    if (f.current === wanted) {
      console.log(`  ${f.file}  already ${wanted}`);
      continue;
    }
    const next = f.text.replace(f.re, `$1${wanted}$3`);
    writeFileSync(f.path, next);
    console.log(`  ${f.file}  ${f.current} -> ${wanted}`);
    changed += 1;
  }
  console.log(`set-version: ${changed} file(s) updated to ${wanted}`);
}
