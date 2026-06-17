import { useState } from "react";

import type { Profile } from "../api";
import type { Tenebra } from "../state/useTenebra";
import { useI18n } from "../i18n/I18nContext";
import { formatBytes, formatRate } from "../lib/format";

interface HomeScreenProps {
  tenebra: Tenebra;
  selectedProfile: Profile | null;
  onSelectProfile: (id: string) => void;
  onGoToProfiles: () => void;
}

export function HomeScreen({
  tenebra,
  selectedProfile,
  onSelectProfile,
  onGoToProfiles,
}: HomeScreenProps) {
  const { t } = useI18n();
  const { state, traffic, profiles } = tenebra;

  // "" means auto — let the core pick the lowest-ping node.
  const [nodeId, setNodeId] = useState("");
  const [busy, setBusy] = useState(false);

  if (profiles.length === 0) {
    return (
      <section className="home home--empty">
        <div className="empty-state">
          <h1>{t.home.noProfile}</h1>
          <p className="muted">{t.home.noProfileHint}</p>
          <button type="button" className="btn btn-primary" onClick={onGoToProfiles}>
            {t.nav.profiles}
          </button>
        </div>
      </section>
    );
  }

  const phase = state.state;
  const isConnected = phase === "connected";
  const isConnecting = phase === "connecting";
  const locked = isConnecting || isConnected;

  const activeProfile =
    profiles.find((p) => p.id === state.profile) ?? selectedProfile;
  const activeNode = activeProfile?.nodes.find((n) => n.id === state.node);

  async function handlePrimary() {
    if (busy) return;
    setBusy(true);
    try {
      if (phase === "connected" || phase === "connecting") {
        await tenebra.disconnect();
      } else if (selectedProfile) {
        await tenebra.connect(selectedProfile.id, nodeId || undefined);
      }
    } catch {
      // The state event reflects failures; swallow the rejection so a transient
      // error doesn't tear down the view.
    } finally {
      setBusy(false);
    }
  }

  const buttonLabel = isConnected
    ? t.home.disconnect
    : isConnecting
      ? t.state.connecting
      : t.home.connect;

  return (
    <section className="home">
      <div className="home-dial">
        <button
          type="button"
          className={`dial dial--${phase}`}
          onClick={handlePrimary}
          disabled={busy && !locked}
          title={isConnecting ? t.home.cancel : buttonLabel}
        >
          <span className="dial-ring" aria-hidden="true" />
          {isConnecting && <span className="dial-spinner" aria-hidden="true" />}
          <span className="dial-label">{buttonLabel}</span>
        </button>

        <p className="home-status" aria-live="polite">
          {t.state[phase]}
        </p>

        {phase === "error" && state.error && (
          <div className="home-error" role="alert">
            {state.error}
          </div>
        )}
      </div>

      <dl className="home-meta">
        <div className="home-meta-row">
          <dt>{t.home.activeProfile}</dt>
          <dd>{activeProfile?.name ?? "—"}</dd>
        </div>
        <div className="home-meta-row">
          <dt>{t.home.activeNode}</dt>
          <dd>{activeNode?.name ?? (locked ? t.home.autoNode : "—")}</dd>
        </div>
      </dl>

      {isConnected && (
        <div className="home-traffic">
          <div className="traffic-stat">
            <span className="traffic-label">{t.home.download}</span>
            <span className="traffic-rate">
              {formatRate(traffic.downRate, t.units.perSecond)}
            </span>
            <span className="traffic-total muted">
              {t.home.sessionTraffic}: {formatBytes(traffic.down)}
            </span>
          </div>
          <div className="traffic-stat">
            <span className="traffic-label">{t.home.upload}</span>
            <span className="traffic-rate">
              {formatRate(traffic.upRate, t.units.perSecond)}
            </span>
            <span className="traffic-total muted">
              {t.home.sessionTraffic}: {formatBytes(traffic.up)}
            </span>
          </div>
        </div>
      )}

      <div className="home-quick">
        <p className="home-quick-title">{t.home.quickConnect}</p>
        <div className="home-quick-row">
          <label className="field">
            <span className="field-label">{t.home.activeProfile}</span>
            <select
              className="control"
              value={selectedProfile?.id ?? ""}
              disabled={locked}
              onChange={(e) => {
                onSelectProfile(e.target.value);
                setNodeId("");
              }}
            >
              {profiles.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>

          <label className="field">
            <span className="field-label">{t.home.activeNode}</span>
            <select
              className="control"
              value={nodeId}
              disabled={locked || !selectedProfile}
              onChange={(e) => setNodeId(e.target.value)}
            >
              <option value="">{t.home.autoNode}</option>
              {selectedProfile?.nodes.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
            </select>
          </label>
        </div>
      </div>
    </section>
  );
}
