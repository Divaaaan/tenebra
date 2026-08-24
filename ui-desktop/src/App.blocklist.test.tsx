import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";

import type { State } from "./api";
import { App } from "./App";
import { dictionaries } from "./i18n/strings";
import { renderWithProviders } from "./test/renderWithProviders";

// What the bypass panel accepts, and — the point of this suite — what it must
// not pretend to accept. The panel takes a zapret bundle, which the core really
// installs and probes. A domain blocklist has no command behind it: the app can
// parse one, but nothing anywhere can apply it, so the import must fail loudly
// instead of listing a rule count that reads as "loaded".

const en = dictionaries.en;

const profile = {
  id: "p1",
  name: "Example subscription",
  source: "subscription" as const,
  url: "https://example.invalid/sub",
  nodes: [
    {
      id: "n1",
      name: "EX-TEST-01",
      protocol: "vless" as const,
      server: "198.51.100.10",
      port: 443,
    },
  ],
  updatedAt: "2026-01-01T00:00:00Z",
};

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  listProfiles: vi.fn(),
  connect: vi.fn(),
  disconnect: vi.fn(),
  ping: vi.fn(),
  leakCheck: vi.fn(),
  importZapret: vi.fn(),
  importZapretPath: vi.fn(),
  pickZapret: vi.fn(),
  startZapret: vi.fn(),
  stopZapret: vi.fn(),
  onState: vi.fn(),
  onTraffic: vi.fn(),
  onLog: vi.fn(),
  onProfilesChanged: vi.fn(),
  onAttempts: vi.fn(),
  onTrayConnect: vi.fn(),
  onTrayShow: vi.fn(),
  onDeepLink: vi.fn(),
  takeLaunchDeepLinks: vi.fn(),
}));

vi.mock("./api", () => ({
  api: {
    status: mocks.status,
    listProfiles: mocks.listProfiles,
    connect: mocks.connect,
    disconnect: mocks.disconnect,
    ping: mocks.ping,
    leakCheck: mocks.leakCheck,
    importZapret: mocks.importZapret,
    importZapretPath: mocks.importZapretPath,
    pickZapret: mocks.pickZapret,
    startZapret: mocks.startZapret,
    stopZapret: mocks.stopZapret,
  },
  onState: mocks.onState,
  onTraffic: mocks.onTraffic,
  onLog: mocks.onLog,
  onProfilesChanged: mocks.onProfilesChanged,
  onAttempts: mocks.onAttempts,
  onTrayConnect: mocks.onTrayConnect,
  onTrayShow: mocks.onTrayShow,
  onDeepLink: mocks.onDeepLink,
  takeLaunchDeepLinks: mocks.takeLaunchDeepLinks,
}));

// The launch update check would otherwise reach the (absent) updater plugin.
vi.mock("./lib/updates", () => ({
  checkForUpdate: vi.fn().mockResolvedValue(null),
  inAppUpdatesSupported: vi.fn().mockResolvedValue(true),
  installUpdate: vi.fn().mockResolvedValue(undefined),
}));

// The panel subscribes to Tauri's OS-level drag-drop (that is how a dropped
// FOLDER arrives). There is no Tauri here, so hand it an inert subscription.
vi.mock("@tauri-apps/api/webview", () => ({
  getCurrentWebview: () => ({
    onDragDropEvent: () => Promise.resolve(() => {}),
  }),
}));

/**
 * Builds a stored (uncompressed) zip, so the reader's inflate path — and with
 * it the platform's DecompressionStream — stays out of these tests. Layout
 * matches lib/zip.test.ts: local headers, then the central directory, then the
 * EOCD, which is what the reader actually parses.
 */
function buildZip(entries: { name: string; body: string }[]): ArrayBuffer {
  const enc = new TextEncoder();
  const parts: Uint8Array[] = [];
  const central: Uint8Array[] = [];
  let offset = 0;

  for (const e of entries) {
    const name = enc.encode(e.name);
    const body = enc.encode(e.body);

    const local = new Uint8Array(30 + name.length);
    const lv = new DataView(local.buffer);
    lv.setUint32(0, 0x04034b50, true);
    lv.setUint16(4, 20, true);
    lv.setUint16(8, 0, true); // stored
    lv.setUint32(18, body.length, true);
    lv.setUint32(22, body.length, true);
    lv.setUint16(26, name.length, true);
    local.set(name, 30);

    const cd = new Uint8Array(46 + name.length);
    const cv = new DataView(cd.buffer);
    cv.setUint32(0, 0x02014b50, true);
    cv.setUint16(10, 0, true); // stored
    cv.setUint32(20, body.length, true);
    cv.setUint32(24, body.length, true);
    cv.setUint16(28, name.length, true);
    cv.setUint32(42, offset, true);
    cd.set(name, 46);

    parts.push(local, body);
    central.push(cd);
    offset += local.length + body.length;
  }

  const dirOffset = offset;
  let dirSize = 0;
  for (const c of central) dirSize += c.length;

  const eocd = new Uint8Array(22);
  const ev = new DataView(eocd.buffer);
  ev.setUint32(0, 0x06054b50, true);
  ev.setUint16(8, entries.length, true);
  ev.setUint16(10, entries.length, true);
  ev.setUint32(12, dirSize, true);
  ev.setUint32(16, dirOffset, true);

  const all = [...parts, ...central, eocd];
  let total = 0;
  for (const a of all) total += a.length;
  const out = new Uint8Array(total);
  let p = 0;
  for (const a of all) {
    out.set(a, p);
    p += a.length;
  }
  return out.buffer;
}

function zipFile(name: string, entries: { name: string; body: string }[]): File {
  return new File([buildZip(entries)], name, { type: "application/zip" });
}

/** A zapret bundle: the executable plus at least one real strategy. */
function bundleFile(name = "zapret-discord-youtube.zip"): File {
  return zipFile(name, [
    { name: "bin/winws.exe", body: "MZ not-really-an-exe" },
    { name: "general.bat", body: "@echo off\r\n" },
    { name: "general (ALT).bat", body: "@echo off\r\n" },
    { name: "service.bat", body: "@echo off\r\n" },
  ]);
}

function armEvents() {
  const noopUnlisten = () => {};
  for (const on of [
    mocks.onState,
    mocks.onTraffic,
    mocks.onLog,
    mocks.onProfilesChanged,
    mocks.onAttempts,
    mocks.onTrayConnect,
    mocks.onTrayShow,
  ]) {
    on.mockResolvedValue(noopUnlisten);
  }
  mocks.onDeepLink.mockResolvedValue(noopUnlisten);
  mocks.takeLaunchDeepLinks.mockResolvedValue([]);
}

/** Mounts the full shell and opens the bypass panel from the bottom bar. */
async function openPanel() {
  const utils = renderWithProviders(<App />);
  await screen.findAllByText("EX-TEST-01");
  fireEvent.click(screen.getByRole("button", { name: new RegExp(en.blocklist.title) }));
  const zone = await waitFor(() => {
    const el = utils.container.querySelector(".bl-drop");
    if (!el) throw new Error("drop zone not rendered");
    return el;
  });
  return { ...utils, zone };
}

describe("bypass import panel", () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem("tenebra.simpleMode", "0");
    armEvents();
    mocks.status.mockResolvedValue({ state: "idle" } as State);
    mocks.listProfiles.mockResolvedValue([profile]);
    mocks.connect.mockResolvedValue({ state: "connecting" } as State);
    mocks.disconnect.mockResolvedValue({ state: "idle" } as State);
    mocks.ping.mockResolvedValue([]);
    mocks.leakCheck.mockResolvedValue({
      connected: false,
      ip_verdict: "neutral",
      ip_message: "",
      dns: { status: "unavailable", message: "" },
    });
    mocks.importZapret.mockResolvedValue({
      dir: "/var/lib/tenebra/zapret",
      strategies: Array.from({ length: 20 }, (_, i) => `strategy-${i + 1}`),
    });
    mocks.pickZapret.mockResolvedValue({
      baseline: 1,
      targets: 3,
      best: "strategy-7",
      improved: true,
      results: null,
    });
  });

  // The half of this panel that is real: a bundle goes to the core, gets
  // installed, and its strategies are probed. Nothing below may break it.
  it("sends a dropped zapret bundle to the core and lists its strategies", async () => {
    const { container, zone } = await openPanel();

    fireEvent.drop(zone, { dataTransfer: { files: [bundleFile("zapret.zip")] } });

    await waitFor(() => expect(mocks.importZapret).toHaveBeenCalledTimes(1));
    const [bytes, name] = mocks.importZapret.mock.calls[0];
    expect(bytes).toBeInstanceOf(Uint8Array);
    expect((bytes as Uint8Array).length).toBeGreaterThan(0);
    expect(name).toBe("zapret.zip");

    // The count the user reads is the core's answer, not something the renderer
    // made up: 20 strategies came back, 20 are shown.
    expect(await screen.findByText("zapret.zip")).toBeTruthy();
    expect(
      await screen.findByText(`20 ${en.blocklist.rules}`),
    ).toBeTruthy();
    await waitFor(() =>
      expect(container.querySelector(".act-count")?.textContent).toBe("1"),
    );
  });

  it("probes for a working strategy right after the install", async () => {
    const { zone } = await openPanel();

    fireEvent.drop(zone, { dataTransfer: { files: [bundleFile()] } });

    await waitFor(() => expect(mocks.pickZapret).toHaveBeenCalledTimes(1));
  });

  // The lie this suite exists for. A domain blocklist parses fine, and the panel
  // used to answer with "<n> rules" — but there is no command that hands those
  // rules to the core, so the count was the entire effect of the import.
  it("refuses a domain blocklist instead of counting rules it never sends", async () => {
    const { container, zone } = await openPanel();
    const list = new File(
      [
        [
          "# a perfectly good blocklist",
          "0.0.0.0 ads.example",
          "||tracker.example^",
          "metrics.example",
        ].join("\n"),
      ],
      "blocklist.txt",
      { type: "text/plain" },
    );

    fireEvent.drop(zone, { dataTransfer: { files: [list] } });

    // Refused in the user's language, next to the zone they dropped onto.
    expect(await screen.findByRole("alert")).toHaveTextContent(en.blocklist.badFile);

    // Nothing left the renderer...
    expect(mocks.importZapret).not.toHaveBeenCalled();
    expect(mocks.importZapretPath).not.toHaveBeenCalled();
    // ...and nothing on screen says otherwise: no source row, no rule/strategy
    // count, no badge on the bottom bar.
    expect(screen.queryByText("blocklist.txt")).toBeNull();
    expect(container.querySelector(".bl-list")).toBeNull();
    expect(screen.queryByText(new RegExp(`\\d+\\s+${en.blocklist.rules}`))).toBeNull();
    expect(container.querySelector(".act-count")).toBeNull();
  });

  // Same lie in its most convincing shape: an archive of lists is exactly what
  // the old reader was written for, and it is the drop most likely to be
  // mistaken for a working import.
  it("refuses an archive of lists too", async () => {
    const { container, zone } = await openPanel();
    const archive = zipFile("ru-blocklist.zip", [
      { name: "hosts", body: "0.0.0.0 ads.example\n0.0.0.0 tracker.example\n" },
      { name: "domains.txt", body: "metrics.example\n" },
      { name: "README.md", body: "# lists\n" },
    ]);

    fireEvent.drop(zone, { dataTransfer: { files: [archive] } });

    expect(await screen.findByRole("alert")).toHaveTextContent(en.blocklist.badFile);
    expect(mocks.importZapret).not.toHaveBeenCalled();
    expect(container.querySelector(".bl-list")).toBeNull();
    expect(container.querySelector(".act-count")).toBeNull();
  });

  // A multi-file pick cannot be a bundle — a bundle is one archive or one
  // folder — so it must not fall through to a silent "imported" either.
  it("refuses a multi-file drop rather than importing it as rules", async () => {
    const { container, zone } = await openPanel();
    const files = [
      new File(["ads.example\n"], "hosts", { type: "text/plain" }),
      new File(["tracker.example\n"], "list.aa", { type: "text/plain" }),
    ];

    fireEvent.drop(zone, { dataTransfer: { files } });

    expect(await screen.findByRole("alert")).toHaveTextContent(en.blocklist.badFile);
    expect(mocks.importZapret).not.toHaveBeenCalled();
    expect(container.querySelector(".bl-list")).toBeNull();
  });
});
