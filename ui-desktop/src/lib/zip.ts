/**
 * Minimal zip reader.
 *
 * It parses the central directory rather than walking local file headers,
 * because the archives people actually download are written by streaming
 * writers: those set the data-descriptor flag and leave the sizes in the local
 * header as zero, so a local-header walk reads a length of nothing and stops at
 * the first member. GitHub's own release archives are produced this way. The
 * central directory always carries the true sizes, so it is the only route that
 * reads a real-world archive end to end.
 *
 * No dependency is pulled in for this: the app runs in a webview that already
 * ships DecompressionStream, and in a privacy tool an added dependency is
 * something to audit and update forever.
 */

const SIG_EOCD = 0x06054b50;
const SIG_EOCD64_LOCATOR = 0x07064b50;
const SIG_CENTRAL = 0x02014b50;

/** One member extracted from an archive. */
export interface ZipMember {
  name: string;
  /** Raw bytes of the member's contents. */
  data: Uint8Array;
}

/** Finds the End Of Central Directory record, scanning back from the tail. */
function findEocd(view: DataView): number {
  // The EOCD is last but carries a variable-length comment, so it has to be
  // searched for. The comment is at most 0xFFFF, hence the bounded window.
  const maxBack = Math.min(view.byteLength, 0xffff + 22);
  for (let i = view.byteLength - 22; i >= view.byteLength - maxBack; i--) {
    if (i < 0) break;
    if (view.getUint32(i, true) === SIG_EOCD) return i;
  }
  return -1;
}

/**
 * Reads every member of a zip.
 *
 * Members that fail to inflate are skipped rather than aborting the archive: one
 * unreadable entry should not discard the rest, which is the difference between
 * "this list imported" and "nothing happened".
 */
export async function readZip(buf: ArrayBuffer): Promise<ZipMember[]> {
  const view = new DataView(buf);
  const bytes = new Uint8Array(buf);

  const eocd = findEocd(view);
  if (eocd < 0) {
    throw new Error("Это не zip-архив");
  }

  let count = view.getUint16(eocd + 10, true);
  let dirOffset = view.getUint32(eocd + 16, true);

  // Zip64: the 32-bit fields saturate and the real values live in a separate
  // record. Archives above 4GB or with >65535 entries are rare here, but a
  // saturated field would otherwise send the parser to a nonsense offset.
  if (dirOffset === 0xffffffff || count === 0xffff) {
    const locator = eocd - 20;
    if (locator >= 0 && view.getUint32(locator, true) === SIG_EOCD64_LOCATOR) {
      const eocd64 = Number(view.getBigUint64(locator + 8, true));
      if (eocd64 >= 0 && eocd64 + 56 <= view.byteLength) {
        count = Number(view.getBigUint64(eocd64 + 32, true));
        dirOffset = Number(view.getBigUint64(eocd64 + 48, true));
      }
    }
  }

  const out: ZipMember[] = [];
  let p = dirOffset;

  for (let i = 0; i < count; i++) {
    if (p + 46 > view.byteLength || view.getUint32(p, true) !== SIG_CENTRAL) break;

    const method = view.getUint16(p + 10, true);
    const compressedSize = view.getUint32(p + 20, true);
    const nameLen = view.getUint16(p + 28, true);
    const extraLen = view.getUint16(p + 30, true);
    const commentLen = view.getUint16(p + 32, true);
    const localOffset = view.getUint32(p + 42, true);
    const name = new TextDecoder().decode(bytes.subarray(p + 46, p + 46 + nameLen));

    p += 46 + nameLen + extraLen + commentLen;

    if (name.endsWith("/")) continue; // directory entry

    // The local header repeats the name/extra with its OWN lengths, which are
    // not always the central ones — the data start must be computed from it.
    if (localOffset + 30 > view.byteLength) continue;
    const lNameLen = view.getUint16(localOffset + 26, true);
    const lExtraLen = view.getUint16(localOffset + 28, true);
    const dataStart = localOffset + 30 + lNameLen + lExtraLen;
    if (dataStart + compressedSize > view.byteLength) continue;

    const slice = bytes.subarray(dataStart, dataStart + compressedSize);
    try {
      if (method === 0) {
        out.push({ name, data: slice });
      } else if (method === 8) {
        out.push({ name, data: await inflateRaw(slice) });
      }
      // Other methods (bzip2, lzma) are skipped: they do not appear in
      // blocklist archives and guessing at them would risk garbage rules.
    } catch {
      // Unreadable member: skip, keep the rest.
    }
  }

  return out;
}

/** Inflates a raw deflate stream with the platform's DecompressionStream. */
async function inflateRaw(data: Uint8Array): Promise<Uint8Array> {
  const ds = new DecompressionStream("deflate-raw");
  const stream = new Blob([data as BlobPart]).stream().pipeThrough(ds);
  const buf = await new Response(stream).arrayBuffer();
  return new Uint8Array(buf);
}
