import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { disable, enable, isEnabled } from "@tauri-apps/plugin-autostart";
import { getVersion } from "@tauri-apps/api/app";
import type { Update } from "@tauri-apps/plugin-updater";

import {
  api,
  type ConnectionMode,
  type RoutingMode,
  type SplitMode,
  type TunStack,
  type ZapretUpdate,
} from "../api";
import type { Tenebra } from "../state/useTenebra";
import { DiagnosticsPanel } from "../components/DiagnosticsPanel";
import { UpdateConfirm } from "../components/UpdateConfirm";
import { useI18n } from "../i18n/I18nContext";
import { useTheme } from "../theme/ThemeContext";
import { describeCoreError, type Language } from "../i18n/strings";
import { pushToast } from "../lib/toast";
import {
  getAutoFastest,
  getAutoInstallUpdates,
  getUpdateChannel,
  setAutoFastest,
  setAutoInstallUpdates,
  setUpdateChannel,
  type UpdateChannel,
} from "../lib/settings";
import { isValidDnsServer } from "../lib/dns";
import { isValidDomainSuffix } from "../lib/rules";
import {
  checkForUpdate,
  inAppUpdatesSupported,
  installUpdate,
  type UpdateStatus,
} from "../lib/updates";
import { tunnelBusy } from "../lib/tunnel";
import { useReducedMotion } from "../lib/useReducedMotion";

interface SettingsScreenProps {
  tenebra: Tenebra;
}

// The ordered sections, keyed so each key doubles as its i18n label
// (t.settings[key]) and its DOM anchor id. The left rail lists these; the content
// column renders a matching <section> per key. Kept at module scope so the
// identity is stable across renders.
const NAV_SECTIONS = [
  "routing",
  "split",
  "presets",
  "rules",
  "mode",
  "tunnel",
  "dns",
  "bypass",
  "multihop",
  "reliability",
  "diagnostics",
  "appearance",
  "startup",
  "updates",
] as const;
type SectionKey = (typeof NAV_SECTIONS)[number];

/**
 * Which section index is "active" for a given scroll position: the last section
 * whose top has crossed the threshold line (offsetTop − threshold ≤ scrollTop).
 * Pure and layout-free so the scroll tracking can be unit-tested without a real
 * viewport; falls back to the first section when nothing has scrolled past.
 */
export function pickActiveSection(
  scrollTop: number,
  sectionTops: number[],
  threshold = 60,
  viewport?: { clientHeight: number; scrollHeight: number },
): number {
  // A short final section can never reach the top of the scroll container, so
  // scrollTop alone would keep the previous rail item active and a click on the
  // last item would visibly "not take" (it snapped back once the smooth scroll
  // settled). When the container is scrolled to its bottom, the last section is
  // what the user is looking at — lock onto it.
  if (
    viewport &&
    scrollTop + viewport.clientHeight >= viewport.scrollHeight - 2
  ) {
    return sectionTops.length - 1;
  }
  let active = 0;
  for (let i = 0; i < sectionTops.length; i += 1) {
    if (scrollTop >= sectionTops[i] - threshold) {
      active = i;
    }
  }
  return active;
}

// A single custom-rule list editor: a validated domain input, an add button, and
// the current domains as removable rows. Both the "always direct" and "always
// through the tunnel" lists render this — only the label, hint, backing list, and
// the add/remove callbacks differ. Kept at module scope (not nested in
// SettingsScreen) so its draft state survives the parent's re-renders. The core
// owns the canonical list, so the rendered rows come from props; the draft is the
// only local state. onAdd receives the normalized (trimmed, lowercased) domain.
function RuleListEditor({
  idBase,
  label,
  hint,
  domains,
  onAdd,
  onRemove,
}: {
  idBase: string;
  label: string;
  hint: string;
  domains: string[];
  onAdd: (domain: string) => void;
  onRemove: (domain: string) => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState("");

  const trimmed = draft.trim();
  const normalized = trimmed.toLowerCase();
  // An empty draft is not "invalid" (nothing typed yet); a non-empty one is
  // flagged the moment it can't be a domain, mirroring the DNS resolver field.
  const valid = trimmed === "" || isValidDomainSuffix(trimmed);
  const canAdd =
    trimmed !== "" &&
    isValidDomainSuffix(trimmed) &&
    !domains.includes(normalized);
  const errorId = `${idBase}-error`;

  function add() {
    if (!canAdd) {
      return;
    }
    onAdd(normalized);
    setDraft("");
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      add();
    }
  }

  return (
    <div className="set-apps">
      <span className="set-eyebrow">{label}</span>
      <span className="set-row-hint">{hint}</span>
      <div className="set-add">
        <span className={`set-field${valid ? "" : " is-invalid"}`}>
          <span className="set-prompt" aria-hidden="true">
            $
          </span>
          <input
            id={idBase}
            type="text"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={onKey}
            placeholder={t.settings.rulesAddPlaceholder}
            aria-label={label}
            aria-invalid={!valid}
            aria-describedby={valid ? undefined : errorId}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
          />
        </span>
        <button
          type="button"
          className="set-btn"
          onClick={add}
          disabled={!canAdd}
          aria-label={`${t.settings.rulesAdd} — ${label}`}
        >
          {t.settings.rulesAdd}
        </button>
      </div>
      {!valid && (
        <p id={errorId} className="set-error" role="alert">
          {t.settings.rulesInvalid}
        </p>
      )}
      {domains.length === 0 ? (
        <p className="set-empty">{t.settings.rulesEmpty}</p>
      ) : (
        <ul className="set-app-list">
          {domains.map((name) => (
            <li key={name} className="set-app-row">
              <span className="set-app-name">{name}</span>
              <button
                type="button"
                className="set-remove"
                onClick={() => onRemove(name)}
                aria-label={`${t.settings.rulesRemove} ${name}`}
              >
                <span aria-hidden="true">✕</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function SettingsScreen({ tenebra }: SettingsScreenProps) {
  const { t, lang, setLang } = useI18n();
  const { theme, setTheme } = useTheme();

  // Every control below is core-owned: its drawn position comes from the state
  // the daemon echoes back, never from a local guess. So a refused command means
  // the control does not move — and while these were sent with a bare `void` or
  // a catch that discarded the error, that was the entire user-visible outcome.
  // Report it on the toast bus the rest of the app already uses, naming an
  // out-of-date service when that is what the refusal actually was.
  const reportRefusal = (e: unknown) => pushToast(describeCoreError(e, t));

  // Section navigation. The content column owns its own scroll; the rail tracks
  // which section is in view and jumps to one on click. A layout-free picker
  // (pickActiveSection) turns the scroll position into the active index.
  const reducedMotion = useReducedMotion();
  const mainRef = useRef<HTMLDivElement>(null);
  const sectionRefs = useRef<Map<SectionKey, HTMLElement>>(new Map());
  const [activeSection, setActiveSection] = useState<SectionKey>("routing");

  const setSectionRef = (key: SectionKey) => (el: HTMLElement | null) => {
    if (el) {
      sectionRefs.current.set(key, el);
    }
  };

  // Track the active section by scroll position: read each section's offsetTop
  // (relative to the positioned content column) and ask the picker which one the
  // scroll has reached. The scroll handler is rAF-throttled; the initial pass runs
  // synchronously so the rail is correct before the first scroll. Cleaned up on
  // unmount.
  useEffect(() => {
    const main = mainRef.current;
    if (!main) {
      return;
    }
    const updateActive = () => {
      const tops = NAV_SECTIONS.map(
        (key) =>
          sectionRefs.current.get(key)?.offsetTop ?? Number.POSITIVE_INFINITY,
      );
      setActiveSection(
        NAV_SECTIONS[
          pickActiveSection(main.scrollTop, tops, undefined, {
            clientHeight: main.clientHeight,
            scrollHeight: main.scrollHeight,
          })
        ],
      );
    };
    let frame = 0;
    const onScroll = () => {
      if (frame) {
        return;
      }
      frame = requestAnimationFrame(() => {
        frame = 0;
        updateActive();
      });
    };
    updateActive();
    main.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      main.removeEventListener("scroll", onScroll);
      if (frame) {
        cancelAnimationFrame(frame);
      }
    };
  }, []);

  function goToSection(key: SectionKey) {
    // Optimistic highlight so the click lands instantly; the scroll tracker then
    // confirms as the smooth scroll settles.
    setActiveSection(key);
    sectionRefs.current.get(key)?.scrollIntoView?.({
      behavior: reducedMotion ? "auto" : "smooth",
      block: "start",
    });
  }

  function closeSettings() {
    // The overlay's dismissal lives in App (an Escape key handler and the shared
    // ✕). Rather than thread a close callback the screen doesn't have, re-raise the
    // Escape the app already listens for — one owner for the close, no duplication.
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  }

  // Launch-at-login mirrors the OS autostart registration; we read the real
  // state on mount so the toggle reflects what the system actually has set.
  const [launchAtLogin, setLaunchAtLogin] = useState(false);
  const [launchBusy, setLaunchBusy] = useState(false);
  const [autoFastest, setAutoFastestState] = useState(getAutoFastest);
  const [autoInstall, setAutoInstallState] = useState(getAutoInstallUpdates);
  const [channel, setChannelState] = useState<UpdateChannel>(getUpdateChannel);
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus>({
    kind: "idle",
  });
  const [pendingUpdate, setPendingUpdate] = useState<Update | null>(null);
  // A manual install while a tunnel is up would be dropped by the relaunch, so it
  // waits on a confirm; this holds that dialog open.
  const [confirmingInstall, setConfirmingInstall] = useState(false);
  const [appVersion, setAppVersion] = useState("");
  // Whether this install can replace itself at all. False only for a Linux copy
  // that came from a package manager, where the whole updater flow is inert and
  // the row says where updates come from instead. Starts optimistic so the
  // controls never flicker from disabled to enabled on the common platforms.
  const [selfUpdating, setSelfUpdating] = useState(true);

  useEffect(() => {
    getVersion()
      .then(setAppVersion)
      .catch(() => {
        // The version line is cosmetic; leave it blank if the host call fails.
      });
  }, []);

  useEffect(() => {
    let active = true;
    void inAppUpdatesSupported().then((supported) => {
      if (active) {
        setSelfUpdating(supported);
      }
    });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    let active = true;
    isEnabled()
      .then((on) => {
        if (active) {
          setLaunchAtLogin(on);
        }
      })
      .catch(() => {
        // If the query fails, leave the toggle off rather than guessing on.
      });
    return () => {
      active = false;
    };
  }, []);

  async function toggleLaunchAtLogin() {
    if (launchBusy) {
      return;
    }
    setLaunchBusy(true);
    const next = !launchAtLogin;
    try {
      if (next) {
        await enable();
      } else {
        await disable();
      }
      setLaunchAtLogin(next);
    } catch {
      // Keep the toggle where it was if the OS rejected the change.
    } finally {
      setLaunchBusy(false);
    }
  }

  // Autoconnect is core-owned, like the kill switch: the daemon persists it and
  // reconnects the last profile when it starts (service mode: at boot). The UI
  // reflects the reported state and toggles it over the protocol.
  const autoconnect = tenebra.state.autoconnect ?? false;

  function toggleAutoconnect() {
    void tenebra.setAutoconnect(!autoconnect).catch(reportRefusal);
  }

  // Crash reports are core-owned and opt-in, like autoconnect: the daemon
  // persists the choice; the UI reflects and toggles it over the protocol.
  // Undefined (never asked) reads as off here.
  const crashReports = tenebra.state.crash_reports ?? false;

  function toggleCrashReports() {
    void tenebra.setCrashReports(!crashReports).catch(reportRefusal);
  }

  // Forced TLS-ClientHello fragmentation — the DPI-obfuscation override. Core-owned
  // like the kill switch: an opt-in override, off until armed, so an omitted field
  // reads as off.
  const tlsFragment = tenebra.state.tls_fragment ?? false;

  function toggleTlsFragment() {
    void tenebra.setTlsFragment(!tlsFragment).catch(reportRefusal);
  }

  // The bypass bundle's version and its updater. Both are core-owned; the version
  // is shown because a stale bundle fails exactly like a dead node or an expired
  // subscription, and knowing which of the three you have is otherwise guesswork.
  const bypassVersion = tenebra.state.zapret_version ?? "";
  const bypassAutoUpdate = tenebra.state.zapret_auto_update ?? true;
  const [bypassUpdating, setBypassUpdating] = useState(false);
  const [bypassUpdateNote, setBypassUpdateNote] = useState("");

  function updateBypass() {
    if (bypassUpdating) return;
    setBypassUpdating(true);
    setBypassUpdateNote("");
    void api
      .updateZapret()
      .then((r: ZapretUpdate) => {
        // Three successes to tell apart: an install happened; a newer bundle is
        // out but its checksum is not pinned into this client, so it is held back
        // until Tenebra itself is updated; or the installed one is already current
        // — the last means the bypass is not the problem, which is the whole reason
        // to look here.
        setBypassUpdateNote(
          r.blocked
            ? `${t.settings.bypassNeedsClientUpdate} ${r.latest}`
            : r.updated
              ? `${r.installed || "—"} → ${r.latest}`
              : `${t.settings.bypassUpToDate} ${r.latest || r.installed}`,
        );
      })
      .catch((e: unknown) => {
        setBypassUpdateNote(describeCoreError(e, t));
      })
      .finally(() => setBypassUpdating(false));
  }

  function toggleBypassAutoUpdate() {
    void api.setZapretAutoUpdate(!bypassAutoUpdate).catch(reportRefusal);
  }

  // Multihop two-hop chain. Core-owned like the other toggles: off with no
  // selection until the user picks an entry and an exit node. The choices are
  // drawn from the active profile (the connected one, else the first stored) and
  // sent by stable id; the core resolves them at connect time and falls back to a
  // single hop for a pair that no longer fits the profile.
  const multihopProfileId =
    tenebra.state.profile || tenebra.profiles[0]?.id || "";
  const multihopNodes =
    tenebra.profiles.find((p) => p.id === multihopProfileId)?.nodes ?? [];
  const multihopEnabled = tenebra.state.multihop?.enabled ?? false;
  const multihopEntry = tenebra.state.multihop?.entry_id ?? "";
  const multihopExit = tenebra.state.multihop?.exit_id ?? "";
  // The chain can only be armed once both ends are chosen and distinct — the same
  // rule the core enforces — so the toggle is inert until then (it can always turn
  // off). The selectors stay usable while off so a pick can be made first.
  const multihopArmable =
    multihopEntry !== "" &&
    multihopExit !== "" &&
    multihopEntry !== multihopExit;

  function toggleMultihop() {
    if (!multihopEnabled && !multihopArmable) {
      return;
    }
    void tenebra
      .setMultihop(
        multihopProfileId,
        !multihopEnabled,
        multihopEntry,
        multihopExit,
      )
      .catch(reportRefusal);
  }
  function selectMultihopEntry(entryId: string) {
    void tenebra
      .setMultihop(multihopProfileId, multihopEnabled, entryId, multihopExit)
      .catch(reportRefusal);
  }
  function selectMultihopExit(exitId: string) {
    void tenebra
      .setMultihop(multihopProfileId, multihopEnabled, multihopEntry, exitId)
      .catch(reportRefusal);
  }

  // Health-failover watchdog. On by default in the core, which projects the
  // effective value into State as a concrete bool — so the armed default arrives
  // as `auto_failover: true` (present) and only an explicit disarm is omitted.
  // Reading a missing field as off is therefore correct: a fresh install is never
  // missing it (the core sends true), and the sole time it is absent is after the
  // user turned it off. Mirrors every other core-owned toggle here.
  const autoFailover = tenebra.state.auto_failover ?? false;

  function toggleAutoFailover() {
    void tenebra.setAutoFailover(!autoFailover).catch(reportRefusal);
  }

  // Whether a tunnel is live — gates the diagnostics speed test.
  const connected = tenebra.state.state === "connected";

  // Simple mode is renderer-owned like the theme: a localStorage flag the app
  // shell reads to swap in a pared-back layout. Writing it raises a storage event
  // the shell listens for, so the change lands without a reload. The exact key
  // (`tenebra.simpleMode`) and "true"/"false" encoding are a contract with the
  // shell — keep them verbatim.
  const [simpleMode, setSimpleMode] = useState(
    () => localStorage.getItem("tenebra.simpleMode") === "true",
  );

  function toggleSimpleMode() {
    setSimpleMode((prev) => {
      const next = !prev;
      const value = next ? "true" : "false";
      localStorage.setItem("tenebra.simpleMode", value);
      // Same-document writes don't fire `storage` natively (that event is for
      // *other* tabs), so raise it ourselves for the app shell's listener.
      window.dispatchEvent(
        new StorageEvent("storage", {
          key: "tenebra.simpleMode",
          newValue: value,
        }),
      );
      return next;
    });
  }

  function toggleAutoFastest() {
    setAutoFastestState((prev) => {
      const next = !prev;
      setAutoFastest(next);
      return next;
    });
  }

  function toggleAutoInstall() {
    setAutoInstallState((prev) => {
      const next = !prev;
      setAutoInstallUpdates(next);
      return next;
    });
  }

  // Update channel is renderer-owned, like the auto-* toggles: the launch check
  // and the manual check read the persisted value to resolve which signed
  // manifest to compare against. The radiogroup mirrors the routing/stack
  // pattern below — a roving tabIndex plus arrow keys that carry focus with the
  // selection.
  const channelOptions: {
    value: UpdateChannel;
    label: string;
    hint: string;
  }[] = [
    {
      value: "stable",
      label: t.settings.updateChannelStable,
      hint: t.settings.updateChannelStableHint,
    },
    {
      value: "beta",
      label: t.settings.updateChannelBeta,
      hint: t.settings.updateChannelBetaHint,
    },
  ];

  const channelRefs = useRef<(HTMLButtonElement | null)[]>([]);

  function chooseChannel(next: UpdateChannel) {
    setChannelState(next);
    setUpdateChannel(next);
  }

  function onChannelKey(e: KeyboardEvent, index: number) {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") {
      return;
    }
    e.preventDefault();
    const delta = e.key === "ArrowDown" ? 1 : -1;
    const nextIndex =
      (index + delta + channelOptions.length) % channelOptions.length;
    chooseChannel(channelOptions[nextIndex].value);
    channelRefs.current[nextIndex]?.focus();
  }

  async function checkUpdates() {
    setUpdateStatus({ kind: "checking" });
    try {
      const update = await checkForUpdate();
      if (!update) {
        setPendingUpdate(null);
        setUpdateStatus({ kind: "uptodate" });
        return;
      }
      setPendingUpdate(update);
      setUpdateStatus({ kind: "available", version: update.version });
    } catch (e) {
      setUpdateStatus({ kind: "error", message: String(e) });
    }
  }

  // The download → install → relaunch itself. Shared by the direct (tunnel down)
  // path and the confirmed one, so the gate below has one thing to call.
  async function runUpdateInstall() {
    if (!pendingUpdate) {
      return;
    }
    setConfirmingInstall(false);
    setUpdateStatus({ kind: "downloading", percent: null });
    try {
      await installUpdate(pendingUpdate, (percent) =>
        setUpdateStatus({ kind: "downloading", percent }),
      );
      // relaunch() normally ends the process; reaching here means the install
      // completed and the restart is imminent.
      setUpdateStatus({ kind: "installing" });
    } catch (e) {
      setUpdateStatus({ kind: "error", message: String(e) });
    }
  }

  // The Install button. Installing relaunches the app (and on Windows stops the
  // background service to swap it), which would drop a live tunnel — so when one
  // is up, confirm first; when it's down, install straight away.
  function applyUpdate() {
    if (!pendingUpdate) {
      return;
    }
    if (tunnelBusy(tenebra.state.state)) {
      setConfirmingInstall(true);
      return;
    }
    void runUpdateInstall();
  }

  const updateBusy =
    updateStatus.kind === "checking" || updateStatus.kind === "downloading";
  const updateStatusText = ((): string => {
    // A packaged install has no flow to report on: say where updates do come
    // from, in place of every other status this row would show.
    if (!selfUpdating) {
      return t.settings.updatesPackaged;
    }
    switch (updateStatus.kind) {
      case "checking":
        return t.settings.updatesChecking;
      case "uptodate":
        return t.settings.updatesUpToDate;
      case "available":
        return t.settings.updatesAvailable.replace(
          "{version}",
          updateStatus.version,
        );
      case "downloading":
        return updateStatus.percent == null
          ? t.settings.updatesDownloading
          : `${t.settings.updatesDownloading} ${updateStatus.percent}%`;
      case "installing":
        return t.settings.updatesInstalling;
      case "error":
        return t.settings.updatesError;
      default:
        return appVersion
          ? t.settings.updatesCurrent.replace("{version}", appVersion)
          : "";
    }
  })();

  const routing = tenebra.state.routing ?? "smart";

  const routingOptions: { mode: RoutingMode; label: string; hint: string }[] = [
    {
      mode: "smart",
      label: t.settings.routingSmart,
      hint: t.settings.routingSmartHint,
    },
    {
      mode: "global",
      label: t.settings.routingGlobal,
      hint: t.settings.routingGlobalHint,
    },
    {
      mode: "direct",
      label: t.settings.routingDirect,
      hint: t.settings.routingDirectHint,
    },
  ];

  // One ref slot per radio button so an arrow key can move DOM focus onto the
  // newly selected option. Focus has to travel with the selection: the roving
  // tabIndex re-anchors on whatever holds focus, so without this the next key
  // press would recompute from the original (now stale) index and stall.
  const routingRefs = useRef<(HTMLButtonElement | null)[]>([]);

  function onRoutingKey(e: KeyboardEvent, index: number) {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") {
      return;
    }
    e.preventDefault();
    const delta = e.key === "ArrowDown" ? 1 : -1;
    const nextIndex =
      (index + delta + routingOptions.length) % routingOptions.length;
    void tenebra
      .setRouting(routingOptions[nextIndex].mode)
      .catch(reportRefusal);
    routingRefs.current[nextIndex]?.focus();
  }

  // Split tunnelling. The core owns the canonical list (it normalizes names), so
  // the rendered list always reflects tenebra.state rather than a local copy; the
  // text field is the only local state.
  const splitMode = tenebra.state.split ?? "off";
  const splitApps = tenebra.state.split_apps ?? [];
  const [appDraft, setAppDraft] = useState("");

  const splitOptions: { mode: SplitMode; label: string; hint: string }[] = [
    { mode: "off", label: t.settings.splitOff, hint: t.settings.splitOffHint },
    {
      mode: "exclude",
      label: t.settings.splitExclude,
      hint: t.settings.splitExcludeHint,
    },
    {
      mode: "include",
      label: t.settings.splitInclude,
      hint: t.settings.splitIncludeHint,
    },
  ];

  function chooseSplitMode(mode: SplitMode) {
    if (mode === splitMode) {
      return;
    }
    void tenebra.setSplit(mode, splitApps).catch(reportRefusal);
  }

  // See routingRefs: arrow keys move focus along with the selection here too.
  const splitRefs = useRef<(HTMLButtonElement | null)[]>([]);

  function onSplitKey(e: KeyboardEvent, index: number) {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") {
      return;
    }
    e.preventDefault();
    const delta = e.key === "ArrowDown" ? 1 : -1;
    const nextIndex =
      (index + delta + splitOptions.length) % splitOptions.length;
    chooseSplitMode(splitOptions[nextIndex].mode);
    splitRefs.current[nextIndex]?.focus();
  }

  // Normalize the draft the same way the core does for the dedupe check, so the
  // UI doesn't offer to add a name that will collapse server-side anyway.
  const normalizedDraft = appDraft.trim().toLowerCase();
  const canAddApp =
    splitMode !== "off" &&
    normalizedDraft.length > 0 &&
    !splitApps.includes(normalizedDraft);

  function addApp() {
    if (!canAddApp) {
      return;
    }
    void tenebra
      .setSplit(splitMode, [...splitApps, normalizedDraft])
      .catch(reportRefusal);
    setAppDraft("");
  }

  function removeApp(name: string) {
    void tenebra
      .setSplit(
        splitMode,
        splitApps.filter((a) => a !== name),
      )
      .catch(reportRefusal);
  }

  function onAppInputKey(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      addApp();
    }
  }

  // The connection mode (tun vs system proxy). Core-owned like the tun stack: the
  // daemon validates, persists, and — when a tunnel is live — re-applies it in
  // place (arming or clearing the OS proxy in step), so the UI just reflects
  // tenebra.state and requests changes.
  const proxyMode = tenebra.state.proxy_mode ?? "tun";

  const modeOptions: { mode: ConnectionMode; label: string; hint: string }[] = [
    { mode: "tun", label: t.settings.modeTun, hint: t.settings.modeTunHint },
    {
      mode: "system-proxy",
      label: t.settings.modeSystemProxy,
      hint: t.settings.modeSystemProxyHint,
    },
  ];

  function chooseMode(mode: ConnectionMode) {
    if (mode === proxyMode) {
      return;
    }
    void tenebra.setProxyMode(mode).catch(reportRefusal);
  }

  // See routingRefs: arrow keys move focus along with the selection here too.
  const modeRefs = useRef<(HTMLButtonElement | null)[]>([]);

  function onModeKey(e: KeyboardEvent, index: number) {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") {
      return;
    }
    e.preventDefault();
    const delta = e.key === "ArrowDown" ? 1 : -1;
    const nextIndex = (index + delta + modeOptions.length) % modeOptions.length;
    chooseMode(modeOptions[nextIndex].mode);
    modeRefs.current[nextIndex]?.focus();
  }

  // The tun network stack. Core-owned like routing: the daemon validates,
  // persists, and — when a tunnel is live — re-applies it in place, so the UI
  // just reflects tenebra.state and requests changes.
  const tunStack = tenebra.state.tun_stack ?? "system";

  const stackOptions: { stack: TunStack; label: string; hint: string }[] = [
    {
      stack: "system",
      label: t.settings.stackSystem,
      hint: t.settings.stackSystemHint,
    },
    {
      stack: "gvisor",
      label: t.settings.stackGvisor,
      hint: t.settings.stackGvisorHint,
    },
    {
      stack: "mixed",
      label: t.settings.stackMixed,
      hint: t.settings.stackMixedHint,
    },
  ];

  function chooseStack(stack: TunStack) {
    if (stack === tunStack) {
      return;
    }
    void tenebra.setTun(stack).catch(reportRefusal);
  }

  // See routingRefs: arrow keys move focus along with the selection here too.
  const stackRefs = useRef<(HTMLButtonElement | null)[]>([]);

  function onStackKey(e: KeyboardEvent, index: number) {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") {
      return;
    }
    e.preventDefault();
    const delta = e.key === "ArrowDown" ? 1 : -1;
    const nextIndex =
      (index + delta + stackOptions.length) % stackOptions.length;
    chooseStack(stackOptions[nextIndex].stack);
    stackRefs.current[nextIndex]?.focus();
  }

  // DNS: ad/tracker blocking, IPv4-only, plus the two custom resolvers. Core-owned
  // like the rest — the daemon validates, persists, and (when a tunnel is live)
  // re-applies them in place. Each set_dns carries the whole set, so we derive it
  // from the current toggles and the two resolver drafts, ignoring a malformed
  // draft.
  const adBlock = tenebra.state.ad_block ?? false;
  const ipv4Only = tenebra.state.ipv4_only ?? false;
  const dnsRemote = tenebra.state.dns_remote ?? "";
  const dnsDirect = tenebra.state.dns_direct ?? "";
  const [remoteDraft, setRemoteDraft] = useState(dnsRemote);
  const [directDraft, setDirectDraft] = useState(dnsDirect);

  // Keep the drafts in step with the effective values the core reports (our own
  // commit's result, or another window's change). Editing only pushes on blur or
  // Enter, and dns_remote/dns_direct change only when we commit, so this never
  // clobbers an in-progress edit.
  useEffect(() => setRemoteDraft(dnsRemote), [dnsRemote]);
  useEffect(() => setDirectDraft(dnsDirect), [dnsDirect]);

  const remoteValid = isValidDnsServer(remoteDraft.trim());
  const directValid = isValidDnsServer(directDraft.trim());

  // The resolver values a set_dns should carry: the valid draft, or the effective
  // value when the draft is malformed — a malformed resolver is never sent.
  const remoteValue = remoteValid ? remoteDraft.trim() : dnsRemote;
  const directValue = directValid ? directDraft.trim() : dnsDirect;

  // Every set_dns carries the whole set of preferences, so each toggle/edit path
  // re-sends the current values of the others alongside the one it changes.
  function pushDns(nextAdBlock: boolean, nextIpv4Only: boolean) {
    void tenebra
      .setDns(nextAdBlock, remoteValue, directValue, nextIpv4Only)
      .catch(reportRefusal);
  }

  function toggleAdBlock() {
    pushDns(!adBlock, ipv4Only);
  }

  function toggleIpv4Only() {
    pushDns(adBlock, !ipv4Only);
  }

  // Commit a resolver edit: only when a valid draft actually changes an effective
  // resolver, so blurring an unchanged (or malformed) field is a no-op.
  function commitResolvers() {
    if (remoteValue === dnsRemote && directValue === dnsDirect) {
      return;
    }
    pushDns(adBlock, ipv4Only);
  }

  function onResolverKey(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      commitResolvers();
    }
  }

  // Custom routing rules: two RU direct-rule presets plus two per-domain lists.
  // Core-owned like the rest — the daemon validates, normalizes, persists, and
  // (when a tunnel is live) re-applies in place. Each set_rules carries the whole
  // set, so every toggle/edit re-sends the current values of the others alongside
  // the one it changes.
  const rulesDirect = tenebra.state.rules_direct ?? [];
  const rulesProxy = tenebra.state.rules_proxy ?? [];
  const presetRuBanking = tenebra.state.preset_ru_banking ?? false;
  const presetRuGov = tenebra.state.preset_ru_gov ?? false;

  function pushRules(
    nextDirect: string[],
    nextProxy: string[],
    nextBanking: boolean,
    nextGov: boolean,
  ) {
    void tenebra
      .setRules(nextDirect, nextProxy, nextBanking, nextGov)
      .catch(reportRefusal);
  }

  function toggleRuleBanking() {
    pushRules(rulesDirect, rulesProxy, !presetRuBanking, presetRuGov);
  }

  function toggleRuleGov() {
    pushRules(rulesDirect, rulesProxy, presetRuBanking, !presetRuGov);
  }

  // Routing presets. Two of the three take a class of traffic out of the tunnel,
  // so the state has to be visible: for three releases the command existed only
  // in the core, nothing carried the presets to the UI, and the only way to turn
  // one off was to hand-edit settings.json. Unlike set_rules, each command names
  // only the preset it changes — the core leaves the unnamed ones alone.
  const presetGamesDirect = tenebra.state.preset_games_direct ?? false;
  const presetVoiceDirect = tenebra.state.preset_voice_direct ?? false;
  const presetUnblockServices = tenebra.state.preset_unblock_services ?? false;

  function pushPresets(presets: {
    games?: boolean;
    voice?: boolean;
    services?: boolean;
  }) {
    void tenebra.setPresets(presets).catch(reportRefusal);
  }

  const themeOptions: { value: "dark" | "light"; label: string }[] = [
    { value: "dark", label: t.settings.themeDark },
    { value: "light", label: t.settings.themeLight },
  ];

  const langOptions: { value: Language; label: string }[] = [
    { value: "en", label: "EN" },
    { value: "ru", label: "RU" },
  ];

  return (
    <div className="set-shell">
      <nav className="set-nav" aria-label={t.settings.nav}>
        <h1 className="set-nav-title">{t.settings.title}</h1>
        <div className="set-nav-list">
          {NAV_SECTIONS.map((key) => {
            const active = activeSection === key;
            return (
              <button
                key={key}
                type="button"
                className={`set-nav-item${active ? " is-active" : ""}`}
                aria-current={active ? "true" : undefined}
                onClick={() => goToSection(key)}
              >
                {t.settings[key]}
              </button>
            );
          })}
        </div>
        <p className="set-nav-foot">
          {appVersion
            ? t.settings.rail.replace("{version}", appVersion)
            : t.settings.license}
        </p>
      </nav>

      <div className="set-main" ref={mainRef}>
        <div className="set-close-bar">
          <button
            type="button"
            className="set-close"
            onClick={closeSettings}
            aria-label={t.settings.close}
          >
            <span className="set-close-key">{t.settings.escLabel}</span>
            <span aria-hidden="true">✕</span>
          </button>
        </div>

        <section
          className="set-section"
          id="set-sec-routing"
          ref={setSectionRef("routing")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.routing}</h2>
          </div>
          <div
            className="set-options"
            role="radiogroup"
            aria-label={t.settings.routing}
          >
            {routingOptions.map((opt, index) => {
              const checked = routing === opt.mode;
              return (
                <button
                  key={opt.mode}
                  ref={(el) => {
                    routingRefs.current[index] = el;
                  }}
                  type="button"
                  role="radio"
                  aria-checked={checked}
                  tabIndex={checked ? 0 : -1}
                  className={`set-option${checked ? " is-checked" : ""}`}
                  onClick={() =>
                    void tenebra.setRouting(opt.mode).catch(reportRefusal)
                  }
                  onKeyDown={(e) => onRoutingKey(e, index)}
                >
                  <span className="set-mark" aria-hidden="true">
                    {checked ? "▣" : "▢"}
                  </span>
                  <span className="set-option-text">
                    <span className="set-option-label">{opt.label}</span>
                    <span className="set-option-hint">{opt.hint}</span>
                  </span>
                </button>
              );
            })}
          </div>
        </section>

        <section
          className="set-section"
          id="set-sec-split"
          ref={setSectionRef("split")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.split}</h2>
            <p className="set-sub">{t.settings.splitHint}</p>
          </div>

          <div
            className="set-options"
            role="radiogroup"
            aria-label={t.settings.split}
          >
            {splitOptions.map((opt, index) => {
              const checked = splitMode === opt.mode;
              return (
                <button
                  key={opt.mode}
                  ref={(el) => {
                    splitRefs.current[index] = el;
                  }}
                  type="button"
                  role="radio"
                  aria-checked={checked}
                  tabIndex={checked ? 0 : -1}
                  className={`set-option${checked ? " is-checked" : ""}`}
                  onClick={() => chooseSplitMode(opt.mode)}
                  onKeyDown={(e) => onSplitKey(e, index)}
                >
                  <span className="set-mark" aria-hidden="true">
                    {checked ? "▣" : "▢"}
                  </span>
                  <span className="set-option-text">
                    <span className="set-option-label">{opt.label}</span>
                    <span className="set-option-hint">{opt.hint}</span>
                  </span>
                </button>
              );
            })}
          </div>

          {splitMode !== "off" && (
            <div className="set-apps">
              <span className="set-eyebrow">{t.settings.splitApps}</span>
              <div className="set-add">
                <span className="set-field">
                  <span className="set-prompt" aria-hidden="true">
                    $
                  </span>
                  <input
                    type="text"
                    value={appDraft}
                    onChange={(e) => setAppDraft(e.target.value)}
                    onKeyDown={onAppInputKey}
                    placeholder={t.settings.splitAddPlaceholder}
                    aria-label={t.settings.splitApps}
                    spellCheck={false}
                    autoCapitalize="off"
                    autoCorrect="off"
                  />
                </span>
                <button
                  type="button"
                  className="set-btn"
                  onClick={addApp}
                  disabled={!canAddApp}
                >
                  {t.settings.splitAdd}
                </button>
              </div>

              {splitApps.length === 0 ? (
                <p className="set-empty">{t.settings.splitAppsEmpty}</p>
              ) : (
                <ul className="set-app-list">
                  {splitApps.map((name) => (
                    <li key={name} className="set-app-row">
                      <span className="set-app-name">{name}</span>
                      <button
                        type="button"
                        className="set-remove"
                        onClick={() => removeApp(name)}
                        aria-label={`${t.settings.splitRemove} ${name}`}
                      >
                        <span aria-hidden="true">✕</span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </section>

        <section
          className="set-section"
          id="set-sec-presets"
          ref={setSectionRef("presets")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.presets}</h2>
            <p className="set-sub">{t.settings.presetsHint}</p>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">
                {t.settings.presetUnblockServices}
              </span>
              <span className="set-row-hint">
                {t.settings.presetUnblockServicesHint}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={presetUnblockServices}
              className={`set-switch${presetUnblockServices ? " is-on" : ""}`}
              onClick={() => pushPresets({ services: !presetUnblockServices })}
            >
              <span className="set-switch-box" aria-hidden="true">
                {presetUnblockServices ? "▣" : "▢"}
              </span>
              {presetUnblockServices ? "ON" : "OFF"}
            </button>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">
                {t.settings.presetGamesDirect}
              </span>
              <span className="set-row-hint">
                {t.settings.presetGamesDirectHint}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={presetGamesDirect}
              className={`set-switch${presetGamesDirect ? " is-on" : ""}`}
              onClick={() => pushPresets({ games: !presetGamesDirect })}
            >
              <span className="set-switch-box" aria-hidden="true">
                {presetGamesDirect ? "▣" : "▢"}
              </span>
              {presetGamesDirect ? "ON" : "OFF"}
            </button>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">
                {t.settings.presetVoiceDirect}
              </span>
              <span className="set-row-hint">
                {t.settings.presetVoiceDirectHint}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={presetVoiceDirect}
              className={`set-switch${presetVoiceDirect ? " is-on" : ""}`}
              onClick={() => pushPresets({ voice: !presetVoiceDirect })}
            >
              <span className="set-switch-box" aria-hidden="true">
                {presetVoiceDirect ? "▣" : "▢"}
              </span>
              {presetVoiceDirect ? "ON" : "OFF"}
            </button>
          </div>
        </section>

        <section
          className="set-section"
          id="set-sec-rules"
          ref={setSectionRef("rules")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.rules}</h2>
            <p className="set-sub">{t.settings.rulesHint}</p>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">
                {t.settings.presetRuBanking}
              </span>
              <span className="set-row-hint">
                {t.settings.presetRuBankingHint}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={presetRuBanking}
              className={`set-switch${presetRuBanking ? " is-on" : ""}`}
              onClick={toggleRuleBanking}
            >
              <span className="set-switch-box" aria-hidden="true">
                {presetRuBanking ? "▣" : "▢"}
              </span>
              {presetRuBanking ? "ON" : "OFF"}
            </button>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.presetRuGov}</span>
              <span className="set-row-hint">{t.settings.presetRuGovHint}</span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={presetRuGov}
              className={`set-switch${presetRuGov ? " is-on" : ""}`}
              onClick={toggleRuleGov}
            >
              <span className="set-switch-box" aria-hidden="true">
                {presetRuGov ? "▣" : "▢"}
              </span>
              {presetRuGov ? "ON" : "OFF"}
            </button>
          </div>

          <RuleListEditor
            idBase="rules-direct"
            label={t.settings.rulesDirect}
            hint={t.settings.rulesDirectHint}
            domains={rulesDirect}
            onAdd={(domain) =>
              pushRules(
                [...rulesDirect, domain],
                rulesProxy,
                presetRuBanking,
                presetRuGov,
              )
            }
            onRemove={(domain) =>
              pushRules(
                rulesDirect.filter((d) => d !== domain),
                rulesProxy,
                presetRuBanking,
                presetRuGov,
              )
            }
          />

          <RuleListEditor
            idBase="rules-proxy"
            label={t.settings.rulesProxy}
            hint={t.settings.rulesProxyHint}
            domains={rulesProxy}
            onAdd={(domain) =>
              pushRules(
                rulesDirect,
                [...rulesProxy, domain],
                presetRuBanking,
                presetRuGov,
              )
            }
            onRemove={(domain) =>
              pushRules(
                rulesDirect,
                rulesProxy.filter((d) => d !== domain),
                presetRuBanking,
                presetRuGov,
              )
            }
          />
        </section>

        <section
          className="set-section"
          id="set-sec-mode"
          ref={setSectionRef("mode")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.mode}</h2>
            <p className="set-sub">{t.settings.modeHint}</p>
          </div>
          <div
            className="set-options"
            role="radiogroup"
            aria-label={t.settings.mode}
          >
            {modeOptions.map((opt, index) => {
              const checked = proxyMode === opt.mode;
              return (
                <button
                  key={opt.mode}
                  ref={(el) => {
                    modeRefs.current[index] = el;
                  }}
                  type="button"
                  role="radio"
                  aria-checked={checked}
                  tabIndex={checked ? 0 : -1}
                  className={`set-option${checked ? " is-checked" : ""}`}
                  onClick={() => chooseMode(opt.mode)}
                  onKeyDown={(e) => onModeKey(e, index)}
                >
                  <span className="set-mark" aria-hidden="true">
                    {checked ? "▣" : "▢"}
                  </span>
                  <span className="set-option-text">
                    <span className="set-option-label">{opt.label}</span>
                    <span className="set-option-hint">{opt.hint}</span>
                  </span>
                </button>
              );
            })}
          </div>
        </section>

        <section
          className="set-section"
          id="set-sec-tunnel"
          ref={setSectionRef("tunnel")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.tunnel}</h2>
            <p className="set-sub">{t.settings.tunnelHint}</p>
          </div>
          <div
            className="set-options"
            role="radiogroup"
            aria-label={t.settings.tunnel}
          >
            {stackOptions.map((opt, index) => {
              const checked = tunStack === opt.stack;
              return (
                <button
                  key={opt.stack}
                  ref={(el) => {
                    stackRefs.current[index] = el;
                  }}
                  type="button"
                  role="radio"
                  aria-checked={checked}
                  tabIndex={checked ? 0 : -1}
                  className={`set-option${checked ? " is-checked" : ""}`}
                  onClick={() => chooseStack(opt.stack)}
                  onKeyDown={(e) => onStackKey(e, index)}
                >
                  <span className="set-mark" aria-hidden="true">
                    {checked ? "▣" : "▢"}
                  </span>
                  <span className="set-option-text">
                    <span className="set-option-label">{opt.label}</span>
                    <span className="set-option-hint">{opt.hint}</span>
                  </span>
                </button>
              );
            })}
          </div>
        </section>

        <section
          className="set-section"
          id="set-sec-dns"
          ref={setSectionRef("dns")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.dns}</h2>
            <p className="set-sub">{t.settings.dnsHint}</p>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.adBlock}</span>
              <span className="set-row-hint">{t.settings.adBlockHint}</span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={adBlock}
              className={`set-switch${adBlock ? " is-on" : ""}`}
              onClick={toggleAdBlock}
            >
              <span className="set-switch-box" aria-hidden="true">
                {adBlock ? "▣" : "▢"}
              </span>
              {adBlock ? "ON" : "OFF"}
            </button>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.ipv4Only}</span>
              <span className="set-row-hint">{t.settings.ipv4OnlyHint}</span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={ipv4Only}
              className={`set-switch${ipv4Only ? " is-on" : ""}`}
              onClick={toggleIpv4Only}
            >
              <span className="set-switch-box" aria-hidden="true">
                {ipv4Only ? "▣" : "▢"}
              </span>
              {ipv4Only ? "ON" : "OFF"}
            </button>
          </div>

          <div className="set-apps">
            <label className="set-eyebrow" htmlFor="dns-remote">
              {t.settings.dnsRemote}
            </label>
            <span className="set-row-hint">{t.settings.dnsRemoteHint}</span>
            <span className={`set-field${remoteValid ? "" : " is-invalid"}`}>
              <span className="set-prompt" aria-hidden="true">
                $
              </span>
              <input
                id="dns-remote"
                type="text"
                value={remoteDraft}
                onChange={(e) => setRemoteDraft(e.target.value)}
                onKeyDown={onResolverKey}
                onBlur={commitResolvers}
                placeholder={t.settings.dnsPlaceholder}
                aria-label={t.settings.dnsRemote}
                aria-invalid={!remoteValid}
                aria-describedby={remoteValid ? undefined : "dns-remote-error"}
                spellCheck={false}
                autoCapitalize="off"
                autoCorrect="off"
              />
            </span>
            {!remoteValid && (
              <p id="dns-remote-error" className="set-error" role="alert">
                {t.settings.dnsInvalid}
              </p>
            )}
          </div>

          <div className="set-apps">
            <label className="set-eyebrow" htmlFor="dns-direct">
              {t.settings.dnsDirect}
            </label>
            <span className="set-row-hint">{t.settings.dnsDirectHint}</span>
            <span className={`set-field${directValid ? "" : " is-invalid"}`}>
              <span className="set-prompt" aria-hidden="true">
                $
              </span>
              <input
                id="dns-direct"
                type="text"
                value={directDraft}
                onChange={(e) => setDirectDraft(e.target.value)}
                onKeyDown={onResolverKey}
                onBlur={commitResolvers}
                placeholder={t.settings.dnsPlaceholder}
                aria-label={t.settings.dnsDirect}
                aria-invalid={!directValid}
                aria-describedby={directValid ? undefined : "dns-direct-error"}
                spellCheck={false}
                autoCapitalize="off"
                autoCorrect="off"
              />
            </span>
            {!directValid && (
              <p id="dns-direct-error" className="set-error" role="alert">
                {t.settings.dnsInvalid}
              </p>
            )}
          </div>
        </section>

        <section
          className="set-section"
          id="set-sec-bypass"
          ref={setSectionRef("bypass")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.bypass}</h2>
            <p className="set-sub">{t.settings.bypassHint}</p>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.tlsFragment}</span>
              <span className="set-row-hint">{t.settings.tlsFragmentHint}</span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={tlsFragment}
              className={`set-switch${tlsFragment ? " is-on" : ""}`}
              onClick={toggleTlsFragment}
            >
              <span className="set-switch-box" aria-hidden="true">
                {tlsFragment ? "▣" : "▢"}
              </span>
              {tlsFragment ? "ON" : "OFF"}
            </button>
          </div>

          {/* The bundle's version, and a way to move it. A bypass a few releases
              behind does not get slower, it stops working — and it fails exactly
              like a dead node or an expired subscription, so the version is the
              one fact that tells those apart. */}
          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.bypassVersion}</span>
              <span className="set-row-hint">
                {bypassVersion
                  ? `${t.settings.bypassVersionInstalled} ${bypassVersion}`
                  : t.settings.bypassVersionUnknown}
                {bypassUpdateNote ? ` · ${bypassUpdateNote}` : ""}
              </span>
            </span>
            <button
              type="button"
              className="set-btn"
              disabled={bypassUpdating}
              onClick={updateBypass}
            >
              {bypassUpdating
                ? t.settings.bypassUpdating
                : t.settings.bypassUpdate}
            </button>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">
                {t.settings.bypassAutoUpdate}
              </span>
              <span className="set-row-hint">
                {t.settings.bypassAutoUpdateHint}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={bypassAutoUpdate}
              className={`set-switch${bypassAutoUpdate ? " is-on" : ""}`}
              onClick={toggleBypassAutoUpdate}
            >
              <span className="set-switch-box" aria-hidden="true">
                {bypassAutoUpdate ? "▣" : "▢"}
              </span>
              {bypassAutoUpdate ? "ON" : "OFF"}
            </button>
          </div>
        </section>

        <section
          className="set-section"
          id="set-sec-multihop"
          ref={setSectionRef("multihop")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.multihop}</h2>
            <p className="set-sub">{t.settings.multihopHint}</p>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.multihopEnable}</span>
              <span className="set-row-hint">
                {t.settings.multihopEnableHint}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={multihopEnabled}
              disabled={!multihopEnabled && !multihopArmable}
              className={`set-switch${multihopEnabled ? " is-on" : ""}`}
              onClick={toggleMultihop}
            >
              <span className="set-switch-box" aria-hidden="true">
                {multihopEnabled ? "▣" : "▢"}
              </span>
              {multihopEnabled ? "ON" : "OFF"}
            </button>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.multihopEntry}</span>
              <span className="set-row-hint">
                {t.settings.multihopEntryHint}
              </span>
            </span>
            <select
              className="simple-select"
              aria-label={t.settings.multihopEntry}
              value={multihopEntry}
              disabled={multihopNodes.length === 0}
              onChange={(e) => selectMultihopEntry(e.target.value)}
            >
              <option value="">{t.settings.multihopUnset}</option>
              {multihopNodes.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
            </select>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.multihopExit}</span>
              <span className="set-row-hint">
                {t.settings.multihopExitHint}
              </span>
            </span>
            <select
              className="simple-select"
              aria-label={t.settings.multihopExit}
              value={multihopExit}
              disabled={multihopNodes.length === 0}
              onChange={(e) => selectMultihopExit(e.target.value)}
            >
              <option value="">{t.settings.multihopUnset}</option>
              {multihopNodes.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
            </select>
          </div>
        </section>

        <section
          className="set-section"
          id="set-sec-reliability"
          ref={setSectionRef("reliability")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.reliability}</h2>
            <p className="set-sub">{t.settings.reliabilityHint}</p>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.autoFailover}</span>
              <span className="set-row-hint">
                {t.settings.autoFailoverHint}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={autoFailover}
              className={`set-switch${autoFailover ? " is-on" : ""}`}
              onClick={toggleAutoFailover}
            >
              <span className="set-switch-box" aria-hidden="true">
                {autoFailover ? "▣" : "▢"}
              </span>
              {autoFailover ? "ON" : "OFF"}
            </button>
          </div>
        </section>

        <section
          className="set-section"
          id="set-sec-diagnostics"
          ref={setSectionRef("diagnostics")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.diagnostics}</h2>
            <p className="set-sub">{t.settings.diagnosticsHint}</p>
          </div>

          <DiagnosticsPanel connected={connected} />
        </section>

        <section
          className="set-section"
          id="set-sec-appearance"
          ref={setSectionRef("appearance")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.appearance}</h2>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.theme}</span>
            </span>
            <div className="set-seg" role="group" aria-label={t.settings.theme}>
              {themeOptions.map((opt) => (
                <button
                  key={opt.value}
                  type="button"
                  aria-pressed={theme === opt.value}
                  className={`${theme === opt.value ? "is-active" : ""}`}
                  onClick={() => setTheme(opt.value)}
                >
                  {opt.label}
                </button>
              ))}
            </div>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.language}</span>
            </span>
            <div
              className="set-seg"
              role="group"
              aria-label={t.settings.language}
            >
              {langOptions.map((opt) => (
                <button
                  key={opt.value}
                  type="button"
                  aria-pressed={lang === opt.value}
                  className={`${lang === opt.value ? "is-active" : ""}`}
                  onClick={() => setLang(opt.value)}
                >
                  {opt.label}
                </button>
              ))}
            </div>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.simpleMode}</span>
              <span className="set-row-hint">{t.settings.simpleModeHint}</span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={simpleMode}
              className={`set-switch${simpleMode ? " is-on" : ""}`}
              onClick={toggleSimpleMode}
            >
              <span className="set-switch-box" aria-hidden="true">
                {simpleMode ? "▣" : "▢"}
              </span>
              {simpleMode ? "ON" : "OFF"}
            </button>
          </div>
        </section>

        <section
          className="set-section"
          id="set-sec-startup"
          ref={setSectionRef("startup")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.startup}</h2>
          </div>
          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.launchAtLogin}</span>
              <span className="set-row-hint">
                {t.settings.launchAtLoginHint}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={launchAtLogin}
              disabled={launchBusy}
              className={`set-switch${launchAtLogin ? " is-on" : ""}`}
              onClick={() => void toggleLaunchAtLogin()}
            >
              <span className="set-switch-box" aria-hidden="true">
                {launchAtLogin ? "▣" : "▢"}
              </span>
              {launchAtLogin ? "ON" : "OFF"}
            </button>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.autoconnect}</span>
              <span className="set-row-hint">{t.settings.autoconnectHint}</span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={autoconnect}
              className={`set-switch${autoconnect ? " is-on" : ""}`}
              onClick={toggleAutoconnect}
            >
              <span className="set-switch-box" aria-hidden="true">
                {autoconnect ? "▣" : "▢"}
              </span>
              {autoconnect ? "ON" : "OFF"}
            </button>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.autoFastest}</span>
              <span className="set-row-hint">{t.settings.autoFastestHint}</span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={autoFastest}
              className={`set-switch${autoFastest ? " is-on" : ""}`}
              onClick={toggleAutoFastest}
            >
              <span className="set-switch-box" aria-hidden="true">
                {autoFastest ? "▣" : "▢"}
              </span>
              {autoFastest ? "ON" : "OFF"}
            </button>
          </div>
        </section>

        <section
          className="set-section"
          id="set-sec-updates"
          ref={setSectionRef("updates")}
        >
          <div className="set-section-head">
            <h2 className="set-eyebrow">{t.settings.updates}</h2>
          </div>

          <div className="set-channel">
            <span className="set-eyebrow">{t.settings.updateChannel}</span>
            <div
              className="set-options"
              role="radiogroup"
              aria-label={t.settings.updateChannel}
            >
              {channelOptions.map((opt, index) => {
                const checked = channel === opt.value;
                return (
                  <button
                    key={opt.value}
                    ref={(el) => {
                      channelRefs.current[index] = el;
                    }}
                    type="button"
                    role="radio"
                    aria-checked={checked}
                    tabIndex={checked ? 0 : -1}
                    className={`set-option${checked ? " is-checked" : ""}`}
                    onClick={() => chooseChannel(opt.value)}
                    onKeyDown={(e) => onChannelKey(e, index)}
                  >
                    <span className="set-mark" aria-hidden="true">
                      {checked ? "▣" : "▢"}
                    </span>
                    <span className="set-option-text">
                      <span className="set-option-label">{opt.label}</span>
                      <span className="set-option-hint">{opt.hint}</span>
                    </span>
                  </button>
                );
              })}
            </div>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.updatesCheck}</span>
              <span className="set-row-hint">{updateStatusText}</span>
            </span>
            {selfUpdating &&
            pendingUpdate &&
            (updateStatus.kind === "available" ||
              updateStatus.kind === "error") ? (
              <button type="button" className="set-btn" onClick={applyUpdate}>
                {t.settings.updatesInstall}
              </button>
            ) : (
              <button
                type="button"
                className="set-btn"
                onClick={() => void checkUpdates()}
                disabled={updateBusy || !selfUpdating}
              >
                {updateStatus.kind === "checking"
                  ? t.settings.updatesChecking
                  : t.settings.updatesCheck}
              </button>
            )}
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.autoInstall}</span>
              <span className="set-row-hint">{t.settings.autoInstallHint}</span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={autoInstall}
              className={`set-switch${autoInstall ? " is-on" : ""}`}
              onClick={toggleAutoInstall}
              // Nothing to arm on a packaged install: the launch check does not
              // run there, so the preference could only ever be a dead switch.
              disabled={!selfUpdating}
            >
              <span className="set-switch-box" aria-hidden="true">
                {autoInstall ? "▣" : "▢"}
              </span>
              {autoInstall ? "ON" : "OFF"}
            </button>
          </div>

          <div className="set-row">
            <span className="set-row-text">
              <span className="set-row-label">{t.settings.crashReports}</span>
              <span className="set-row-hint">
                {t.settings.crashReportsHint}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={crashReports}
              className={`set-switch${crashReports ? " is-on" : ""}`}
              onClick={toggleCrashReports}
            >
              <span className="set-switch-box" aria-hidden="true">
                {crashReports ? "▣" : "▢"}
              </span>
              {crashReports ? "ON" : "OFF"}
            </button>
          </div>
        </section>
      </div>

      {confirmingInstall && (
        <UpdateConfirm
          onConfirm={() => void runUpdateInstall()}
          onCancel={() => setConfirmingInstall(false)}
        />
      )}
    </div>
  );
}
