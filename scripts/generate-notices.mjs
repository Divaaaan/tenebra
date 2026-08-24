#!/usr/bin/env node
// Regenerates THIRD-PARTY-NOTICES.md at the repository root.
//
// It reads the direct dependencies (and their pinned versions) straight from the
// project manifests, joins them with the verified license / copyright metadata
// below, and stitches in the full license texts kept under scripts/licenses/.
// The bundled runtime versions come from scripts/fetch-resources.ps1 so the
// notice tracks whatever the build actually ships.
//
// Software the application downloads onto the user's disk at run time has no
// manifest to read, so it is described by hand in RUNTIME below — and pinned by
// scripts/generate-notices.test.mjs, because "no manifest mentions it" is
// precisely how it went unattributed in the first place.
//
// Run: node scripts/generate-notices.mjs
//
// If a manifest gains a dependency that has no entry in META below, the script
// fails loudly so the notice never silently falls out of date.

import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
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
  "github.com/sagernet/gomobile": {
    license: "BSD-3-Clause",
    copyright: "Copyright (c) 2009 The Go Authors. All rights reserved.",
    note:
      "a fork of golang.org/x/mobile, pinned so the Android/iOS bind resolves; " +
      "its `bind` support package is compiled into the mobile artifacts and into " +
      "no desktop binary.",
  },
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
// Software fetched at run time rather than shipped. None of it is in this
// repository and none of it is in the installer, so no manifest names it — but
// the application puts every file of it on the user's disk, which is the same
// obligation by a different route.
//
// `bundleRepo` is the upstream the core actually downloads from; it is checked
// against core/zapret/update.go below so repointing the updater at a fork
// cannot leave this file crediting the wrong project.
//
// Versions: the bundle is fetched by "latest", not pinned, so the version of the
// archive is whatever upstream published. The versions recorded here are those
// of the third-party binaries *inside* it, read from the file resources of
// bundle 1.10.1 — they move on upstream's schedule, not on ours.
// ---------------------------------------------------------------------------
const bundleRepo = "Flowseal/zapret-discord-youtube";

// The bundle release compiled into the core (core/zapret/embedded.go). Named here
// so the notice states what actually ships rather than a version that has moved.
const EMBEDDED_VERSION = "1.10.2";
const EMBEDDED_ARCHIVE = `zapret-discord-youtube-${EMBEDDED_VERSION}.zip`;

const RUNTIME = [
  {
    name: "zapret",
    title: "zapret",
    licenseTokens: ["MIT"],
    lines: [
      "- Components: `bin/winws.exe` — the Windows DPI-bypass engine — together with",
      "  the packet templates and host lists it reads",
      "- License: MIT (full text in section 7)",
      "- Copyright: Copyright (c) 2016-2024 bol-van",
      "- Source: <https://github.com/bol-van/zapret>",
      "- Attribution: winws is the program that does the actual work of the bypass.",
      "  Tenebra neither builds nor modifies it: the core starts it as a separate",
      "  process with a strategy chosen from those the bundle ships.",
    ],
  },
  {
    name: "zapret-discord-youtube",
    title: "zapret-discord-youtube (bundle)",
    licenseTokens: ["MIT"],
    lines: [
      "- Component: the release archive `zapret-discord-youtube-<version>.zip` —",
      "  the strategy batch files, the host and IP lists, and the `bin/` directory",
      "  whose contents are covered by the three entries around this one",
      "- License: MIT (full text in section 7)",
      "- Copyright: Copyright (c) 2016-2026 bol-van; Copyright (c) 2024-2026 Flowseal",
      `- Source: <https://github.com/${bundleRepo}>`,
      "- Attribution: This is the archive the updater downloads and the one compiled",
      "  into the core, unmodified and whole either way. It is a packaging project:",
      "  the strategies and lists are its own work, the binaries in `bin/` are",
      "  redistributed from the projects above and below.",
    ],
  },
  {
    name: "WinDivert",
    title: "WinDivert",
    licenseTokens: ["LGPL-3.0"],
    lines: [
      "- Components: `bin/WinDivert.dll`, `bin/WinDivert64.sys` (WinDivert 2.2)",
      "- License: LGPL-3.0-or-later **or** GPL-2.0, at the recipient's choice —",
      "  **Tenebra takes WinDivert under the LGPLv3 branch**, and the LGPLv3 full",
      "  text is in section 7. The election is recorded rather than left open",
      "  because the alternative branch, GPLv2, is the one this project could not",
      "  use: GPLv2 and the GPLv3 that covers Tenebra do not combine.",
      "- Copyright: Copyright (C) Basil 2011-2022",
      "- Source: <https://github.com/basil00/WinDivert>",
      "- Attribution: WinDivert is the kernel driver and user-mode library winws",
      "  uses to intercept packets. It is redistributed unmodified inside the bundle",
      "  above; Tenebra links against none of it and calls none of it directly. The",
      "  LGPLv3 incorporates the GPLv3, whose text is in [LICENSE](LICENSE).",
    ],
  },
  {
    name: "Cygwin runtime",
    title: "Cygwin runtime",
    licenseTokens: ["LGPL-3.0"],
    lines: [
      "- Component: `bin/cygwin1.dll` (Cygwin 3.4.10)",
      "- License: LGPL-3.0-or-later, with the Cygwin Linking Exception (LGPLv3 full",
      "  text in section 7)",
      "- Copyright: Copyright (C) Cygwin Authors 1996-2023",
      "- Source: <https://cygwin.com/>, <https://cygwin.com/git/> (`newlib-cygwin`)",
      "- Attribution: winws is built against Cygwin, so the bundle carries the",
      "  Cygwin API library beside it. Redistributed unmodified. Upstream states the",
      "  exception as:",
      "",
      "  > As a special exception, the copyright holders of the Cygwin library grant",
      "  > you additional permission to link libcygwin.a, crt0.o, and gcrt0.o with",
      "  > independent modules to produce an executable, and to convey the resulting",
      "  > executable under terms of your choice, without any need to comply with the",
      "  > conditions of LGPLv3 section 4. An independent module is a module which is",
      "  > not itself based on the Cygwin library.",
    ],
  },
];

// bundleRepoFromAPI reads the `owner/repo` out of a GitHub releases API URL, and
// returns null for anything that is not one. Exported so the test can compare
// the notice against the feed core/zapret/update.go really talks to.
export function bundleRepoFromAPI(url) {
  const m = /^https:\/\/api\.github\.com\/repos\/([^/]+\/[^/]+)\/releases\//.exec(url || "");
  return m ? m[1] : null;
}

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
function loadInputs() {
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
    throw new Error(
      "Missing license metadata for: " +
        missing.map((d) => d.name).join(", ") +
        "\nAdd an entry to META in scripts/generate-notices.mjs, then re-run.",
    );
  }
  // Reverse check: flag metadata that no longer matches any parsed dependency, which
  // usually means a dropped dependency (remove it from META) or a parser miss.
  const parsedNames = new Set(allDeps.map((d) => d.name));
  const orphaned = Object.keys(META).filter((k) => !parsedNames.has(k));
  if (orphaned.length) {
    console.warn("Warning: META has entries not found in any manifest: " + orphaned.join(", "));
  }

  // The same guard for the one component no manifest declares: the bundle the
  // core downloads. A fork, a rename or a mirror changes who has to be credited,
  // and the only place that fact lives is the release feed in the Go source.
  const releaseAPI = /releaseAPI\s*=\s*"([^"]+)"/.exec(
    read(join(root, "core", "zapret", "update.go")),
  )?.[1];
  const repo = bundleRepoFromAPI(releaseAPI);
  if (repo !== bundleRepo) {
    throw new Error(
      `The bypass bundle is downloaded from ${repo || releaseAPI || "an unreadable feed"}, ` +
        `but this script credits ${bundleRepo}.\n` +
        "Update RUNTIME in scripts/generate-notices.mjs to match, then re-run.",
    );
  }

  return { goDeps, cargoDeps, npmDeps, singboxVersion, wintunVersion };
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

// Package lists per full-text license, derived from META and RUNTIME (so they
// self-update). The runtime components come last: they are not packages, and a
// reader scanning the list for a dependency name should meet those first.
function usersOf(groups, token) {
  const names = [];
  const seen = new Set();
  for (const group of groups) {
    for (const d of group) {
      if (seen.has(d.name)) continue;
      if (META[d.name].license.split(/\s+OR\s+/).includes(token)) {
        names.push(d.name);
        seen.add(d.name);
      }
    }
  }
  for (const c of RUNTIME) {
    if (c.licenseTokens.includes(token) && !seen.has(c.name)) {
      names.push(c.name);
      seen.add(c.name);
    }
  }
  return names;
}

// ---------------------------------------------------------------------------
// Assemble the document
// ---------------------------------------------------------------------------
export function renderNotices(inputs = loadInputs()) {
  const { goDeps, cargoDeps, npmDeps, singboxVersion, wintunVersion } = inputs;
  const depGroups = [goDeps, cargoDeps, npmDeps];
  const mitUsers = usersOf(depGroups, "MIT");
  const apacheUsers = usersOf(depGroups, "Apache-2.0");
  const bsdUsers = usersOf(depGroups, "BSD-3-Clause");
  const oflUsers = usersOf(depGroups, "OFL-1.1");
  const lgplUsers = usersOf(depGroups, "LGPL-3.0");

  const out = [];
  const p = (s = "") => out.push(s);

  p("# Third-Party Notices");
  p();
  p("Tenebra is distributed under the GNU General Public License, version 3; the");
  p("full text of that license is in the [LICENSE](LICENSE) file at the repository");
  p("root. In addition, Tenebra redistributes, links against, compiles in, or");
  p("downloads onto the user's machine the third-party software listed below. This");
  p("file aggregates the copyright notices, license identifiers, and attributions");
  p("those components require. It is generated by `scripts/generate-notices.mjs`");
  p("from the project manifests; edit that script rather than this file.");
  p();
  p("Contents:");
  p();
  p("1. Bundled runtime components");
  p("2. Components downloaded at runtime");
  p("3. Go core dependencies");
  p("4. Rust (Tauri) dependencies");
  p("5. Frontend (npm) dependencies");
  p("6. Bundled fonts");
  p("7. Full license texts");
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
  p("  appears in section 7. `geosite-ads.srs` (the ad/tracker blocklist behind the");
  p("  optional DNS ad-blocker) is the `category-ads-all` aggregate from that list.");
  p();

  // --- 2. Components downloaded at runtime ---
  p("## 2. Components downloaded at runtime");
  p();
  p("The DPI-bypass bundle reaches the user's disk two ways, and both are listed");
  p("here. One release of it is compiled into the core binary and installed when a");
  p("download cannot deliver one, so this project does redistribute that copy —");
  p("unmodified, under the licenses recorded below. Anything newer is fetched from");
  p("the upstream release page at runtime and is in neither the repository nor the");
  p("installer.");
  p();
  p(`**What ships.** \`core/zapret/bundled/${EMBEDDED_ARCHIVE}\` — the`);
  p("release archive exactly as upstream publishes it, whose checksum is checked");
  p("against the pin this client carries for that version on every build. It is");
  p("compiled into the Go core (`//go:embed`) and unpacked only when no bundle is");
  p("installed and upstream cannot supply one: no network, an unreachable release");
  p("page, a published version this build carries no checksum for, or an archive");
  p("that did not match the checksum it does carry.");
  p();
  p("**What is downloaded.** On a connect with no bundle installed, the core asks the");
  p("release feed for the latest published version and installs it when this build");
  p("pins that version's checksum, then re-checks every twelve hours — so a release");
  p("newer than the compiled-in copy replaces it. The archive is taken whole and");
  p("unmodified; only the wrapper directory the release ships is stripped. Either");
  p("way it lands in the core's own data directory —");
  p("`%ProgramData%\\Tenebra\\data\\zapret` under the Windows service,");
  p("`<user config dir>\\tenebra\\zapret` for a console run.");
  p();
  p("**How to decline it.** The first-connect install — downloaded or compiled-in —");
  p("and the periodic check are all bound to *Update the bundle automatically* in");
  p("Settings. With that off the application installs none of this on its own: only");
  p("an explicit press of *Update*, or a bundle dragged in by hand, puts anything");
  p("there, and deleting the `zapret` directory removes what is already installed.");
  p("Without a bundle the tunnel still carries everything; it carries it through the");
  p("tunnel rather than around the censor.");
  p();
  p(`Versions below are those of the binaries inside bundle ${EMBEDDED_VERSION}, the`);
  p("release compiled into this build. A newer bundle installed from upstream may");
  p("move them.");
  p();

  for (const c of RUNTIME) {
    p(`### ${c.title}`);
    p();
    for (const line of c.lines) p(line);
    p();
  }

  // --- 3. Go core dependencies ---
  p("## 3. Go core dependencies");
  p();
  p("Direct dependencies from `go.mod`. Everything else `go.sum` carries is pulled");
  p("in by the gomobile bind tool listed here and runs only at build time.");
  p();
  p(depLines(goDeps));
  p();

  // --- 4. Rust dependencies ---
  p("## 4. Rust (Tauri) dependencies");
  p();
  p("Direct dependencies from `ui-desktop/src-tauri/Cargo.toml` (including the build");
  p("and Windows-target dependencies). Each is dual-licensed; a distributor may use");
  p("either of the listed licenses.");
  p();
  p(depLines(cargoDeps));
  p();

  // --- 5. npm dependencies ---
  p("## 5. Frontend (npm) dependencies");
  p();
  p("Direct production dependencies from `ui-desktop/package.json`. `devDependencies`");
  p("are build-time only and are not redistributed in the application, so they are");
  p("not listed here.");
  p();
  p(depLines(npmDeps));
  p();

  // --- 6. Bundled fonts ---
  p("## 6. Bundled fonts");
  p();
  p("The application embeds the following variable fonts (delivered as `.woff2`");
  p("assets compiled into the frontend bundle). Both are licensed under the SIL Open");
  p("Font License, Version 1.1, whose full text appears in section 7.");
  p();
  p("- **JetBrains Mono** — SIL OFL 1.1 — Copyright 2020 The JetBrains Mono Project");
  p("  Authors (<https://github.com/JetBrains/JetBrainsMono>)");
  p("- **Space Grotesk** — SIL OFL 1.1 — Copyright 2020 The Space Grotesk Project");
  p("  Authors (<https://github.com/floriankarsten/space-grotesk>)");
  p();

  // --- 7. Full license texts ---
  p("## 7. Full license texts");
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

  p("### GNU Lesser General Public License, Version 3");
  p();
  p("Applies to: " + lgplUsers.join(", ") + " — the components of the downloaded");
  p("bypass bundle in section 2. WinDivert offers a choice of this license or GPLv2;");
  p("Tenebra takes the LGPLv3. This license incorporates the GNU GPL version 3,");
  p("whose text is in [LICENSE](LICENSE) and is not repeated here.");
  p();
  p(fenced(licenseText("lgpl-3.0.txt")));
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

  return out.join("\n").replace(/\n+$/, "\n");
}

function main() {
  let inputs;
  try {
    inputs = loadInputs();
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  }
  writeFileSync(join(root, "THIRD-PARTY-NOTICES.md"), renderNotices(inputs));
  const { goDeps, cargoDeps, npmDeps, singboxVersion, wintunVersion } = inputs;
  console.log("Wrote THIRD-PARTY-NOTICES.md");
  console.log(
    `Components: ${goDeps.length} Go, ${cargoDeps.length} Rust, ${npmDeps.length} npm, ` +
      `${RUNTIME.length} downloaded at runtime; ` +
      `bundled sing-box ${singboxVersion}, wintun ${wintunVersion}.`,
  );
}

// Run only when invoked as a script, so the document can be rendered in a test
// without writing over the copy in the tree.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main();
}
