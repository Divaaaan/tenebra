import type { ReactNode } from "react";

import type {
  ConnectionState,
  Node,
  Profile,
  ServiceCheck as ServiceCheckResult,
} from "../api";
import { useI18n } from "../i18n/I18nContext";
import { ServiceChecks } from "./ServiceChecks";
import { SimpleSetup } from "./SimpleSetup";

interface SimpleViewProps {
  phase: ConnectionState;
  /** True while a primary action is mid-flight; disables the button. */
  busy: boolean;
  /** Connect / disconnect / abort, decided by phase — the same handler the shell uses. */
  onPrimary: () => void;
  /** Display name of the active (connected) or target node; "" when none. */
  nodeName: string;
  profiles: Profile[];
  selectedProfileId: string | null;
  onSelectProfile: (id: string) => void;
  /** Nodes of the selected profile. */
  nodes: Node[];
  /** Pinned node id, or "" for automatic (let the core pick the fastest). */
  selectedNodeId: string;
  onSelectNode: (id: string) => void;
  onSelectAuto: () => void;
  /** True when the core reports a bundle on disk (or a filter already running). */
  bypassInstalled: boolean;
  /** True when the core reports the packet filter as carrying traffic. */
  bypassOn: boolean;
  /** The strategy the core is running; "" when it does not name one. */
  bypassStrategy: string;
  /**
   * True while the core cannot be reached at all. Nothing else on this screen is
   * backed by anything then, and saying so beats a calm "disconnected" over an
   * app that has no core behind it.
   */
  coreUnreachable: boolean;
  /** Import a subscription from a pasted link. */
  onSubscribe: (url: string) => Promise<void>;
  /** What the post-connect checks measured; empty before one has run. */
  serviceChecks: ServiceCheckResult[];
  /** True while those checks are in flight. */
  serviceChecking: boolean;
  /** Open the report-a-problem flow. */
  onReportProblem: () => void;
  /**
   * The app's own offer to report, when the checks below have twice said video
   * isn't getting through. Built by the shell so both views raise the same one.
   */
  reportNudge?: ReactNode;
}

/**
 * The stripped-down connection screen for people who want one button, not a
 * control panel. A single large connect / disconnect control, the current status
 * in plain words, and a minimal server picker — everything advanced (routing,
 * kill switch, diagnostics, the node table, logs) is left to the full shell.
 * Reads the same connection state and calls the same actions as the shell, so the
 * two views never disagree.
 */
export function SimpleView({
  phase,
  busy,
  onPrimary,
  nodeName,
  profiles,
  selectedProfileId,
  onSelectProfile,
  nodes,
  selectedNodeId,
  onSelectNode,
  onSelectAuto,
  bypassInstalled,
  bypassOn,
  bypassStrategy,
  coreUnreachable,
  onSubscribe,
  serviceChecks,
  serviceChecking,
  onReportProblem,
  reportNudge = null,
}: SimpleViewProps) {
  const { t } = useI18n();

  // The only way out of simple mode from here: it hides Settings (and its own
  // toggle), so a stranded user would otherwise be stuck. Flip the shared
  // `tenebra.simpleMode` flag off and nudge the app shell exactly the way the
  // Settings toggle does — a `storage` event — plus the same-document custom
  // event App also listens for. App re-reads the flag and restores the full
  // shell. The key and "false" encoding are the contract with App/Settings;
  // keep them verbatim.
  function exitSimpleMode() {
    localStorage.setItem("tenebra.simpleMode", "false");
    window.dispatchEvent(
      new StorageEvent("storage", {
        key: "tenebra.simpleMode",
        newValue: "false",
      }),
    );
    window.dispatchEvent(new CustomEvent("tenebra:simple-mode"));
  }

  const connected = phase === "connected";
  const pending = phase === "connecting";
  const hasProfile = profiles.length > 0;

  const buttonLabel = connected
    ? t.home.disconnect
    : pending
      ? t.conn.abort
      : t.home.connect;

  // Calm, plain-language status line. Connecting / reconnecting say nothing extra —
  // the status word already carries it.
  const reassurance =
    phase === "connected"
      ? nodeName
        ? `${t.simple.statusOn} · ${nodeName}`
        : t.simple.statusOn
      : phase === "idle" || phase === "error"
        ? t.simple.statusOff
        : "";

  return (
    <div className="simple">
      <div className="simple-brand" aria-hidden="true">
        <span className="bracket">[</span>
        <span className="mark">Tenebra</span>
        <span className="bracket">]</span>
      </div>

      {/* A core that never answered leaves this screen drawn over nothing: no
          profiles, no real status, every button doomed. The calm one-word status
          is exactly what must not be shown on its own here. */}
      {coreUnreachable && (
        <p className="simple-core-down" role="alert">
          ⚠ {t.daemon.unreachable}
        </p>
      )}

      <div className="simple-core">
        <div className={`simple-word ${phase}`}>
          <span className="simple-ind" aria-hidden="true" />
          <span className="simple-word-text" aria-live="polite">
            {t.state[phase]}
          </span>
        </div>
        {reassurance && <p className="simple-sub">{reassurance}</p>}
        {/* The bypass, read off the core's own snapshot. It is named rather than
            just flagged: which strategy is up is the one fact worth showing —
            they behave differently and the app chose this one by measurement.
            Nothing is drawn before a bundle exists, because until the first
            connect installs one there is genuinely nothing to report. */}
        {bypassInstalled && (
          <p className={`simple-bypass${bypassOn ? "" : " is-off"}`}>
            <span className="simple-bypass-dot" aria-hidden="true" />
            {bypassOn
              ? bypassStrategy
                ? `${t.simple.bypassOn} · ${bypassStrategy}`
                : t.simple.bypassOn
              : t.simple.bypassOff}
          </p>
        )}

        <button
          type="button"
          className={`simple-btn${connected ? " on" : ""}${pending ? " pending" : ""}`}
          onClick={onPrimary}
          disabled={busy || (!connected && !pending && !hasProfile)}
        >
          {buttonLabel}
        </button>

        {/* What "connected" actually bought: video, voice and game latency,
            measured. The status word alone leaves a user watching a spinning
            YouTube with no way to say what is wrong. */}
        <ServiceChecks checks={serviceChecks} checking={serviceChecking} />

        {/* Directly under the check that raised it, where the ✕ it is talking
            about is on screen. */}
        {reportNudge}
      </div>

      <div className="simple-pick">
        {hasProfile ? (
          <>
            {profiles.length > 1 && (
              <label className="simple-field">
                <span className="simple-field-lab">{t.home.activeProfile}</span>
                <select
                  className="simple-select"
                  value={selectedProfileId ?? ""}
                  onChange={(e) => onSelectProfile(e.target.value)}
                >
                  {profiles.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </label>
            )}

            <label className="simple-field">
              <span className="simple-field-lab">{t.simple.server}</span>
              <select
                className="simple-select"
                value={selectedNodeId}
                onChange={(e) =>
                  e.target.value
                    ? onSelectNode(e.target.value)
                    : onSelectAuto()
                }
                disabled={nodes.length === 0}
              >
                <option value="">{t.simple.auto}</option>
                {nodes.map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.name}
                  </option>
                ))}
              </select>
            </label>
          </>
        ) : null}
      </div>

      <SimpleSetup hasProfile={hasProfile} onSubscribe={onSubscribe} />

      {/* This screen has no Settings and no log console, so before this there
          was nothing here to complain with at all — a user whose video stopped
          loading could only close the app. */}
      <div className="simple-foot">
        <button
          type="button"
          className="simple-advanced"
          onClick={onReportProblem}
        >
          {t.report.action}
        </button>
        <button
          type="button"
          className="simple-advanced"
          onClick={exitSimpleMode}
        >
          {t.simple.advanced}
        </button>
      </div>
    </div>
  );
}
