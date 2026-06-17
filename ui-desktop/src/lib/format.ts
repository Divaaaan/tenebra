import type { Language } from "../i18n/strings";

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB"] as const;

/** Human-readable byte size, e.g. 1536 → "1.5 KB". */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return "0 B";
  }
  const exp = Math.min(
    Math.floor(Math.log(bytes) / Math.log(1024)),
    BYTE_UNITS.length - 1,
  );
  const value = bytes / 1024 ** exp;
  const digits = value >= 100 || exp === 0 ? 0 : 1;
  return `${value.toFixed(digits)} ${BYTE_UNITS[exp]}`;
}

/** Byte rate suffixed with a localized "/s". */
export function formatRate(bytesPerSecond: number, perSecond: string): string {
  return `${formatBytes(bytesPerSecond)}${perSecond}`;
}

/** RFC3339 timestamp as a localized date, or em dash when absent/invalid. */
export function formatDate(iso: string | undefined, lang: Language): string {
  if (!iso) {
    return "—";
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "—";
  }
  return date.toLocaleDateString(lang === "ru" ? "ru-RU" : "en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

/** Wall-clock time for a log line, e.g. "14:03:21". */
export function formatTime(date: Date, lang: Language): string {
  return date.toLocaleTimeString(lang === "ru" ? "ru-RU" : "en-US", {
    hour12: false,
  });
}

/** A used/total byte pair as "1.2 GB / 50 GB". Total of 0 means unmetered. */
export function formatTrafficUsage(
  used: number | undefined,
  total: number | undefined,
): string | null {
  if (used === undefined && total === undefined) {
    return null;
  }
  const usedStr = formatBytes(used ?? 0);
  if (!total) {
    return usedStr;
  }
  return `${usedStr} / ${formatBytes(total)}`;
}
