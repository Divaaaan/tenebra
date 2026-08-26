import type { RoutingMode } from "../api";
import { useI18n } from "../i18n/I18nContext";

interface BottomBarProps {
  routing: RoutingMode;
  onSetRouting: (mode: RoutingMode) => void;
  killSwitch: boolean;
  onToggleKillSwitch: () => void;
  onLeakCheck: () => void;
  onSettings: () => void;
  /** Open the report-a-problem flow. */
  onReportProblem: () => void;
  /** True when the core reports a bundle on disk (or a filter already running). */
  bypassInstalled: boolean;
  /** True when the core reports the packet filter as carrying traffic. */
  bypassOn: boolean;
  /** The strategy the core is running; "" when it does not name one. */
  bypassStrategy: string;
}

// The spec's protocol toggle (wireguard/openvpn) doesn't map to sing-box, where
// the transport is fixed per node. We repurpose the segmented control as the
// routing mode — the choice Tenebra actually exposes. "direct" lives in Settings;
// the bar is the quick smart↔global switch, so neither segment lights when the
// mode is direct.
const SEGMENTS: {
  mode: RoutingMode;
  labelKey: "routingSmart" | "routingGlobal";
  hintKey: "routingSmartHint" | "routingGlobalHint";
}[] = [
  { mode: "smart", labelKey: "routingSmart", hintKey: "routingSmartHint" },
  { mode: "global", labelKey: "routingGlobal", hintKey: "routingGlobalHint" },
];

export function BottomBar({
  routing,
  onSetRouting,
  killSwitch,
  onToggleKillSwitch,
  onLeakCheck,
  onSettings,
  onReportProblem,
  bypassInstalled,
  bypassOn,
  bypassStrategy,
}: BottomBarProps) {
  const { t } = useI18n();

  return (
    <div className="app-bottom">
      <div className="proto">
        <span className="proto-lab">{t.bottom.routing}</span>
        <div className="seg" role="group" aria-label={t.bottom.routing}>
          {SEGMENTS.map(({ mode, labelKey, hintKey }) => (
            <button
              type="button"
              key={mode}
              className={routing === mode ? "on" : ""}
              aria-pressed={routing === mode}
              title={t.settings[hintKey]}
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
        {/* The bypass, read off the core's snapshot — a readout, not a control:
            it used to be a button opening an import panel with a count of
            imported files beside it, which said nothing about whether the filter
            was actually up. Switching it on and off lives in Settings; what
            belongs here is the answer to "is it running right now". Silent until
            a bundle exists, since before the first connect there is nothing to
            report. */}
        {bypassInstalled && (
          <span
            className={`bypass-stat${bypassOn ? " is-on" : ""}`}
            title={t.settings.bypassStatusHint}
          >
            <span className="box" aria-hidden="true">
              {bypassOn ? "▣" : "▢"}
            </span>
            {t.settings.bypassStatus}
            {bypassOn && bypassStrategy ? ` · ${bypassStrategy}` : ""}
          </span>
        )}
        {/* Reporting sits out here with the other quick actions rather than
            behind Settings: the app had no way at all to say "this is broken"
            unless it had crashed *and* the user had opted into crash reports,
            which is not how most things break. */}
        <button type="button" className="act" onClick={onReportProblem}>
          ▶ {t.bottom.report}
        </button>
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
