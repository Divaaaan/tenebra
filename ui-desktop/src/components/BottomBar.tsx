import type { RoutingMode } from "../api";
import { useI18n } from "../i18n/I18nContext";

interface BottomBarProps {
  routing: RoutingMode;
  onSetRouting: (mode: RoutingMode) => void;
  killSwitch: boolean;
  onToggleKillSwitch: () => void;
  onLeakCheck: () => void;
  onSettings: () => void;
}

// The spec's protocol toggle (wireguard/openvpn) doesn't map to sing-box, where
// the transport is fixed per node. We repurpose the segmented control as the
// routing mode — the choice Tenebra actually exposes. "direct" lives in Settings;
// the bar is the quick smart↔global switch, so neither segment lights when the
// mode is direct.
const SEGMENTS: { mode: RoutingMode; labelKey: "routingSmart" | "routingGlobal" }[] =
  [
    { mode: "smart", labelKey: "routingSmart" },
    { mode: "global", labelKey: "routingGlobal" },
  ];

export function BottomBar({
  routing,
  onSetRouting,
  killSwitch,
  onToggleKillSwitch,
  onLeakCheck,
  onSettings,
}: BottomBarProps) {
  const { t } = useI18n();

  return (
    <div className="app-bottom">
      <div className="proto">
        <span className="proto-lab">{t.bottom.routing}</span>
        <div className="seg" role="group" aria-label={t.bottom.routing}>
          {SEGMENTS.map(({ mode, labelKey }) => (
            <button
              type="button"
              key={mode}
              className={routing === mode ? "on" : ""}
              aria-pressed={routing === mode}
              onClick={() => onSetRouting(mode)}
            >
              {t.settings[labelKey]}
            </button>
          ))}
        </div>
        <button
          type="button"
          className={`toggle${killSwitch ? " on" : ""}`}
          aria-pressed={killSwitch}
          title={t.bottom.killSwitchHint}
          onClick={onToggleKillSwitch}
        >
          <span className="box" aria-hidden="true">
            {killSwitch ? "▣" : "▢"}
          </span>
          {t.bottom.killSwitch}
        </button>
      </div>
      <div className="right">
        <button type="button" className="act" onClick={onLeakCheck}>
          ▶ {t.bottom.leakCheck}
        </button>
        <button type="button" className="act" onClick={onSettings}>
          ▶ {t.bottom.settings}
        </button>
      </div>
    </div>
  );
}
