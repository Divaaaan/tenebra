import type { ReactNode } from "react";

import type { ConnectionState, Node, Profile, ServiceCheck } from "../api";
import { useI18n } from "../i18n/I18nContext";
import { formatExpiry, formatTrafficUsage } from "../lib/format";
import { ServiceChecks } from "./ServiceChecks";
import { SimpleSetup } from "./SimpleSetup";

interface SimpleViewProps {
  phase: ConnectionState;
  busy: boolean;
  checkingServers?: boolean;
  ready?: boolean;
  protectionBlocked?: boolean;
  onPrimary: () => void;
  nodeName: string;
  profiles: Profile[];
  selectedProfileId: string | null;
  onSelectProfile: (id: string) => void;
  nodes: Node[];
  selectedNodeId: string;
  onSelectNode: (id: string) => void;
  onSelectAuto: () => void;
  bypassInstalled: boolean;
  bypassOn: boolean;
  bypassStrategy: string;
  coreUnreachable: boolean;
  onSubscribe: (url: string) => Promise<void>;
  serviceChecks: ServiceCheck[];
  serviceChecking: boolean;
  onReportProblem: () => void;
  onManageProfiles?: () => void;
  onSettings?: () => void;
  reportNudge?: ReactNode;
}

/** A daily connection screen using the shell's real state and shared actions. */
export function SimpleView({
  phase, busy, checkingServers = false, ready = true, protectionBlocked = false, onPrimary, nodeName,
  profiles, selectedProfileId, onSelectProfile, nodes, selectedNodeId,
  onSelectNode, onSelectAuto, bypassInstalled, bypassOn, bypassStrategy,
  coreUnreachable, onSubscribe, serviceChecks, serviceChecking,
  onReportProblem, onManageProfiles, onSettings, reportNudge = null,
}: SimpleViewProps) {
  const { t, lang } = useI18n();
  const connected = phase === "connected";
  const pending = phase === "connecting" || phase === "health_reconnecting";
  const hasProfile = profiles.length > 0;
  const showConnection = hasProfile || connected || pending;
  const unavailable = !ready || coreUnreachable;
  const selectionLocked = unavailable || busy || pending || checkingServers;
  const selectedProfile = profiles.find((profile) => profile.id === selectedProfileId);
  const usage = selectedProfile
    ? formatTrafficUsage(selectedProfile.trafficUsed, selectedProfile.trafficTotal)
    : null;
  const expiry = selectedProfile
    ? formatExpiry(selectedProfile.expiresAt, lang, {
      in: t.profiles.expiresIn, today: t.profiles.expiresToday,
      tomorrow: t.profiles.expiresTomorrow, expired: t.profiles.expired,
    }) : null;

  function exitSimpleMode() {
    localStorage.setItem("tenebra.simpleMode", "false");
    window.dispatchEvent(new StorageEvent("storage", {
      key: "tenebra.simpleMode", newValue: "false",
    }));
    window.dispatchEvent(new CustomEvent("tenebra:simple-mode"));
  }

  const statusLabel = coreUnreachable ? t.simple.serviceUnavailable
    : !ready ? t.simple.serviceStarting
      : protectionBlocked && !pending && !checkingServers ? t.simple.trafficBlocked
        : checkingServers ? t.simple.checkingServers
        : busy && !connected && !pending ? t.simple.preparing : t.state[phase];
  const buttonLabel = connected ? t.home.disconnect
    : pending ? t.conn.abort
      : checkingServers ? t.simple.checkingServers
        : busy ? t.simple.preparing : t.home.connect;
  const reassurance = connected && !unavailable
    ? nodeName ? `${t.simple.statusOn} · ${nodeName}` : t.simple.statusOn
    : coreUnreachable ? t.simple.serviceHelp
      : protectionBlocked && ready ? t.simple.blockedHint
        : phase === "idle" && !busy && ready ? t.simple.statusOff : "";

  return (
    <div className="simple">
      <header className="simple-header">
        <div className="simple-brand" aria-label="Tenebra">
          <span className="bracket" aria-hidden="true">[</span>
          <span className="mark">Tenebra</span>
          <span className="bracket" aria-hidden="true">]</span>
          <span className="simple-mode">{t.simple.mode}</span>
        </div>
        <button type="button" className="simple-link" onClick={exitSimpleMode}>{t.simple.advanced}</button>
      </header>

      <main className="simple-content">
        {coreUnreachable && <p className="simple-core-down" role="alert">{t.daemon.unreachable}</p>}

        {showConnection ? (
          <div className="simple-layout">
            <section className="simple-core" aria-label={t.simple.mode}>
              <h1 className={`simple-word ${unavailable ? "unavailable" : phase}`} aria-live="polite">
                {statusLabel}
              </h1>
              {reassurance && <p className="simple-sub">{reassurance}</p>}
              <button type="button"
                className={`simple-btn${connected && !unavailable ? " on" : ""}${pending || checkingServers ? " pending" : ""}`}
                onClick={onPrimary}
                disabled={busy || checkingServers || (!connected && !pending && (unavailable || nodes.length === 0 || !selectedProfile))}
              >
                <span className="simple-power" aria-hidden="true">
                  <svg width="38" height="38" viewBox="0 0 32 32" fill="none">
                    <path d="M16 3v12M8 7.5a12 12 0 1 0 16 0" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
                  </svg>
                </span>
                <span className="simple-button-label">{buttonLabel}</span>
              </button>
              {!unavailable && (serviceChecking || serviceChecks.length > 0) && <section className="simple-checks" aria-label={t.simple.checksTitle}>
                <h2>{t.simple.checksTitle}</h2>
                <ServiceChecks checks={serviceChecks} checking={serviceChecking} />
              </section>}
              {reportNudge}
            </section>

            <section className="simple-pick" aria-label={t.simple.subscription}>
              <div className="simple-section-head">
                <h2>{t.simple.subscription}</h2>
                {onManageProfiles && <button type="button" className="simple-link" onClick={onManageProfiles}>{t.simple.manage}</button>}
              </div>
              {profiles.length > 1 ? (
                <label className="simple-field">
                  <span className="simple-field-lab">{t.simple.subscription}</span>
                  <select className="simple-select" value={selectedProfileId ?? ""}
                    disabled={selectionLocked} onChange={(event) => onSelectProfile(event.target.value)}>
                    {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
                  </select>
                </label>
              ) : <p className="simple-profile-name">{selectedProfile?.name ?? profiles[0]?.name}</p>}
              {(usage || expiry) && <div className="simple-subscription-meta">
                {usage && <span>{usage}</span>}{expiry && <span>{expiry}</span>}
              </div>}

              <label className="simple-field simple-server-field">
                <span className="simple-field-lab">{t.simple.server}</span>
                <select className="simple-select" value={selectedNodeId}
                  disabled={selectionLocked || nodes.length === 0}
                  onChange={(event) => event.target.value ? onSelectNode(event.target.value) : onSelectAuto()}>
                  <option value="">{t.simple.auto}</option>
                  {nodes.map((node) => <option key={node.id} value={node.id}>{node.name}</option>)}
                </select>
              </label>
              <p className={`simple-hint${nodes.length === 0 ? " is-empty" : ""}`}>
                {nodes.length === 0 ? t.simple.noNodes : connected ? t.simple.changeHint : t.simple.autoHint}
              </p>

              {!unavailable && bypassInstalled && <details className="simple-details">
                <summary>{t.simple.details}</summary>
                <p className={`simple-bypass${bypassOn ? "" : " is-off"}`}>
                  <span className="simple-bypass-dot" aria-hidden="true" />
                  {bypassOn ? t.simple.bypassOn : t.simple.bypassOff}
                </p>
                {bypassOn && bypassStrategy && <p className="simple-strategy">{bypassStrategy}</p>}
              </details>}
            </section>
          </div>
        ) : unavailable ? (
          <section className="simple-wait" role="status">
            <h1>{statusLabel}</h1>
            <p>{t.simple.serviceHelp}</p>
          </section>
        ) : <SimpleSetup hasProfile={false} onSubscribe={onSubscribe} />}
      </main>

      <footer className="simple-foot">
        {onSettings && <button type="button" className="simple-link" onClick={onSettings}>{t.settings.title}</button>}
        <button type="button" className="simple-link" onClick={onReportProblem}>{t.report.action}</button>
      </footer>
    </div>
  );
}
