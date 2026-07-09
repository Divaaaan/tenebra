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

/** A byte/sec rate as a bare megabit-per-second number, e.g. 11_775_000 → "94.2".
 *  The unit ("Mbps") is rendered separately by the stat cell. */
export function formatMbps(bytesPerSecond: number): string {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) {
    return "0.0";
  }
  return ((bytesPerSecond * 8) / 1_000_000).toFixed(1);
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

/** Localized labels for the relative-expiry phrasing. */
export interface ExpiryLabels {
  /** Template with "{days}", e.g. "in {days} days". */
  in: string;
  today: string;
  tomorrow: string;
  expired: string;
}

/**
 * Relative expiry as a short phrase: "today", "tomorrow", "in N days", or
 * "expired". Returns the absolute localized date instead once the horizon is far
 * enough out (over a month) that a day count stops being useful. Returns null
 * for an absent or invalid timestamp so the caller can hide the field.
 */
export function formatExpiry(
  iso: string | undefined,
  lang: Language,
  labels: ExpiryLabels,
): string | null {
  if (!iso) {
    return null;
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return null;
  }
  // Day granularity from the start of today, so "in 1 day" doesn't flip on the
  // hour. Compare calendar days, not raw 24h spans.
  const startOfDay = (d: Date) =>
    new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round(
    (startOfDay(date) - startOfDay(new Date())) / 86_400_000,
  );

  if (days < 0) {
    return labels.expired;
  }
  if (days === 0) {
    return labels.today;
  }
  if (days === 1) {
    return labels.tomorrow;
  }
  if (days > 31) {
    return formatDate(iso, lang);
  }
  return labels.in.replace("{days}", String(days));
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
