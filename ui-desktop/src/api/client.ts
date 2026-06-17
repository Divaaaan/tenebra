import { invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";

import type {
  LogEvent,
  PingResult,
  Profile,
  RoutingMode,
  SplitMode,
  State,
  StateEvent,
  TrafficEvent,
} from "./types";

// Typed wrapper over the Tauri command surface. The Rust shell forwards every
// call to whichever backend is wired in (mock today, the sidecar client later),
// so this file never needs to change when the real core arrives — the command
// names and payloads are the contract from docs/control-protocol.md.
//
// Commands take snake_case argument keys because that is what the Rust handlers
// declare; Tauri matches them verbatim.

export const api = {
  status(): Promise<State> {
    return invoke<State>("status");
  },

  listProfiles(): Promise<Profile[]> {
    return invoke<{ profiles: Profile[] }>("list_profiles").then(
      (r) => r.profiles,
    );
  },

  importSubscription(url: string, name: string): Promise<Profile> {
    return invoke<{ profile: Profile }>("import_subscription", {
      url,
      name,
    }).then((r) => r.profile);
  },

  importLink(link: string, name?: string): Promise<Profile> {
    return invoke<{ profile: Profile }>("import_link", { link, name }).then(
      (r) => r.profile,
    );
  },

  removeProfile(profile: string): Promise<void> {
    return invoke<void>("remove_profile", { profile });
  },

  refreshSubscription(profile: string): Promise<Profile> {
    return invoke<{ profile: Profile }>("refresh_subscription", {
      profile,
    }).then((r) => r.profile);
  },

  connect(profile: string, node?: string): Promise<State> {
    return invoke<State>("connect", { profile, node });
  },

  disconnect(): Promise<State> {
    return invoke<State>("disconnect");
  },

  ping(profile: string): Promise<PingResult[]> {
    return invoke<{ results: PingResult[] }>("ping", { profile }).then(
      (r) => r.results,
    );
  },

  setRouting(mode: RoutingMode): Promise<State> {
    return invoke<State>("set_routing", { mode });
  },

  // Per-app split tunnelling. apps are executable file names (e.g. "chrome.exe").
  // The core normalizes them, so the returned State may reorder/lowercase them.
  setSplit(mode: SplitMode, apps: string[]): Promise<State> {
    return invoke<State>("set_split", { mode, apps });
  },

  /** Stubbed leak check; the mock returns a canned result. */
  leakCheck(): Promise<LeakCheck> {
    return invoke<LeakCheck>("leak_check");
  },

  /** Actually exit the app. Closing the window only hides it to the tray. */
  quit(): Promise<void> {
    return invoke<void>("quit_app");
  },
};

export interface LeakCheck {
  ip: string;
  country: string;
  /** True when the egress IP differs from the machine's WAN address. */
  tunneled: boolean;
}

// Event subscriptions. Each returns the Tauri unlisten handle; callers detach on
// unmount. Channel names match the protocol's event field exactly.

export function onState(
  handler: (e: StateEvent) => void,
): Promise<UnlistenFn> {
  return listen<StateEvent>("state", (event) => handler(event.payload));
}

export function onTraffic(
  handler: (e: TrafficEvent) => void,
): Promise<UnlistenFn> {
  return listen<TrafficEvent>("traffic", (event) => handler(event.payload));
}

export function onLog(handler: (e: LogEvent) => void): Promise<UnlistenFn> {
  return listen<LogEvent>("log", (event) => handler(event.payload));
}

// The core fires this (payload-less) when the stored profile set changes —
// notably after a background subscription refresh updates usage or node lists.
// The renderer responds by re-fetching the profile list so the view stays live.
export function onProfilesChanged(handler: () => void): Promise<UnlistenFn> {
  return listen("profiles", () => handler());
}

// Tray-originated events. The tray can disconnect on its own, but connecting and
// showing need the front end (it owns the selected profile and routing), so the
// Rust tray emits these for the renderer to act on. They carry no payload.

export function onTrayConnect(handler: () => void): Promise<UnlistenFn> {
  return listen("tray://connect", () => handler());
}

export function onTrayShow(handler: () => void): Promise<UnlistenFn> {
  return listen("tray://show", () => handler());
}
