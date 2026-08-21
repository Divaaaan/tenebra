import { describe, expect, it } from "vitest";

import { readZip } from "./zip";

/**
 * Builds a zip by hand so the tests can control the exact layout — in
 * particular the streaming layout that broke the previous reader.
 *
 * `streaming: true` writes what a streaming writer produces: the local header
 * carries zeroes for the sizes and the data-descriptor flag, with the true
 * sizes only in the central directory. GitHub's archives are written this way.
 */
function buildZip(
  entries: { name: string; body: string }[],
  opts: { streaming?: boolean } = {},
): ArrayBuffer {
  const enc = new TextEncoder();
  const parts: Uint8Array[] = [];
  const central: Uint8Array[] = [];
  let offset = 0;

  for (const e of entries) {
    const name = enc.encode(e.name);
    const body = enc.encode(e.body);
    const crc = 0; // not validated by the reader

    const local = new Uint8Array(30 + name.length);
    const lv = new DataView(local.buffer);
    lv.setUint32(0, 0x04034b50, true);
    lv.setUint16(4, 20, true);
    lv.setUint16(6, opts.streaming ? 0x08 : 0, true); // data-descriptor flag
    lv.setUint16(8, 0, true); // stored
    lv.setUint32(14, crc, true);
    lv.setUint32(18, opts.streaming ? 0 : body.length, true);
    lv.setUint32(22, opts.streaming ? 0 : body.length, true);
    lv.setUint16(26, name.length, true);
    lv.setUint16(28, 0, true);
    local.set(name, 30);

    const cd = new Uint8Array(46 + name.length);
    const cv = new DataView(cd.buffer);
    cv.setUint32(0, 0x02014b50, true);
    cv.setUint16(10, 0, true); // stored
    cv.setUint32(16, crc, true);
    cv.setUint32(20, body.length, true); // true size, always present here
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

describe("readZip", () => {
  it("reads every member", async () => {
    const buf = buildZip([
      { name: "hosts", body: "0.0.0.0 ads.example\n" },
      { name: "README.md", body: "# a readme\n" },
    ]);

    const members = await readZip(buf);
    expect(members.map((m) => m.name)).toEqual(["hosts", "README.md"]);
    expect(new TextDecoder().decode(members[0].data)).toContain("ads.example");
  });

  // The regression this rewrite exists for: a streaming writer leaves the local
  // header sizes at zero, so a local-header walk reads a length of nothing and
  // stops at the first member. Release archives are written exactly this way.
  it("reads an archive whose sizes live only in the central directory", async () => {
    const buf = buildZip(
      [
        { name: "list-a.txt", body: "ads.example\n" },
        { name: "list-b.txt", body: "trackers.example\n" },
      ],
      { streaming: true },
    );

    const members = await readZip(buf);
    expect(members).toHaveLength(2);
    expect(new TextDecoder().decode(members[1].data)).toContain("trackers.example");
  });

  it("skips directory entries", async () => {
    const buf = buildZip([
      { name: "lists/", body: "" },
      { name: "lists/hosts", body: "ads.example\n" },
    ]);

    const members = await readZip(buf);
    expect(members.map((m) => m.name)).toEqual(["lists/hosts"]);
  });

  it("rejects a file that is not a zip at all", async () => {
    const notZip = new TextEncoder().encode("just some text, definitely not a zip");
    await expect(readZip(notZip.buffer as ArrayBuffer)).rejects.toThrow();
  });
});
