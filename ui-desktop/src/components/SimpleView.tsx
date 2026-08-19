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
  /** True once a bypass bundle is installed. */
  hasBypass: boolean;
  /** Bypass strategy currently running, or "" when it is off. */
  bypassActive: string;
  /** Import a subscription from a pasted link. */
  onSubscribe: (url: string) => Promise<void>;
  /** Import a bypass bundle from dropped files. */
  onBypassFiles: (files: File[]) => Promise<void>;
  /** Import a bypass bundle from dropped paths (folder support). */
  onBypassPaths: (paths: string[]) => Promise<void>;
  /** What the post-connect checks measured; empty before one has run. */
  serviceChecks: ServiceCheckResult[];
  /** True while those checks are in flight. */
  serviceChecking: boolean;
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
  hasBypass,
  bypassActive,
  onSubscribe,
  onBypassFiles,
  onBypassPaths,
  serviceChecks,
  serviceChecking,
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

      <div className="simple-core">
        <div className={`simple-word ${phase}`}>
          <span className="simple-ind" aria-hidden="true" />
          <span className="simple-word-text" aria-live="polite">
            {t.state[phase]}
          </span>
        </div>
        {reassurance && <p className="simple-sub">{reassurance}</p>}
        {/* The bypass is named, not just flagged: it is what makes YouTube and
            Discord work at native latency, and the user just watched it be
            chosen. A bare dot would hide the one fact worth showing. */}
        {bypassActive && (
          <p className="simple-bypass">
            <span className="simple-bypass-dot" aria-hidden="true" />
            {t.simple.bypassOn} · {bypassActive}
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

      <SimpleSetup
        hasProfile={hasProfile}
        hasBypass={hasBypass}
        onSubscribe={onSubscribe}
        onBypassFiles={onBypassFiles}
        onBypassPaths={onBypassPaths}
      />

      <button
        type="button"
        className="simple-advanced"
        onClick={exitSimpleMode}
      >
        {t.simple.advanced}
      </button>
    </div>
  );
}
