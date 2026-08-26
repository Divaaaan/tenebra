// Small client-side settings backed by localStorage. These are renderer-owned
// preferences (the core doesn't know or care about them). Kept here so the
// components that read and write them share one source of truth and one set of
// keys.

const AUTO_FASTEST_KEY = "tenebra.autoFastest";
const AUTO_INSTALL_UPDATES_KEY = "tenebra.autoInstallUpdates";
const UPDATE_CHANNEL_KEY = "tenebra.updateChannel";
const UPDATE_LAST_CHECK_KEY = "tenebra.updateLastCheck";
const UPDATE_FAILURES_KEY = "tenebra.updateFailures";

/** The release channel the updater follows. */
export type UpdateChannel = "stable" | "beta";

// The kill switch used to be a renderer-side flag here ("tenebra.killSwitch");
// it is now core-owned: the daemon persists it in its settings.json, arms it in
// the tunnel config, and reports it back through State.kill_switch.

// Autoconnect and the last-connected profile followed it: the daemon persists
// both, reconnects when it starts (service mode: at boot, before any UI), and
// reports the preference back through State.autoconnect. The legacy keys below
// survive only long enough to be migrated.
const LEGACY_AUTOCONNECT_KEY = "tenebra.autoconnect";
const LEGACY_LAST_PROFILE_KEY = "tenebra.lastProfile";

/**
 * One-shot migration of the legacy renderer-owned autoconnect flag. If the old
 * key says "on" while the core reports the preference off, `arm` is awaited
 * once to turn it on in the core; the stale keys are then removed. When arming
 * fails the keys are left in place — the next launch retries — and the error
 * propagates to the caller. The last-profile key is only dropped, never
 * transplanted: the core records its own last connect on the next success.
 */
export async function migrateLegacyAutoconnect(
  coreOn: boolean,
  arm: () => Promise<void>,
): Promise<void> {
  if (localStorage.getItem(LEGACY_AUTOCONNECT_KEY) === "1" && !coreOn) {
    await arm();
  }
  localStorage.removeItem(LEGACY_AUTOCONNECT_KEY);
  localStorage.removeItem(LEGACY_LAST_PROFILE_KEY);
}

/**
 * Whether "auto-select fastest node" is enabled. When on, a connect without an
 * explicit node asks the core to ping every candidate and pick the lowest-RTT
 * one (anti-DPI fallback still applies). Off unless explicitly turned on, so the
 * default stays the core's protocol-fallback order.
 */
export function getAutoFastest(): boolean {
  return localStorage.getItem(AUTO_FASTEST_KEY) === "1";
}

export function setAutoFastest(on: boolean): void {
  localStorage.setItem(AUTO_FASTEST_KEY, on ? "1" : "0");
}

/**
 * Whether an update found by a scheduled check installs on its own instead of
 * being offered in the banner. Off unless explicitly turned on: installing
 * restarts the app, so it has to be an opt-in.
 */
export function getAutoInstallUpdates(): boolean {
  return localStorage.getItem(AUTO_INSTALL_UPDATES_KEY) === "1";
}

export function setAutoInstallUpdates(on: boolean): void {
  localStorage.setItem(AUTO_INSTALL_UPDATES_KEY, on ? "1" : "0");
}

/**
 * The release channel the scheduled and manual checks follow. "stable"
 * (the default) offers only tested releases; "beta" also offers prereleases,
 * and always at least the newest stable, so a beta user is never held back from
 * a shipped release. Renderer-owned like the toggles above: the updater reads
 * it to resolve which signed manifest to compare against. Any value other than
 * "beta" — unset, or a stale/garbage entry — resolves to the safe stable
 * channel, so a corrupt preference can never silently opt someone into betas.
 */
export function getUpdateChannel(): UpdateChannel {
  return localStorage.getItem(UPDATE_CHANNEL_KEY) === "beta" ? "beta" : "stable";
}

export function setUpdateChannel(channel: UpdateChannel): void {
  localStorage.setItem(UPDATE_CHANNEL_KEY, channel);
}

/**
 * Wall-clock milliseconds of the last update check this install started, or
 * null when it has never run one. The schedule is decided by comparing this
 * against the current time rather than by counting timer ticks (see
 * lib/updateSchedule), so it has to outlive the process that wrote it.
 *
 * Any value that isn't a positive finite number — unset, or a stale/garbage
 * entry — resolves to null, which reads as "never checked" and simply makes a
 * check due. A stamp *ahead* of now is left alone here and handled by
 * isCheckDue, which treats it as due for the same reason.
 */
export function getUpdateLastCheck(): number | null {
  const raw = Number(localStorage.getItem(UPDATE_LAST_CHECK_KEY));
  return Number.isFinite(raw) && raw > 0 ? raw : null;
}

export function setUpdateLastCheck(at: number): void {
  localStorage.setItem(UPDATE_LAST_CHECK_KEY, String(at));
}

/**
 * How many update checks have failed back to back, counted across runs: a
 * client that cannot reach the release host is usually one that has been
 * offline since before it was last started, so a per-run counter would reset
 * exactly when it mattered. Cleared by the first check that answers.
 *
 * Anything that isn't a positive integer resolves to 0, so a corrupt entry can
 * never pin the "couldn't check" notice on screen.
 */
export function getUpdateFailures(): number {
  const raw = Number(localStorage.getItem(UPDATE_FAILURES_KEY));
  return Number.isInteger(raw) && raw > 0 ? raw : 0;
}

export function setUpdateFailures(count: number): void {
  localStorage.setItem(UPDATE_FAILURES_KEY, String(count));
}
