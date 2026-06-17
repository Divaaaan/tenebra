import { useEffect, useMemo, useRef, useState } from "react";

import { api, type PingResult, type Profile } from "../api";
import type { Tenebra } from "../state/useTenebra";
import { useI18n } from "../i18n/I18nContext";
import type { Strings } from "../i18n/strings";
import { formatDate, formatExpiry, formatTrafficUsage } from "../lib/format";
import { ClipboardError, readClipboardText } from "../lib/clipboard";
import {
  decodeQrFromBlob,
  decodeQrFromClipboard,
  isQrDecodingSupported,
  QrError,
} from "../lib/qr";
import { PingBadge } from "../components/PingBadge";

interface ProfilesScreenProps {
  tenebra: Tenebra;
  selectedProfileId: string | null;
  onSelectProfile: (id: string) => void;
}

export function ProfilesScreen({
  tenebra,
  selectedProfileId,
  onSelectProfile,
}: ProfilesScreenProps) {
  const { t } = useI18n();
  const { profiles, state } = tenebra;

  const [importing, setImporting] = useState(false);
  const [pings, setPings] = useState<Record<string, PingResult[]>>({});

  return (
    <section className="screen profiles">
      <header className="screen-head">
        <h1>{t.profiles.title}</h1>
        <button
          type="button"
          className="btn btn-primary"
          onClick={() => setImporting(true)}
        >
          {t.profiles.import.title}
        </button>
      </header>

      {profiles.length === 0 ? (
        <div className="empty-state">
          <h2>{t.profiles.empty}</h2>
          <p className="muted">{t.profiles.emptyHint}</p>
        </div>
      ) : (
        <ul className="profile-list">
          {profiles.map((profile) => (
            <ProfileCard
              key={profile.id}
              profile={profile}
              tenebra={tenebra}
              connectedProfileId={state.profile}
              selected={selectedProfileId === profile.id}
              onSelect={() => onSelectProfile(profile.id)}
              pings={pings[profile.id]}
              onPings={(results) =>
                setPings((prev) => ({ ...prev, [profile.id]: results }))
              }
            />
          ))}
        </ul>
      )}

      {importing && (
        <ImportDialog
          tenebra={tenebra}
          onClose={() => setImporting(false)}
        />
      )}
    </section>
  );
}

interface ProfileCardProps {
  profile: Profile;
  tenebra: Tenebra;
  connectedProfileId?: string;
  selected: boolean;
  onSelect: () => void;
  pings?: PingResult[];
  onPings: (results: PingResult[]) => void;
}

function ProfileCard({
  profile,
  tenebra,
  connectedProfileId,
  selected,
  onSelect,
  pings,
  onPings,
}: ProfileCardProps) {
  const { t, lang } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const [pinging, setPinging] = useState(false);
  const [busy, setBusy] = useState(false);

  const connected = connectedProfileId === profile.id;
  const usage = formatTrafficUsage(profile.trafficUsed, profile.trafficTotal);
  const expiry = formatExpiry(profile.expiresAt, lang, {
    in: t.profiles.expiresIn,
    today: t.profiles.expiresToday,
    tomorrow: t.profiles.expiresTomorrow,
    expired: t.profiles.expired,
  });

  const fastest = useMemo(() => {
    const ok = (pings ?? []).filter((r) => r.ok);
    if (ok.length === 0) {
      return null;
    }
    return ok.reduce((best, r) => (r.rttMs < best.rttMs ? r : best));
  }, [pings]);

  async function runPing() {
    setPinging(true);
    try {
      onPings(await api.ping(profile.id));
    } catch {
      // Leave previous results untouched on failure.
    } finally {
      setPinging(false);
    }
  }

  async function connectNode(nodeId?: string) {
    setBusy(true);
    try {
      await tenebra.connect(profile.id, nodeId);
    } catch {
      // The state stream surfaces the failure.
    } finally {
      setBusy(false);
    }
  }

  async function refresh() {
    setBusy(true);
    try {
      // The core emits a profiles event when the refresh changes stored data,
      // which reloads the list; no explicit refetch needed here.
      await api.refreshSubscription(profile.id);
    } catch {
      // Non-fatal; keep the stale view.
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!window.confirm(t.profiles.removeConfirm)) {
      return;
    }
    setBusy(true);
    try {
      await api.removeProfile(profile.id);
      await tenebra.refreshProfiles();
    } catch {
      setBusy(false);
    }
  }

  const pingResultFor = (nodeId: string) =>
    pings?.find((r) => r.node === nodeId);

  return (
    <li className={`card profile-card${selected ? " is-selected" : ""}`}>
      <div className="profile-head">
        <div className="profile-title">
          <h2>{profile.name}</h2>
          <span className={`badge badge--${profile.source}`}>
            {t.profiles.source[profile.source]}
          </span>
          {connected && <span className="badge badge--active">{t.profiles.active}</span>}
        </div>
        <button
          type="button"
          className="btn btn-ghost btn-sm"
          aria-expanded={expanded}
          onClick={() => setExpanded((v) => !v)}
        >
          {profile.nodes.length} {t.profiles.nodes}
        </button>
      </div>

      <dl className="profile-meta">
        <div>
          <dt>{t.profiles.updated}</dt>
          <dd>{formatDate(profile.updatedAt, lang)}</dd>
        </div>
        {expiry && (
          <div>
            <dt>{t.profiles.expires}</dt>
            <dd title={formatDate(profile.expiresAt, lang)}>{expiry}</dd>
          </div>
        )}
        {usage && (
          <div>
            <dt>{t.profiles.traffic}</dt>
            <dd>{usage}</dd>
          </div>
        )}
      </dl>

      {expanded && (
        <ul className="node-list">
          {profile.nodes.map((node) => (
            <li key={node.id} className="node-row">
              <div className="node-info">
                <span className="node-name">{node.name}</span>
                <span className="node-detail muted">
                  {node.protocol} · {node.server}:{node.port}
                </span>
              </div>
              <PingBadge result={pingResultFor(node.id)} />
              <button
                type="button"
                className="btn btn-ghost btn-sm"
                disabled={busy}
                onClick={() => connectNode(node.id)}
              >
                {t.home.connect}
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="profile-actions">
        <button
          type="button"
          className="btn btn-secondary btn-sm"
          disabled={selected}
          onClick={onSelect}
        >
          {selected ? t.profiles.active : t.profiles.setActive}
        </button>
        <button
          type="button"
          className="btn btn-secondary btn-sm"
          disabled={pinging}
          onClick={runPing}
        >
          {pinging ? t.profiles.pinging : t.profiles.pingAll}
        </button>
        {fastest && (
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            disabled={busy}
            onClick={() => connectNode(fastest.node)}
          >
            {t.profiles.autoSelect}
          </button>
        )}
        {profile.source === "subscription" && (
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            disabled={busy}
            onClick={refresh}
          >
            {t.profiles.refresh}
          </button>
        )}
        <button
          type="button"
          className="btn btn-danger btn-sm"
          disabled={busy}
          onClick={remove}
        >
          {t.profiles.remove}
        </button>
      </div>
    </li>
  );
}

type ImportTab = "subscription" | "link" | "file" | "qr";

// Map the typed clipboard/QR failures to a user-facing message. Kept beside the
// dialog so both the paste button and the QR panel report failures the same way.
function clipboardErrorMessage(err: unknown, t: Strings): string {
  if (err instanceof ClipboardError) {
    return err.kind === "empty"
      ? t.errors.clipboardEmpty
      : t.errors.clipboardDenied;
  }
  return t.errors.generic;
}

function qrErrorMessage(err: unknown, t: Strings): string {
  if (err instanceof QrError) {
    switch (err.kind) {
      case "unsupported":
        return t.errors.qrUnsupported;
      case "notFound":
        return t.errors.qrNotFound;
      default:
        return t.errors.qrDecodeFailed;
    }
  }
  return t.errors.generic;
}

interface ImportDialogProps {
  tenebra: Tenebra;
  onClose: () => void;
}

function ImportDialog({ tenebra, onClose }: ImportDialogProps) {
  const { t } = useI18n();
  const [tab, setTab] = useState<ImportTab>("subscription");
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [link, setLink] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [scanning, setScanning] = useState(false);

  const firstFieldRef = useRef<HTMLInputElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const qrInputRef = useRef<HTMLInputElement>(null);

  // Probe once on mount; the QR tab uses this to decide whether to even offer
  // scanning, so the user isn't sent down a path the runtime can't follow.
  const qrSupported = useMemo(() => isQrDecodingSupported(), []);

  useEffect(() => {
    firstFieldRef.current?.focus();
  }, []);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        onClose();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  async function finish(action: () => Promise<unknown>) {
    setBusy(true);
    setError(null);
    try {
      await action();
      await tenebra.refreshProfiles();
      onClose();
    } catch {
      setError(t.errors.generic);
      setBusy(false);
    }
  }

  function submitSubscription() {
    if (!name.trim()) {
      setError(t.errors.nameRequired);
      return;
    }
    if (!url.trim()) {
      setError(t.errors.urlRequired);
      return;
    }
    void finish(() => api.importSubscription(url.trim(), name.trim()));
  }

  function submitLink() {
    if (!link.trim()) {
      setError(t.errors.linkRequired);
      return;
    }
    void finish(() => api.importLink(link.trim(), name.trim() || undefined));
  }

  async function onFileChosen(file: File) {
    const lines = (await file.text())
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean);
    if (lines.length === 0) {
      setError(t.errors.linkRequired);
      return;
    }
    void finish(async () => {
      for (const line of lines) {
        await api.importLink(line);
      }
    });
  }

  // Fill the active field from the clipboard so the user needn't paste by hand.
  // Empty and denied are reported distinctly; a successful read clears any error.
  async function pasteInto(set: (value: string) => void) {
    setError(null);
    try {
      set(await readClipboardText());
    } catch (err) {
      setError(clipboardErrorMessage(err, t));
    }
  }

  // Route a decoded QR string through the existing import path: a web URL is a
  // subscription (named like the subscription tab), anything else a server link.
  function importDecoded(value: string) {
    const isSubscription = /^https?:\/\//i.test(value);
    if (isSubscription) {
      void finish(() =>
        api.importSubscription(value, name.trim() || value),
      );
    } else {
      void finish(() => api.importLink(value, name.trim() || undefined));
    }
  }

  async function onQrBlob(blob: Blob) {
    setScanning(true);
    setError(null);
    try {
      importDecoded(await decodeQrFromBlob(blob));
    } catch (err) {
      setError(qrErrorMessage(err, t));
    } finally {
      setScanning(false);
    }
  }

  async function onQrFromClipboard() {
    setScanning(true);
    setError(null);
    try {
      importDecoded(await decodeQrFromClipboard());
    } catch (err) {
      setError(qrErrorMessage(err, t));
    } finally {
      setScanning(false);
    }
  }

  const tabs: ImportTab[] = ["subscription", "link", "file", "qr"];
  const tabLabels: Record<ImportTab, string> = {
    subscription: t.profiles.import.tabSubscription,
    link: t.profiles.import.tabLink,
    file: t.profiles.import.tabFile,
    qr: t.profiles.import.tabQr,
  };

  return (
    <div
      className="modal-overlay"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="import-title"
      >
        <h2 id="import-title" className="modal-title">
          {t.profiles.import.title}
        </h2>

        <div className="tabs" role="tablist" aria-label={t.profiles.import.title}>
          {tabs.map((id) => (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={tab === id}
              className={`tab${tab === id ? " active" : ""}`}
              onClick={() => {
                setTab(id);
                setError(null);
              }}
            >
              {tabLabels[id]}
            </button>
          ))}
        </div>

        <div className="modal-body" role="tabpanel">
          {tab !== "file" && (
            <label className="field">
              <span className="field-label">{t.profiles.import.name}</span>
              <input
                ref={firstFieldRef}
                className="control"
                value={name}
                placeholder={t.profiles.import.namePlaceholder}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
          )}

          {tab === "subscription" && (
            <label className="field">
              <span className="field-label">{t.profiles.import.url}</span>
              <div className="field-row">
                <input
                  className="control"
                  value={url}
                  placeholder={t.profiles.import.urlPlaceholder}
                  inputMode="url"
                  onChange={(e) => setUrl(e.target.value)}
                />
                <button
                  type="button"
                  className="btn btn-secondary"
                  disabled={busy}
                  onClick={() => void pasteInto(setUrl)}
                >
                  {t.profiles.import.paste}
                </button>
              </div>
            </label>
          )}

          {tab === "link" && (
            <label className="field">
              <span className="field-label">{t.profiles.import.link}</span>
              <textarea
                className="control control--area"
                value={link}
                rows={3}
                placeholder={t.profiles.import.linkPlaceholder}
                onChange={(e) => setLink(e.target.value)}
              />
              <div className="field-actions">
                <button
                  type="button"
                  className="btn btn-secondary btn-sm"
                  disabled={busy}
                  onClick={() => void pasteInto(setLink)}
                >
                  {t.profiles.import.paste}
                </button>
              </div>
            </label>
          )}

          {tab === "file" && (
            <div className="field">
              <button
                type="button"
                className="btn btn-secondary"
                disabled={busy}
                onClick={() => fileInputRef.current?.click()}
              >
                {t.profiles.import.pickFile}
              </button>
              <p className="field-hint muted">{t.profiles.import.fileHint}</p>
              <input
                ref={fileInputRef}
                type="file"
                accept=".txt"
                className="visually-hidden"
                onChange={(e) => {
                  const file = e.target.files?.[0];
                  if (file) {
                    void onFileChosen(file);
                  }
                }}
              />
            </div>
          )}

          {tab === "qr" && (
            <div className="field">
              {qrSupported ? (
                <>
                  <div className="field-row">
                    <button
                      type="button"
                      className="btn btn-secondary"
                      disabled={busy || scanning}
                      onClick={() => qrInputRef.current?.click()}
                    >
                      {scanning ? t.profiles.import.qrScanning : t.profiles.import.qrPick}
                    </button>
                    <button
                      type="button"
                      className="btn btn-secondary"
                      disabled={busy || scanning}
                      onClick={() => void onQrFromClipboard()}
                    >
                      {t.profiles.import.qrPasteImage}
                    </button>
                  </div>
                  <p className="field-hint muted">{t.profiles.import.qrHint}</p>
                </>
              ) : (
                <p className="field-hint muted">{t.errors.qrUnsupported}</p>
              )}
              <input
                ref={qrInputRef}
                type="file"
                accept="image/*"
                className="visually-hidden"
                onChange={(e) => {
                  const file = e.target.files?.[0];
                  // Reset so picking the same file again still fires onChange.
                  e.target.value = "";
                  if (file) {
                    void onQrBlob(file);
                  }
                }}
              />
            </div>
          )}

          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
        </div>

        <div className="modal-actions">
          <button type="button" className="btn btn-ghost" onClick={onClose}>
            {t.home.cancel}
          </button>
          {(tab === "subscription" || tab === "link") && (
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy}
              onClick={tab === "subscription" ? submitSubscription : submitLink}
            >
              {busy ? t.profiles.import.importing : t.profiles.import.submit}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
