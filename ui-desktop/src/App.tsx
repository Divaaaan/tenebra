import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { UnlistenFn } from "@tauri-apps/api/event";

import { Sidebar } from "./components/Sidebar";
import { HomeScreen } from "./screens/HomeScreen";
import { ProfilesScreen } from "./screens/ProfilesScreen";
import { SettingsScreen } from "./screens/SettingsScreen";
import { LogsScreen } from "./screens/LogsScreen";
import { useTenebra } from "./state/useTenebra";
import { useI18n } from "./i18n/I18nContext";
import { onTrayConnect, onTrayShow } from "./api";
import {
  getAutoconnect,
  getLastProfileId,
  setLastProfileId,
} from "./lib/settings";

export type ScreenId = "home" | "profiles" | "settings" | "logs";

export function App() {
  const tenebra = useTenebra();
  const { t } = useI18n();
  const [screen, setScreen] = useState<ScreenId>("home");

  // The profile the user is acting on. Defaults to whatever is connected, then
  // to the first available profile, so the Home screen always has a target.
  const [selectedProfileId, setSelectedProfileId] = useState<string | null>(
    null,
  );

  useEffect(() => {
    if (
      selectedProfileId &&
      tenebra.profiles.some((p) => p.id === selectedProfileId)
    ) {
      return; // current selection is still valid
    }
    const fallback = tenebra.state.profile ?? tenebra.profiles[0]?.id ?? null;
    setSelectedProfileId(fallback);
  }, [tenebra.profiles, tenebra.state.profile, selectedProfileId]);

  const selectedProfile = useMemo(
    () => tenebra.profiles.find((p) => p.id === selectedProfileId) ?? null,
    [tenebra.profiles, selectedProfileId],
  );

  // Connect the currently-selected profile with auto node selection. Shared by
  // the tray "Connect" item, which has no profile context of its own.
  const connectSelected = useCallback(() => {
    if (!selectedProfileId) {
      return;
    }
    void tenebra.connect(selectedProfileId).catch(() => {
      // A failure surfaces on the state/log channels; don't crash the view.
    });
  }, [tenebra, selectedProfileId]);

  // The tray listeners are registered once, but their action depends on the
  // live selection, so route through a ref that always holds the latest.
  const connectSelectedRef = useRef(connectSelected);
  connectSelectedRef.current = connectSelected;

  // Remember the last profile that actually connected, for autoconnect-on-launch.
  useEffect(() => {
    if (tenebra.state.state === "connected" && tenebra.state.profile) {
      setLastProfileId(tenebra.state.profile);
    }
  }, [tenebra.state.state, tenebra.state.profile]);

  // Tray → front end. "Connect" runs the normal selected-profile flow; "Show"
  // also brings us to Home so there's something useful to act on.
  useEffect(() => {
    let active = true;
    const unlisteners: UnlistenFn[] = [];
    (async () => {
      const subs = await Promise.all([
        onTrayConnect(() => connectSelectedRef.current()),
        onTrayShow(() => setScreen("home")),
      ]);
      if (!active) {
        subs.forEach((u) => u());
        return;
      }
      unlisteners.push(...subs);
    })();
    return () => {
      active = false;
      unlisteners.forEach((u) => u());
    };
  }, []);

  // Autoconnect on launch: once the snapshot is in, if the setting is on, the
  // last profile still exists, and nothing is connected yet, connect it once.
  // The guard ref makes this fire at most once per app run — no retry loops.
  const autoconnectTried = useRef(false);
  useEffect(() => {
    // Wait for the snapshot AND the profile list before deciding, so the
    // one-shot guard can't burn on a tick where profiles haven't loaded yet.
    if (
      autoconnectTried.current ||
      !tenebra.ready ||
      tenebra.profiles.length === 0
    ) {
      return;
    }
    autoconnectTried.current = true;
    if (!getAutoconnect() || tenebra.state.state !== "idle") {
      return;
    }
    const lastId = getLastProfileId();
    if (lastId && tenebra.profiles.some((p) => p.id === lastId)) {
      void tenebra.connect(lastId).catch(() => {
        // Surfaced on the state/log channels; never blocks startup.
      });
    }
  }, [tenebra.ready, tenebra.profiles, tenebra.state.state, tenebra]);

  const body = (() => {
    switch (screen) {
      case "home":
        return (
          <HomeScreen
            tenebra={tenebra}
            selectedProfile={selectedProfile}
            onSelectProfile={setSelectedProfileId}
            onGoToProfiles={() => setScreen("profiles")}
          />
        );
      case "profiles":
        return (
          <ProfilesScreen
            tenebra={tenebra}
            selectedProfileId={selectedProfileId}
            onSelectProfile={setSelectedProfileId}
          />
        );
      case "settings":
        return <SettingsScreen tenebra={tenebra} />;
      case "logs":
        return <LogsScreen tenebra={tenebra} />;
    }
  })();

  return (
    <div className="app-shell">
      <Sidebar active={screen} onNavigate={setScreen} state={tenebra.state} />
      <main className="app-main" aria-label={t.nav[screen]}>
        {body}
      </main>
    </div>
  );
}
