// Small client-side settings backed by localStorage. These are renderer-owned
// preferences (the core doesn't know or care about them): whether to reconnect
// on launch, and which profile to reconnect to. Kept here so the components that
// read and write them share one source of truth and one set of keys.

const AUTOCONNECT_KEY = "tenebra.autoconnect";
const LAST_PROFILE_KEY = "tenebra.lastProfile";

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
