/**
 * Reading a user-supplied blocklist.
 *
 * Accepts a plain list (.txt/.lst/.srs) or a .zip holding one or more of them,
 * because that is how blocklists are actually distributed — a release page hands
 * you an archive, and asking the user to unpack it first is the kind of friction
 * that ends with the feature unused.
 *
 * The zip path is implemented against the format directly rather than pulling in
 * a library: the app ships in a webview that already has DecompressionStream, and
 * a dependency added for one archive read is a dependency to audit and update
 * forever in a privacy tool.
 */

/** One parsed blocklist. */
export interface ParsedBlocklist {
  /** Domain rules, lowercased and de-duplicated. */
  rules: string[];
}

/** A rule line that is a comment or blank contributes nothing. */
function isSkippable(line: string): boolean {
  const t = line.trim();
  return t === "" || t.startsWith("#") || t.startsWith("!") || t.startsWith(";");
}

/**
 * Extracts a domain from one list line.
 *
 * Handles the three shapes these files come in interchangeably: a bare domain,
 * a hosts-file line (`0.0.0.0 ads.example`), and an AdBlock-syntax line
 * (`||ads.example^`). A reader that understood only one of them would silently
 * import zero rules from a perfectly good list — the failure mode this parsing
 * exists to avoid.
 */
export function parseRuleLine(line: string): string | null {
  if (isSkippable(line)) return null;
  let s = line.trim().toLowerCase();

  // AdBlock syntax: ||domain^ or ||domain^$modifiers
  if (s.startsWith("||")) {
    s = s.slice(2);
    const cut = s.search(/[\^$/]/);
    if (cut >= 0) s = s.slice(0, cut);
  } else {
    // Hosts syntax: an address followed by one or more names. Take the name.
    const parts = s.split(/\s+/);
    if (parts.length > 1 && /^[\d.:]+$/.test(parts[0])) {
      s = parts[1];
    } else {
      s = parts[0];
    }
  }

  s = s.replace(/^\*\./, "").replace(/^\./, "").replace(/\.$/, "");
  // A domain must have a dot and only host-legal characters; anything else is a
  // header, a regex rule or junk, and importing it would poison the rule set.
  if (!s || !s.includes(".") || !/^[a-z0-9.-]+$/.test(s)) return null;
  if (s.startsWith("-") || s.endsWith("-")) return null;
  return s;
}

/** Parses a whole list body into unique domain rules. */
export function parseBlocklistText(text: string): string[] {
  const seen = new Set<string>();
  for (const line of text.split(/\r?\n/)) {
    const rule = parseRuleLine(line);
    if (rule) seen.add(rule);
  }
  return [...seen];
}

// ── zip ──────────────────────────────────────────────────────────────────────

const SIG_LOCAL = 0x04034b50;

/**
 * Pulls every text member out of a zip.
 *
 * It walks local file headers from the start rather than parsing the central
 * directory: the members are laid out in order, the header carries everything
 * needed (method, sizes, name), and skipping the directory keeps this to one
 * pass without weakening anything — a malformed archive fails the same way
 * either route.
 *
 * Only stored (0) and deflate (8) are handled; those are what real archives use.
 * Anything else is skipped rather than throwing, so one exotic member cannot
 * take down an import whose other files are fine.
 */
async function readZipMembers(buf: ArrayBuffer): Promise<string[]> {
  const view = new DataView(buf);
  const bytes = new Uint8Array(buf);
  const out: string[] = [];
  let off = 0;

  while (off + 30 <= view.byteLength) {
    if (view.getUint32(off, true) !== SIG_LOCAL) break;

    const method = view.getUint16(off + 8, true);
    const flags = view.getUint16(off + 6, true);
    let compressed = view.getUint32(off + 18, true);
    const nameLen = view.getUint16(off + 26, true);
    const extraLen = view.getUint16(off + 28, true);
    const nameStart = off + 30;
    const dataStart = nameStart + nameLen + extraLen;

    const name = new TextDecoder().decode(bytes.subarray(nameStart, nameStart + nameLen));

    // Bit 3 means the sizes live in a trailing data descriptor, not the header.
    // Streaming that correctly needs the central directory; rather than guess a
    // length and read garbage, stop and report what was recovered so far.
    if (flags & 0x08) break;
    if (dataStart + compressed > view.byteLength) break;

    const isDir = name.endsWith("/");
    const looksTextual = /\.(txt|lst|srs|list|dat|conf)$/i.test(name) || !name.includes(".");

    if (!isDir && looksTextual && (method === 0 || method === 8)) {
      const slice = bytes.subarray(dataStart, dataStart + compressed);
      try {
        const text =
          method === 0
            ? new TextDecoder().decode(slice)
            : await inflateRaw(slice);
        out.push(text);
      } catch {
        // A member that will not inflate is skipped; the rest of the archive is
        // still worth importing.
      }
    }

    if (compressed === 0 && method === 0) compressed = 0;
    off = dataStart + compressed;
  }

  return out;
}

/** Inflates a raw deflate member using the platform's DecompressionStream. */
async function inflateRaw(data: Uint8Array): Promise<string> {
  const ds = new DecompressionStream("deflate-raw");
  const stream = new Blob([data as BlobPart]).stream().pipeThrough(ds);
  return await new Response(stream).text();
}

/**
 * Reads a dropped file into rules.
 *
 * Throws with a message meant for the user rather than a stack: this runs on a
 * file they just chose, so the only useful thing to say is what is wrong with
 * that file.
 */
export async function readBlocklist(file: File): Promise<ParsedBlocklist> {
  const name = file.name.toLowerCase();

  if (name.endsWith(".zip")) {
    const members = await readZipMembers(await file.arrayBuffer());
    if (members.length === 0) {
      throw new Error("В архиве нет подходящих списков");
    }
    const seen = new Set<string>();
    for (const body of members) {
      for (const rule of parseBlocklistText(body)) seen.add(rule);
    }
    if (seen.size === 0) {
      throw new Error("В архиве не нашлось ни одного правила");
    }
    return { rules: [...seen] };
  }

  const rules = parseBlocklistText(await file.text());
  if (rules.length === 0) {
    throw new Error("В файле не нашлось ни одного правила");
  }
  return { rules };
}
