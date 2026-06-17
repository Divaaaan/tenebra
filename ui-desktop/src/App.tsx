import { useEffect, useMemo, useState } from "react";

import { Sidebar } from "./components/Sidebar";
import { HomeScreen } from "./screens/HomeScreen";
import { ProfilesScreen } from "./screens/ProfilesScreen";
import { SettingsScreen } from "./screens/SettingsScreen";
import { LogsScreen } from "./screens/LogsScreen";
import { useTenebra } from "./state/useTenebra";
import { useI18n } from "./i18n/I18nContext";

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
