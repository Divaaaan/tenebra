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
import { importErrorMessage } from "../lib/importError";

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
    <section className="prof">
      <header className="prof-head">
        <div className="prof-headings">
          <span className="prof-eyebrow" aria-hidden="true">
            TNB · CONFIG
          </span>
          <h1 className="prof-title">{t.profiles.title}</h1>
        </div>
        <button
          type="button"
          className="prof-btn-primary"
          onClick={() => setImporting(true)}
        >
          {t.profiles.import.title}
        </button>
      </header>

      {profiles.length === 0 ? (
        <div className="prof-empty">
          <span className="prof-empty-glyph" aria-hidden="true">
            ▢
          </span>
          <h2 className="prof-empty-title">{t.profiles.empty}</h2>
          <p className="prof-empty-hint">{t.profiles.emptyHint}</p>
        </div>
      ) : (
        <ul className="prof-list">
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
        <ImportDialog tenebra={tenebra} onClose={() => setImporting(false)} />
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

// Calendar-day tone for the expiry readout, mirroring the day math in
// formatExpiry. Purely presentational: signal once expired, soft-warn inside a
// week, dim otherwise. Returns null when there's no usable timestamp.
type ExpiryTone = "expired" | "soon" | "ok";
function expiryTone(iso: string | undefined): ExpiryTone | null {
  if (!iso) {
    return null;
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return null;
  }
  const startOfDay = (d: Date) =>
    new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round(
    (startOfDay(date) - startOfDay(new Date())) / 86_400_000,
  );
  if (days < 0) {
    return "expired";
  }
  if (days <= 7) {
    return "soon";
  }
  return "ok";
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
  const tone = expiryTone(profile.expiresAt);

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
    <li className={`prof-card${selected ? " is-selected" : ""}`}>
      <div className="prof-card-head">
        <div className="prof-card-title">
          {connected && (
            <span className="prof-active-sq" aria-hidden="true" />
          )}
          <h2 className={connected ? "prof-name is-active" : "prof-name"}>
            {profile.name}
          </h2>
          <span className="prof-src">{t.profiles.source[profile.source]}</span>
          {connected && (
            <span className="prof-active-tag">{t.profiles.active}</span>
          )}
        </div>
        <button
          type="button"
          className="prof-count"
          aria-expanded={expanded}
          onClick={() => setExpanded((v) => !v)}
        >
          <span className="prof-count-n">{profile.nodes.length}</span>
          <span className="prof-count-lab">{t.profiles.nodes}</span>
          <span className="prof-count-caret" aria-hidden="true">
            {expanded ? "▣" : "▢"}
          </span>
        </button>
      </div>

      <dl className="prof-meta">
        <div className="prof-meta-cell">
          <dt className="prof-meta-lab">{t.profiles.updated}</dt>
          <dd className="prof-meta-val">{formatDate(profile.updatedAt, lang)}</dd>
        </div>
        {expiry && (
          <div className="prof-meta-cell">
            <dt className="prof-meta-lab">{t.profiles.expires}</dt>
            <dd
              className={`prof-meta-val prof-expiry prof-expiry--${tone ?? "ok"}`}
              title={formatDate(profile.expiresAt, lang)}
            >
              {expiry}
            </dd>
          </div>
        )}
        {usage && (
          <div className="prof-meta-cell">
            <dt className="prof-meta-lab">{t.profiles.traffic}</dt>
            <dd className="prof-meta-val">{usage}</dd>
          </div>
        )}
      </dl>

      {expanded && (
        <ul className="prof-nodes">
          {profile.nodes.map((node) => (
            <li key={node.id} className="prof-node">
              <div className="prof-node-info">
                <span className="prof-node-name">{node.name}</span>
                <span className="prof-node-detail">
                  {node.protocol} · {node.server}:{node.port}
                </span>
              </div>
              <PingBadge result={pingResultFor(node.id)} />
              <button
                type="button"
                className="prof-ghost"
                disabled={busy}
                onClick={() => connectNode(node.id)}
              >
                {t.home.connect}
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="prof-actions">
        <button
          type="button"
          className="prof-ghost"
          disabled={selected}
          onClick={onSelect}
        >
          {selected ? t.profiles.active : t.profiles.setActive}
        </button>
        <button
          type="button"
          className="prof-ghost"
          disabled={pinging}
          onClick={runPing}
        >
          {pinging ? t.profiles.pinging : t.profiles.pingAll}
        </button>
        {fastest && (
          <button
            type="button"
            className="prof-ghost"
            disabled={busy}
            onClick={() => connectNode(fastest.node)}
          >
            {t.profiles.autoSelect}
          </button>
        )}
        {profile.source === "subscription" && (
          <button
            type="button"
            className="prof-ghost"
            disabled={busy}
            onClick={refresh}
          >
            {t.profiles.refresh}
          </button>
        )}
        <button
          type="button"
          className="prof-ghost prof-ghost--danger"
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

// Pull the importable link lines out of a pasted block or file body: trim each
// line and drop blanks and comments (# or //), mirroring how the core's batch
// parser decides what even counts as a candidate. The count tells the link tab
// whether the user pasted one link (single import) or several (batch), and feeds
// both the textarea and the .txt-file paths so they behave identically.
function linkLines(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#") && !l.startsWith("//"));
}

// Read a chosen file's text in the webview with FileReader. We prefer this over
// File.text() / the Tauri fs plugin: it works inside WebView2 with no extra
// capability or Rust file command, and degrades to a rejected promise the caller
// can report rather than an unhandled throw.
function readFileText(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ""));
    reader.onerror = () => reject(reader.error ?? new Error("file read failed"));
    reader.readAsText(file);
  });
}

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
  // A success line for batch imports ("imported N, skipped M"). Unlike a single
  // import — which closes the dialog on success — a batch keeps the dialog open
  // so the user sees how many links landed and how many were skipped.
  const [result, setResult] = useState<string | null>(null);
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
    setResult(null);
    try {
      await action();
      await tenebra.refreshProfiles();
      onClose();
    } catch (e) {
      // The raw error is English and often carries the subscription URL, so we
      // classify it into a plain, localized cause instead of showing it.
      setError(importErrorMessage(e, t));
      setBusy(false);
    }
  }

  // Batch import of several links. On success the dialog stays open and reports
  // the imported/skipped counts (so the user learns a few links were dropped)
  // rather than vanishing. An empty result — the core found no valid links —
  // surfaces a clear message instead of the generic one.
  async function finishBatch(links: string[]) {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const res = await api.importLinks(links, name.trim() || undefined);
      await tenebra.refreshProfiles();
      setResult(
        t.profiles.import.batchResult
          .replace("{imported}", String(res.imported))
          .replace("{skipped}", String(res.skipped)),
      );
    } catch {
      // The core rejects a batch with no parseable links; tell the user plainly.
      setError(t.errors.batchEmpty);
    } finally {
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
    const lines = linkLines(link);
    if (lines.length === 0) {
      setError(t.errors.linkRequired);
      return;
    }
    // One link keeps the single-import path (a one-server manual profile);
    // several links collapse into one multi-server profile via the batch import,
    // which also reports how many were skipped.
    if (lines.length === 1) {
      void finish(() => api.importLink(lines[0], name.trim() || undefined));
    } else {
      void finishBatch(lines);
    }
  }

  async function onFileChosen(file: File) {
    let text: string;
    try {
      // Read via FileReader rather than file.text(): it needs no new Tauri
      // plugin or Rust file command (it runs entirely in the webview), and it is
      // the broadly-supported path. A read failure (e.g. the file vanished) is
      // surfaced rather than thrown.
      text = await readFileText(file);
    } catch {
      setError(t.errors.generic);
      return;
    }
    const lines = linkLines(text);
    if (lines.length === 0) {
      setError(t.errors.linkRequired);
      return;
    }
    // A .txt list is always a batch — even a single line — so the user gets a
    // consistent imported/skipped report and one profile holding every server.
    void finishBatch(lines);
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
      void finish(() => api.importSubscription(value, name.trim() || value));
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
      className="prof-modal-scrim"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        className="prof-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="import-title"
      >
        <h2 id="import-title" className="prof-modal-title">
          {t.profiles.import.title}
        </h2>

        <div
          className="prof-tabs"
          role="tablist"
          aria-label={t.profiles.import.title}
        >
          {tabs.map((id) => (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={tab === id}
              className={`prof-tab${tab === id ? " is-on" : ""}`}
              onClick={() => {
                setTab(id);
                setError(null);
                setResult(null);
              }}
            >
              {tabLabels[id]}
            </button>
          ))}
        </div>

        <div className="prof-modal-body" role="tabpanel">
          {tab !== "file" && (
            <label className="prof-field">
              <span className="prof-field-lab">{t.profiles.import.name}</span>
              <input
                ref={firstFieldRef}
                className="prof-input"
                value={name}
                placeholder={t.profiles.import.namePlaceholder}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
          )}

          {tab === "subscription" && (
            <label className="prof-field">
              <span className="prof-field-lab">{t.profiles.import.url}</span>
              <div className="prof-field-row">
                <input
                  className="prof-input"
                  value={url}
                  placeholder={t.profiles.import.urlPlaceholder}
                  inputMode="url"
                  onChange={(e) => setUrl(e.target.value)}
                />
                <button
                  type="button"
                  className="prof-ghost"
                  disabled={busy}
                  onClick={() => void pasteInto(setUrl)}
                >
                  {t.profiles.import.paste}
                </button>
              </div>
            </label>
          )}

          {tab === "link" && (
            <label className="prof-field">
              <span className="prof-field-lab">{t.profiles.import.link}</span>
              <textarea
                className="prof-input prof-input--area"
                value={link}
                rows={3}
                placeholder={t.profiles.import.linkPlaceholder}
                onChange={(e) => setLink(e.target.value)}
              />
              <p className="prof-field-hint">{t.profiles.import.linkHint}</p>
              <div className="prof-field-actions">
                <button
                  type="button"
                  className="prof-ghost"
                  disabled={busy}
                  onClick={() => void pasteInto(setLink)}
                >
                  {t.profiles.import.paste}
                </button>
              </div>
            </label>
          )}

          {tab === "file" && (
            <div className="prof-field">
              <button
                type="button"
                className="prof-ghost"
                disabled={busy}
                onClick={() => fileInputRef.current?.click()}
              >
                {t.profiles.import.pickFile}
              </button>
              <p className="prof-field-hint">{t.profiles.import.fileHint}</p>
              <input
                ref={fileInputRef}
                type="file"
                accept=".txt"
                className="prof-visually-hidden"
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
            <div className="prof-field">
              {qrSupported ? (
                <>
                  <div className="prof-field-row">
                    <button
                      type="button"
                      className="prof-ghost"
                      disabled={busy || scanning}
                      onClick={() => qrInputRef.current?.click()}
                    >
                      {scanning
                        ? t.profiles.import.qrScanning
                        : t.profiles.import.qrPick}
                    </button>
                    <button
                      type="button"
                      className="prof-ghost"
                      disabled={busy || scanning}
                      onClick={() => void onQrFromClipboard()}
                    >
                      {t.profiles.import.qrPasteImage}
                    </button>
                  </div>
                  <p className="prof-field-hint">{t.profiles.import.qrHint}</p>
                </>
              ) : (
                <p className="prof-field-hint">{t.errors.qrUnsupported}</p>
              )}
              <input
                ref={qrInputRef}
                type="file"
                accept="image/*"
                className="prof-visually-hidden"
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
            <p className="prof-error" role="alert">
              {error}
            </p>
          )}
          {result && (
            <p className="prof-success" role="status">
              {result}
            </p>
          )}
        </div>

        <div className="prof-modal-actions">
          <button type="button" className="prof-ghost" onClick={onClose}>
            {t.home.cancel}
          </button>
          {(tab === "subscription" || tab === "link") && (
            <button
              type="button"
              className="prof-btn-primary"
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
