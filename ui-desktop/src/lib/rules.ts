// Client-side validation for the custom routing-rule inputs. It mirrors the
// core's routing.ValidDomainSuffix so the Settings screen can flag a malformed
// domain before sending it — the core validates again and is the source of truth,
// this is only for immediate feedback.
//
// A rule is a bare domain suffix: ASCII letters, digits, dots and hyphens, not
// starting or ending with a dot or hyphen, and carrying at least one letter.
// Anything with a scheme, slash, port, whitespace or '@' is rejected — none of
// those is a domain. Casing is tolerated (the core lowercases), so the check runs
// on the lowercased, trimmed form.

/**
 * Whether `suffix` is a domain suffix the core will accept as a routing rule.
 * Mirrors routing.ValidDomainSuffix; a pragmatic check, not a full RFC validator.
 */
export function isValidDomainSuffix(suffix: string): boolean {
  const s = suffix.trim().toLowerCase();
  if (s === "") {
    return false;
  }
  if (
    s.startsWith(".") ||
    s.startsWith("-") ||
    s.endsWith(".") ||
    s.endsWith("-")
  ) {
    return false;
  }
  if (!/^[a-z0-9.-]+$/.test(s)) {
    return false;
  }
  // At least one letter, so a bare number (or an IP-like string) is not a rule.
  return /[a-z]/.test(s);
}
