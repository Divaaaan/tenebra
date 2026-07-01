// Small client-side settings backed by localStorage. These are renderer-owned
// preferences (the core doesn't know or care about them): whether to reconnect
// on launch, and which profile to reconnect to. Kept here so the components that
// read and write them share one source of truth and one set of keys.

const AUTOCONNECT_KEY = "tenebra.autoconnect";
const LAST_PROFILE_KEY = "tenebra.lastProfile";
const AUTO_FASTEST_KEY = "tenebra.autoFastest";

/** Whether "connect on launch" is enabled. Off unless explicitly turned on. */
export function getAutoconnect(): boolean {
  return localStorage.getItem(AUTOCONNECT_KEY) === "1";
}

export function setAutoconnect(on: boolean): void {
  localStorage.setItem(AUTOCONNECT_KEY, on ? "1" : "0");
}

/** The last profile that connected successfully, or null if none is recorded. */
export function getLastProfileId(): string | null {
  const id = localStorage.getItem(LAST_PROFILE_KEY);
  return id && id.length > 0 ? id : null;
}

export function setLastProfileId(id: string): void {
  localStorage.setItem(LAST_PROFILE_KEY, id);
}

// The kill switch used to be a renderer-side flag here ("tenebra.killSwitch");
// it is now core-owned: the daemon persists it in its settings.json, arms it in
// the tunnel config, and reports it back through State.kill_switch.

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
