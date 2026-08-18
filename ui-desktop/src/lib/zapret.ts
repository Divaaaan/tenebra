import { readZip } from "./zip";

/**
 * Recognising a zapret bundle among dropped files.
 *
 * The user is told to drop "the archive", and both things they might drop are
 * zips: a blocklist and a zapret bundle. Deciding by file name is hopeless —
 * releases are called `zapret-discord-youtube-1.10.1.zip`, `zapret.zip`, or
 * whatever the person renamed it to — so the decision is made by looking
 * inside.
 *
 * The tell is unambiguous: a bundle carries `bin/winws.exe`, the executable the
 * whole thing exists to run, plus at least one strategy `.bat`. A blocklist
 * archive has neither. Getting this wrong in either direction is user-visible:
 * a bundle read as a blocklist reports "no rules found" for a perfectly good
 * archive, and a blocklist sent to the core would be rejected as not-a-bundle.
 */
export async function looksLikeZapretBundle(file: File): Promise<boolean> {
  // Cheap gate first: only a zip can be a bundle, and reading a multi-megabyte
  // non-archive just to fail is wasted work.
  const head = new Uint8Array(await file.slice(0, 4).arrayBuffer());
  const isZip = head[0] === 0x50 && head[1] === 0x4b && (head[2] === 0x03 || head[2] === 0x05);
  if (!isZip) return false;

  try {
    const members = await readZip(await file.arrayBuffer());
    let hasWinws = false;
    let hasStrategy = false;
    for (const m of members) {
      const name = m.name.toLowerCase();
      if (name.endsWith("bin/winws.exe") || name.endsWith("winws.exe")) hasWinws = true;
      // service.bat ships with every bundle but is the installer, not a
      // strategy; requiring a real strategy keeps a stripped archive from
      // passing as usable.
      if (name.endsWith(".bat") && !name.endsWith("service.bat")) hasStrategy = true;
      if (hasWinws && hasStrategy) return true;
    }
    return false;
  } catch {
    // Unreadable as a zip: let the blocklist reader produce the error, since it
    // reports what it actually saw.
    return false;
  }
}
