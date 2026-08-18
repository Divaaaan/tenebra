import { readZip } from "./zip";

/**
 * Reading user-supplied blocklists.
 *
 * Two shapes have to work without the user thinking about it: the archive
 * exactly as downloaded (release pages hand you a .zip, and asking someone to
 * unpack it first is the friction that ends with the feature unused), and the
 * unpacked folder dropped in whole. Hence: no extension filtering inside the
 * archive, and multiple files accepted in one drop.
 *
 * Nothing is selected by name. A release archive names its lists anything at
 * all — `hosts`, `list.aa`, `domains-2026-08.dat` — so membership is decided by
 * what parses: a member that yields rules is a list, a member that yields none
 * is a readme or a licence and is skipped. Judging by extension is what made an
 * archive full of perfectly good lists import zero of them.
 */

/** One parsed blocklist. */
export interface ParsedBlocklist {
  /** Domain rules, lowercased and de-duplicated. */
  rules: string[];
  /** Files that actually contributed rules, for reporting. */
  sources: string[];
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
 * import zero rules from a perfectly good list.
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

/**
 * Rejects binary members before they are treated as text.
 *
 * An archive carries icons and compiled data next to the lists; decoding those
 * as UTF-8 produces replacement characters that would never parse into rules
 * anyway, but scanning megabytes of them is wasted work. A NUL byte in the head
 * is the cheap, reliable tell.
 */
function looksBinary(data: Uint8Array): boolean {
  const head = data.subarray(0, Math.min(data.length, 512));
  return head.includes(0);
}

/** Decodes bytes as UTF-8, tolerating malformed input. */
function decode(data: Uint8Array): string {
  return new TextDecoder("utf-8", { fatal: false }).decode(data);
}

/**
 * Reads one file into rules: an archive, or a plain list.
 *
 * Throws with a message meant for the user rather than a stack: this runs on a
 * file they just chose, so the only useful thing to say is what is wrong with it.
 */
export async function readBlocklistFile(file: File): Promise<ParsedBlocklist> {
  const rules = new Set<string>();
  const sources: string[] = [];

  if (file.name.toLowerCase().endsWith(".zip")) {
    const members = await readZip(await file.arrayBuffer());
    if (members.length === 0) {
      throw new Error("Архив пуст или не читается");
    }
    for (const m of members) {
      if (looksBinary(m.data)) continue;
      const found = parseBlocklistText(decode(m.data));
      if (found.length === 0) continue; // readme, licence, changelog
      found.forEach((r) => rules.add(r));
      sources.push(m.name);
    }
    if (rules.size === 0) {
      throw new Error(`В архиве ${members.length} файлов, но правил не нашлось`);
    }
    return { rules: [...rules], sources };
  }

  const found = parseBlocklistText(await file.text());
  if (found.length === 0) {
    throw new Error("В файле не нашлось ни одного правила");
  }
  found.forEach((r) => rules.add(r));
  return { rules: [...rules], sources: [file.name] };
}

/**
 * Reads a whole drop: one archive, or every file of an unpacked folder at once.
 *
 * Files that yield nothing are skipped silently — dropping an unpacked release
 * means dropping its readme and licence too, and complaining about them would
 * turn a successful import into an error message. Only a drop where NOTHING
 * parsed is an error, because then the user genuinely dropped the wrong thing.
 */
export async function readBlocklistFiles(files: File[]): Promise<ParsedBlocklist> {
  if (files.length === 0) {
    throw new Error("Нечего читать");
  }
  if (files.length === 1) {
    return readBlocklistFile(files[0]);
  }

  const rules = new Set<string>();
  const sources: string[] = [];
  let failures = 0;

  for (const f of files) {
    try {
      const parsed = await readBlocklistFile(f);
      parsed.rules.forEach((r) => rules.add(r));
      sources.push(...(parsed.sources.length > 0 ? parsed.sources : [f.name]));
    } catch {
      failures += 1;
    }
  }

  if (rules.size === 0) {
    throw new Error(`Ни в одном из ${files.length} файлов не нашлось правил`);
  }
  void failures;
  return { rules: [...rules], sources };
}

/** Back-compat alias for the single-file entry point. */
export const readBlocklist = readBlocklistFile;
