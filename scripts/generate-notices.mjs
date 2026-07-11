#!/usr/bin/env node
// Regenerates THIRD-PARTY-NOTICES.md at the repository root.
//
// It reads the direct dependencies (and their pinned versions) straight from the
// project manifests, joins them with the verified license / copyright metadata
// below, and stitches in the full license texts kept under scripts/licenses/.
// The bundled runtime versions come from scripts/fetch-resources.ps1 so the
// notice tracks whatever the build actually ships.
//
// Run: node scripts/generate-notices.mjs
//
// If a manifest gains a dependency that has no entry in META below, the script
// fails loudly so the notice never silently falls out of date.

import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const root = join(scriptDir, "..");
const licenseDir = join(scriptDir, "licenses");

const read = (p) => readFileSync(p, "utf8");
const licenseText = (name) =>
  read(join(licenseDir, name))
    .replace(/^(?:[ \t]*\r?\n)+/, "") // drop leading blank lines
    .replace(/\s+$/, ""); // drop trailing whitespace

// ---------------------------------------------------------------------------
// Verified license + copyright metadata (July 2026). Keyed by the exact name
// that appears in the manifest. `license` uses SPDX identifiers; `OR` marks a
// dual-licensed component the distributor may use under either license.
// ---------------------------------------------------------------------------
const META = {
  // Go modules (go.mod)
  "github.com/Microsoft/go-winio": { license: "MIT", copyright: "Copyright (c) 2015 Microsoft" },
  "golang.org/x/sys": { license: "BSD-3-Clause", copyright: "Copyright 2009 The Go Authors" },

  // Rust crates (src-tauri/Cargo.toml)
  tauri: { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "tauri-build": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "tauri-plugin-dialog": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "tauri-plugin-shell": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "tauri-plugin-autostart": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "tauri-plugin-single-instance": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "tauri-plugin-deep-link": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "tauri-plugin-notification": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "tauri-plugin-updater": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "tauri-plugin-process": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  serde: { license: "MIT OR Apache-2.0", copyright: "The Serde developers" },
  serde_json: { license: "MIT OR Apache-2.0", copyright: "The Serde developers" },
  url: { license: "MIT OR Apache-2.0", copyright: "Copyright (c) 2013-2025 The rust-url developers" },
  open: { license: "MIT", copyright: "Copyright (c) 2015 Sebastian Thiel" },
  "windows-sys": { license: "MIT OR Apache-2.0", copyright: "Copyright (c) Microsoft Corporation" },

  // npm production dependencies (ui-desktop/package.json)
  "@tauri-apps/api": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "@tauri-apps/plugin-autostart": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "@tauri-apps/plugin-dialog": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "@tauri-apps/plugin-process": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "@tauri-apps/plugin-shell": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  "@tauri-apps/plugin-updater": { license: "Apache-2.0 OR MIT", copyright: "Copyright (c) 2017 - Present Tauri Apps Contributors" },
  react: { license: "MIT", copyright: "Copyright (c) Facebook, Inc. and its affiliates" },
  "react-dom": { license: "MIT", copyright: "Copyright (c) Facebook, Inc. and its affiliates" },
  "@fontsource-variable/jetbrains-mono": {
    license: "OFL-1.1",
    copyright: "Copyright 2020 The JetBrains Mono Project Authors (https://github.com/JetBrains/JetBrainsMono)",
    note: "npm package MIT-licensed; the bundled font files are under the SIL Open Font License 1.1.",
  },
  "@fontsource-variable/space-grotesk": {
    license: "OFL-1.1",
    copyright: "Copyright 2020 The Space Grotesk Project Authors (https://github.com/floriankarsten/space-grotesk)",
    note: "npm package MIT-licensed; the bundled font files are under the SIL Open Font License 1.1.",
  },
};

// ---------------------------------------------------------------------------
// Manifest parsers
// ---------------------------------------------------------------------------
function parseGoMod(text) {
  const deps = [];
  const block = text.match(/require\s*\(([\s\S]*?)\)/);
  const body = block ? block[1] : text;
  for (const line of body.split("\n")) {
    const m = line.match(/^\s*(\S+)\s+(v\S+?)(?:\s+\/\/.*)?\s*$/);
    if (m && m[1] !== "require") deps.push({ name: m[1], version: m[2] });
  }
  return deps;
}

function parseCargoDeps(text) {
  const deps = [];
  let inDeps = false;
  for (const raw of text.split("\n")) {
    const line = raw.replace(/\r$/, "");
    const section = line.match(/^\s*\[([^\]]+)\]\s*$/);
    if (section) {
      const name = section[1];
      // Runtime, build, and target-specific dependency tables ship code into the
      // binary. [dev-dependencies] is test/bench-only and is intentionally skipped.
      inDeps =
        name === "dependencies" ||
        name === "build-dependencies" ||
        name.endsWith(".dependencies");
      continue;
    }
    if (!inDeps) continue;
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    const m = trimmed.match(/^([A-Za-z0-9_-]+)\s*=\s*(.+)$/);
    if (!m) continue; // continuation lines (e.g. feature array items) are skipped
    const name = m[1];
    const value = m[2];
    let version = null;
    const vm = value.match(/version\s*=\s*"([^"]+)"/) || value.match(/^"([^"]+)"/);
    if (vm) version = vm[1];
    deps.push({ name, version });
  }
  return deps;
}

function parsePkgDeps(text) {
  const json = JSON.parse(text);
  return Object.entries(json.dependencies || {}).map(([name, version]) => ({ name, version }));
}

function scalar(text, varName) {
  const m = text.match(new RegExp(`\\$${varName}\\s*=\\s*"([^"]+)"`));
  return m ? m[1] : "unknown";
}

// ---------------------------------------------------------------------------
// Load manifests
// ---------------------------------------------------------------------------
const goDeps = parseGoMod(read(join(root, "go.mod")));
const cargoDeps = parseCargoDeps(read(join(root, "ui-desktop", "src-tauri", "Cargo.toml")));
const npmDeps = parsePkgDeps(read(join(root, "ui-desktop", "package.json")));
const fetchScript = read(join(root, "scripts", "fetch-resources.ps1"));
const singboxVersion = scalar(fetchScript, "singboxVersion");
const wintunVersion = scalar(fetchScript, "wintunVersion");

// Drift guard: every parsed dependency must have metadata.
const allDeps = [...goDeps, ...cargoDeps, ...npmDeps];
const missing = allDeps.filter((d) => !META[d.name]);
if (missing.length) {
  console.error("Missing license metadata for: " + missing.map((d) => d.name).join(", "));
  console.error("Add an entry to META in scripts/generate-notices.mjs, then re-run.");
  process.exit(1);
}
// Reverse check: flag metadata that no longer matches any parsed dependency, which
// usually means a dropped dependency (remove it from META) or a parser miss.
const parsedNames = new Set(allDeps.map((d) => d.name));
const orphaned = Object.keys(META).filter((k) => !parsedNames.has(k));
if (orphaned.length) {
  console.warn("Warning: META has entries not found in any manifest: " + orphaned.join(", "));
}

// ---------------------------------------------------------------------------
// Rendering helpers
// ---------------------------------------------------------------------------
const fenced = (text) => "```text\n" + text + "\n```";

function depLines(deps) {
  return deps
    .map((d) => {
      const meta = META[d.name];
      const ver = d.version ? ` \`${d.version}\`` : "";
      const note = meta.note ? ` — ${meta.note}` : "";
      return `- **${d.name}**${ver} — ${meta.license} — ${meta.copyright}${note}`;
    })
    .join("\n");
}

// Package lists per full-text license, derived from META (so they self-update).
function usersOf(token) {
  const names = [];
  const seen = new Set();
  for (const group of [goDeps, cargoDeps, npmDeps]) {
    for (const d of group) {
      if (seen.has(d.name)) continue;
      if (META[d.name].license.split(/\s+OR\s+/).includes(token)) {
        names.push(d.name);
        seen.add(d.name);
      }
    }
  }
  return names;
}

const mitUsers = usersOf("MIT");
const apacheUsers = usersOf("Apache-2.0");
const bsdUsers = usersOf("BSD-3-Clause");
const oflUsers = usersOf("OFL-1.1");

// ---------------------------------------------------------------------------
// Assemble the document
// ---------------------------------------------------------------------------
const out = [];
const p = (s = "") => out.push(s);

p("# Third-Party Notices");
p();
p("Tenebra is distributed under the GNU General Public License, version 3; the");
p("full text of that license is in the [LICENSE](LICENSE) file at the repository");
p("root. In addition, Tenebra redistributes, links against, or compiles in the");
p("third-party software listed below. This file aggregates the copyright notices,");
p("license identifiers, and attributions those components require. It is generated");
p("by `scripts/generate-notices.mjs` from the project manifests; edit that script");
p("rather than this file.");
p();
p("Contents:");
p();
p("1. Bundled runtime components");
p("2. Go core dependencies");
p("3. Rust (Tauri) dependencies");
p("4. Frontend (npm) dependencies");
p("5. Bundled fonts");
p("6. Full license texts");
p();

// --- 1. Bundled runtime components ---
p("## 1. Bundled runtime components");
p();
p("These files are produced by `scripts/fetch-resources.ps1` / `.sh` and shipped");
p("inside the installer (declared under `bundle.resources` in");
p("`ui-desktop/src-tauri/tauri.conf.json`). They are redistributed unmodified.");
p();

p("### Wintun");
p();
p(`- Component: \`wintun.dll\` (Wintun ${wintunVersion})`);
p("- License: Wintun Prebuilt Binaries License (a separate license from WireGuard");
p("  LLC — the prebuilt DLL is **not** distributed under the GPLv2 that covers the");
p("  Wintun source)");
p("- Copyright: WireGuard LLC");
p("- Attribution: This is the official prebuilt build downloaded from");
p("  <https://www.wintun.net/builds>, bundled without modification. Tenebra does");
p("  not call Wintun directly; sing-box loads it through the documented Permitted");
p("  API declared in `wintun.h`. The DLL is redistributed alongside software that");
p("  uses it only via that Permitted API, which this license allows. Full text:");
p();
p(fenced(licenseText("wintun-prebuilt-binaries-license.txt")));
p();

p("### sing-box");
p();
p(`- Component: \`sing-box.exe\` (sing-box ${singboxVersion}, official windows-amd64 release)`);
p("- License: GPL-3.0-or-later, with an additional term under section 7 of the GPL");
p("- Copyright: Copyright (C) 2022 by nekohasekai <contact-sagernet@sekai.icu>");
p("- Source: <https://github.com/SagerNet/sing-box>");
p("- Attribution: The binary is bundled unmodified and invoked as a separate");
p("  process. The full GPLv3 text is not duplicated here — it is the same license");
p("  Tenebra ships in [LICENSE](LICENSE). The sing-box license adds the following");
p("  term under GPLv3 section 7, quoted verbatim:");
p();
p("  > In addition, no derivative work may use the name or imply association");
p("  > with this application without prior consent.");
p();

p("### GeoIP rule-set");
p();
p("- Component: `geoip-ru.srs`");
p("- Source: <https://github.com/SagerNet/sing-geoip> (`rule-set` branch,");
p("  `geoip-ru.srs`)");
p("- License: GPL-3.0-or-later — Copyright (C) 2022 by nekohasekai");
p("  <contact-sagernet@sekai.icu>");
p("- Data attribution: The rule-set is compiled from the MaxMind GeoLite2 Country");
p("  database (obtained via <https://github.com/Dreamacro/maxmind-geoip>). MaxMind");
p("  requires the following attribution:");
p();
p("  > This product includes GeoLite2 Data created by MaxMind, available from");
p("  > <https://www.maxmind.com>.");
p();

p("### GeoSite rule-sets");
p();
p("- Components: `geosite-ru.srs`, `geosite-ads.srs`");
p("- Source: <https://github.com/SagerNet/sing-geosite> (`rule-set` branch,");
p("  `geosite-category-ru.srs` and `geosite-category-ads-all.srs`)");
p("- License: GPL-3.0-or-later — Copyright (C) 2022 by nekohasekai");
p("  <contact-sagernet@sekai.icu>");
p("- Data attribution: The rule-sets are compiled from the v2fly community domain");
p("  list, <https://github.com/v2fly/domain-list-community>, which is distributed");
p("  under the MIT License — Copyright (c) 2018-2019 V2Ray. The MIT License text");
p("  appears in section 6. `geosite-ads.srs` (the ad/tracker blocklist behind the");
p("  optional DNS ad-blocker) is the `category-ads-all` aggregate from that list.");
p();

// --- 2. Go core dependencies ---
p("## 2. Go core dependencies");
p();
p("Direct dependencies from `go.mod`. The module graph contains no other");
p("dependencies (`go.sum` lists only these two modules).");
p();
p(depLines(goDeps));
p();

// --- 3. Rust dependencies ---
p("## 3. Rust (Tauri) dependencies");
p();
p("Direct dependencies from `ui-desktop/src-tauri/Cargo.toml` (including the build");
p("and Windows-target dependencies). Each is dual-licensed; a distributor may use");
p("either of the listed licenses.");
p();
p(depLines(cargoDeps));
p();

// --- 4. npm dependencies ---
p("## 4. Frontend (npm) dependencies");
p();
p("Direct production dependencies from `ui-desktop/package.json`. `devDependencies`");
p("are build-time only and are not redistributed in the application, so they are");
p("not listed here.");
p();
p(depLines(npmDeps));
p();

// --- 5. Bundled fonts ---
p("## 5. Bundled fonts");
p();
p("The application embeds the following variable fonts (delivered as `.woff2`");
p("assets compiled into the frontend bundle). Both are licensed under the SIL Open");
p("Font License, Version 1.1, whose full text appears in section 6.");
p();
p("- **JetBrains Mono** — SIL OFL 1.1 — Copyright 2020 The JetBrains Mono Project");
p("  Authors (<https://github.com/JetBrains/JetBrainsMono>)");
p("- **Space Grotesk** — SIL OFL 1.1 — Copyright 2020 The Space Grotesk Project");
p("  Authors (<https://github.com/floriankarsten/space-grotesk>)");
p();

// --- 6. Full license texts ---
p("## 6. Full license texts");
p();
p("The copyright holder that applies to each package is listed in its entry above.");
p("The following are the full texts of the licenses referenced in this file. The");
p("GPL-3.0 text that covers Tenebra and the bundled sing-box components is in");
p("[LICENSE](LICENSE) and is not repeated here.");
p();

p("### MIT License");
p();
p("Applies to: " + mitUsers.join(", ") + ".");
p();
p(fenced(licenseText("mit.txt")));
p();

p("### BSD 3-Clause License");
p();
p("Applies to: " + bsdUsers.join(", ") + ".");
p();
p(fenced(licenseText("bsd-3-clause.txt")));
p();

p("### Apache License 2.0");
p();
p("Applies to: " + apacheUsers.join(", ") + ".");
p();
p(fenced(licenseText("apache-2.0.txt")));
p();

p("### SIL Open Font License, Version 1.1");
p();
p("Applies to the bundled fonts: JetBrains Mono, Space Grotesk (npm packages: " + oflUsers.join(", ") + ").");
p();
p(fenced(licenseText("ofl-1.1.txt")));
p();

writeFileSync(join(root, "THIRD-PARTY-NOTICES.md"), out.join("\n").replace(/\n+$/, "\n"));
console.log("Wrote THIRD-PARTY-NOTICES.md");
console.log(
  `Components: ${goDeps.length} Go, ${cargoDeps.length} Rust, ${npmDeps.length} npm; ` +
    `bundled sing-box ${singboxVersion}, wintun ${wintunVersion}.`,
);
