import { useRef, useState } from "react";
import type { State } from "../api";
import { useI18n } from "../i18n/I18nContext";

interface Props {
  state: State;
  reachable: boolean;
  onRetry: () => Promise<void>;
  onDisable: () => Promise<void>;
  onDisconnect: () => Promise<void>;
  onError: (error: unknown) => void;
}

export function ProtectionStatus({ state, reachable, onRetry, onDisable, onDisconnect, onError }: Props) {
  const { t } = useI18n();
  const p = state.protection;
  const pending = useRef(false);
  const [busy, setBusy] = useState(false);
  const hasGuard = p?.enforced && p.persistent;
  const unconfirmed = !reachable || (p?.status === "active" && state.state !== "connected");
  if (!state.kill_switch && (!p || p.status === "off" || p.status === "unavailable")) return null;

  // Retained evidence explains an outage, but cannot certify the current tunnel.
  const status = unconfirmed ? "unknown" : p?.status ?? "legacy";
  const active = status === "active" && hasGuard;
  const text = unconfirmed ? (hasGuard ? t.protection.lastGuard : t.protection.unknown)
    : !p ? t.protection.legacy
      : p.status === "active" ? (active ? t.protection.active : t.protection.unknown)
        : p.status === "blocked" ? (hasGuard ? t.protection.blocked : t.protection.unknown)
          : t.protection[p.status];
  const run = async (action: () => Promise<void>) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    try { await action(); } catch (error) { onError(error); }
    finally { pending.current = false; setBusy(false); }
  };
  const failed = status === "error";
  return <div className="protection-banner" data-protection={active ? "active" : status === "active" ? "unknown" : status} role={failed ? "alert" : "status"}>
    <div className="protection-copy">
      <p>{text}</p>
      {failed && hasGuard && <p>{t.protection.lastGuard}</p>}
      {p?.error && <details><summary>{t.errors.details}</summary><pre className="selectable">{p.error}</pre></details>}
    </div>
    {(failed || status === "blocked" || status === "unknown") && <div className="protection-actions">
      {failed && <button className="prof-ghost" disabled={busy} onClick={() => void run(onRetry)}>{t.protection.retry}</button>}
      {status === "blocked"
        ? <button className="prof-ghost" disabled={busy} onClick={() => void run(onDisconnect)}>{t.protection.disconnect}</button>
        : <button className="prof-ghost" disabled={busy} onClick={() => void run(onDisable)}>{t.protection.disable}</button>}
    </div>}
  </div>;
}
