// Small client-side settings backed by localStorage. These are renderer-owned
// preferences (the core doesn't know or care about them). Kept here so the
// components that read and write them share one source of truth and one set of
// keys.

const AUTO_FASTEST_KEY = "tenebra.autoFastest";
const AUTO_INSTALL_UPDATES_KEY = "tenebra.autoInstallUpdates";

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
 * Whether an update found by the launch check installs on its own instead of
 * being offered in the banner. Off unless explicitly turned on: installing
 * restarts the app, so it has to be an opt-in.
 */
export function getAutoInstallUpdates(): boolean {
  return localStorage.getItem(AUTO_INSTALL_UPDATES_KEY) === "1";
}

export function setAutoInstallUpdates(on: boolean): void {
  localStorage.setItem(AUTO_INSTALL_UPDATES_KEY, on ? "1" : "0");
}
