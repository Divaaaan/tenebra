//! The catalogue of installed applications the split-tunnelling picker offers.
//!
//! A split rule matches sing-box's `process_name` — the executable's *file
//! name*, case-insensitively — so [`AppEntry::exe`] is the load-bearing field
//! and [`AppEntry::name`] exists only for human eyes. Nothing here ever
//! substitutes a pretty name for a file name.
//!
//! The scan deliberately lives in the GUI process: on Windows the core runs as
//! a LocalSystem service, which sees neither the user's `HKCU` hive nor their
//! Start Menu, so a scan from there would return a stranger's shortened list.
//!
//! Three properties the callers depend on:
//!
//! * **Bounded.** The whole scan shares one [`SCAN_BUDGET`] and one
//!   [`MAX_ENTRIES`] cap. Every source checks the budget between items, so a
//!   slow network drive or a pathological Start Menu cannot wedge the window.
//! * **Partial beats empty.** A source that fails is recorded in
//!   [`AppScan::warnings`] and the rest still run. Hitting the entry cap sets
//!   [`AppScan::truncated`] so the caller can say so rather than imply the list
//!   is complete.
//! * **Private.** The list of applications a person has installed is the most
//!   sensitive data this app holds. It is returned to the caller and nowhere
//!   else: never logged, never written to disk, never put in diagnostics. The
//!   warning strings name sources and failures only — never an app or a path.

mod icons;
// Every platform module is compiled on every host. Only the collectors are
// platform-gated inside; the parsers (`.desktop`, `Info.plist`, `.lnk`, icon
// containers) are ordinary code, which keeps them type-checked and unit-tested
// wherever the crate builds instead of only on their native OS.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
mod linux;
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
mod macos;
#[cfg_attr(not(windows), allow(dead_code))]
mod windows;

use std::collections::HashMap;
use std::time::{Duration, Instant};

use serde::Serialize;

/// Where an entry was found. Serialized as-is, so the front end can group or
/// caption by origin.
pub const SOURCE_REGISTRY: &str = "registry";
pub const SOURCE_STARTMENU: &str = "startmenu";
pub const SOURCE_PROCESS: &str = "process";
pub const SOURCE_BUNDLE: &str = "bundle";
pub const SOURCE_DESKTOP: &str = "desktop";

/// Wall-clock ceiling for one scan, icons included. The picker opens against
/// this list, so the number is a UI latency budget, not a thoroughness knob.
const SCAN_BUDGET: Duration = Duration::from_millis(3000);

/// How much of [`SCAN_BUDGET`] enumeration may spend. Whatever is left goes to
/// icons, which are optional garnish — see [`icons`].
const ENUM_BUDGET: Duration = Duration::from_millis(1800);

/// Hard cap on returned entries. A machine with more installed programs than
/// this yields a truncated list rather than a slow one.
const MAX_ENTRIES: usize = 400;

/// Cap on how many entries get an icon, and on the total base64 payload the
/// icons may add. Icons dominate the response size (a 32x32 RGBA PNG is a few
/// kilobytes once base64'd), so both are bounded independently of the time
/// budget.
const MAX_ICONS: usize = 300;
const MAX_ICON_BYTES: usize = 3 * 1024 * 1024;

/// One selectable application.
///
/// `exe` is what ends up in the split rule: the executable's file name, lower
/// case, no directory part. `path` is the full path when we know it — the
/// picker uses it to group an app with the other executables in its folder, and
/// the icon loader uses it as the source to read from.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AppEntry {
    /// Human-readable name, e.g. "Telegram Desktop". Display only.
    pub name: String,
    /// Executable file name in lower case, e.g. "telegram.exe". This is the
    /// value the rule matches on.
    pub exe: String,
    /// Full path to the executable, when a source could supply one.
    pub path: Option<String>,
    /// `data:image/png;base64,...`, at most 48x48. `None` when we could not
    /// read one — the UI draws its own placeholder rather than us inventing an
    /// icon.
    pub icon: Option<String>,
    /// The process is alive right now.
    pub running: bool,
    /// Which source produced the entry: one of the `SOURCE_*` constants.
    pub source: &'static str,
}

/// A finished scan: the entries plus what the caller needs to describe the
/// result honestly.
#[derive(Debug, Clone, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AppScan {
    pub apps: Vec<AppEntry>,
    /// The entry cap or the time budget cut the scan short, so the list is
    /// incomplete. Say so in the UI instead of implying it is everything.
    pub truncated: bool,
    /// Non-fatal problems, one per failed source. Source names and reasons
    /// only — never an application name or a path (see the privacy note above).
    pub warnings: Vec<String>,
}

/// Every installed application we could find, ordered running-first then by
/// name. This is the shape the Tauri command layer registers.
pub async fn list_installed_apps() -> Result<Vec<AppEntry>, String> {
    scan_installed_apps().await.map(|scan| scan.apps)
}

/// The same scan with its truncation flag and warnings intact. Preferred for
/// the command the picker calls, since a silently truncated list reads as a
/// complete one.
pub async fn scan_installed_apps() -> Result<AppScan, String> {
    // The scan is filesystem- and registry-bound and takes up to SCAN_BUDGET; a
    // sync Tauri command would freeze the event loop for exactly that long.
    tauri::async_runtime::spawn_blocking(|| scan(Budget::new(SCAN_BUDGET), MAX_ENTRIES))
        .await
        .map_err(|e| format!("app scan task failed: {e}"))
}

/// The other executables that live beside `path`, as pickable entries.
///
/// One application often ships several binaries (a launcher plus the process
/// that actually opens sockets, helper processes), and a rule naming only the
/// launcher silently does nothing. The picker offers these as related rows once
/// a user selects an app; it is a separate call because it is only worth paying
/// for on demand.
pub async fn list_sibling_apps(path: String) -> Result<Vec<AppEntry>, String> {
    tauri::async_runtime::spawn_blocking(move || siblings_of(&path, MAX_SIBLINGS))
        .await
        .map_err(|e| format!("sibling scan task failed: {e}"))
}

/// Cap on siblings returned for one folder. Some install directories hold
/// dozens of tools; the picker wants the app's own binaries, not a file manager.
const MAX_SIBLINGS: usize = 24;

/// Executables sitting next to `path`, excluding `path` itself. Ordered by name
/// for a stable list.
fn siblings_of(path: &str, limit: usize) -> Vec<AppEntry> {
    let target = std::path::Path::new(path);
    let Some(dir) = target.parent() else {
        return Vec::new();
    };
    let self_name = file_name_lower(path);
    let Ok(read) = std::fs::read_dir(dir) else {
        return Vec::new();
    };

    let mut out = Vec::new();
    for item in read.flatten() {
        if out.len() >= limit {
            break;
        }
        let p = item.path();
        if !is_executable_file(&p) {
            continue;
        }
        let full = p.to_string_lossy().to_string();
        let Some(exe) = file_name_lower(&full) else {
            continue;
        };
        if self_name.as_deref() == Some(exe.as_str()) || is_installer_exe(&exe) {
            continue;
        }
        out.push(AppEntry {
            name: pretty_stem(&full),
            exe,
            path: Some(full),
            icon: None,
            running: false,
            source: SOURCE_PROCESS,
        });
    }
    out.sort_by_key(|a| a.name.to_lowercase());
    out
}

/// Whether `p` is a file we would offer as a split-rule target. On Windows that
/// means the `.exe` extension; elsewhere any regular file in an app directory
/// qualifies, since Unix executables carry no extension.
fn is_executable_file(p: &std::path::Path) -> bool {
    if !p.is_file() {
        return false;
    }
    if cfg!(windows) {
        return p
            .extension()
            .map(|e| e.eq_ignore_ascii_case("exe"))
            .unwrap_or(false);
    }
    true
}

/// Run one full scan against `budget`, capping the result at `limit` entries.
///
/// Sources run cheapest-and-most-relevant first: live processes, then the
/// platform's installed-application registry. Processes go first on purpose —
/// they are the entries a user is most likely to want, so they must not be the
/// ones the cap drops. A later source that knows the app better upgrades the
/// row's name and source in place (see [`Collector::add`]).
fn scan(budget: Budget, limit: usize) -> AppScan {
    let mut collector = Collector::new(limit);
    let enum_budget = budget.sub_budget(ENUM_BUDGET);

    #[cfg(windows)]
    windows::collect(&mut collector, &enum_budget);
    #[cfg(target_os = "macos")]
    macos::collect(&mut collector, &enum_budget);
    #[cfg(target_os = "linux")]
    linux::collect(&mut collector, &enum_budget);

    // A platform we have no collector for still returns a well-formed empty
    // scan rather than an error, so the UI can say "nothing found here".
    #[cfg(not(any(windows, target_os = "macos", target_os = "linux")))]
    {
        let _ = &enum_budget;
        collector.warn("platform: no application source");
    }

    if enum_budget.expired() {
        collector.truncated = true;
        collector.warn("enumeration: time budget exhausted");
    }

    let (mut scan, hints) = collector.finish();
    icons::fill(&mut scan, &hints, &budget);
    scan
}

/// Where a source keeps an entry's artwork when the executable is not it, keyed
/// by [`AppEntry::exe`]. Linux desktop entries name a theme icon rather than a
/// file, and that name cannot be recovered from the binary afterwards.
pub(crate) type IconHints = HashMap<String, String>;

/// A shared deadline. Sources check it between items rather than being
/// interrupted, so a scan always ends on a consistent list.
#[derive(Debug, Clone, Copy)]
pub(crate) struct Budget {
    deadline: Instant,
}

impl Budget {
    fn new(total: Duration) -> Budget {
        Budget {
            deadline: Instant::now() + total,
        }
    }

    /// A budget that ends after `slice`, or when this one does — whichever
    /// comes first. Used to reserve the tail of the scan for icons.
    fn sub_budget(&self, slice: Duration) -> Budget {
        let end = Instant::now() + slice;
        Budget {
            deadline: end.min(self.deadline),
        }
    }

    pub(crate) fn expired(&self) -> bool {
        Instant::now() >= self.deadline
    }

    /// An already-spent budget, for tests that assert the early-out paths.
    #[cfg(test)]
    pub(crate) fn spent() -> Budget {
        Budget {
            deadline: Instant::now() - Duration::from_millis(1),
        }
    }
}

/// Accumulates entries, deduplicating on the lower-cased executable name.
///
/// Deduplication is the whole point: the same application usually turns up in
/// two or three sources, and the split rule keys on the executable name, so two
/// rows for one name would be two identical rules. A repeat sighting *enriches*
/// the row it matches — a running process flips `running`, a registry entry
/// upgrades a process-derived name — instead of appending a near-duplicate.
pub(crate) struct Collector {
    index: HashMap<String, usize>,
    entries: Vec<AppEntry>,
    hints: IconHints,
    warnings: Vec<String>,
    limit: usize,
    truncated: bool,
}

impl Collector {
    pub(crate) fn new(limit: usize) -> Collector {
        Collector {
            index: HashMap::new(),
            entries: Vec::new(),
            hints: IconHints::new(),
            warnings: Vec::new(),
            limit,
            truncated: false,
        }
    }

    /// Record a sighting.
    ///
    /// `exe_hint` may be a bare file name or a full path; only its file-name
    /// component survives, lower-cased. A hint with no usable file name is
    /// dropped — an entry without an executable name cannot become a rule.
    pub(crate) fn add(
        &mut self,
        name: &str,
        exe_hint: &str,
        path: Option<String>,
        source: &'static str,
        running: bool,
    ) {
        let Some(exe) = normalize_exe(exe_hint) else {
            return;
        };
        let name = clean_name(name);
        let name = if name.is_empty() {
            pretty_stem(&exe)
        } else {
            name
        };

        if let Some(&i) = self.index.get(&exe) {
            let existing = &mut self.entries[i];
            existing.running |= running;
            if existing.path.is_none() {
                existing.path = path;
            }
            // A better-informed source wins the display name and the label; a
            // peer source only fills in for a name that is just the file name.
            let better = source_rank(source) > source_rank(existing.source);
            let fills_placeholder = source_rank(source) == source_rank(existing.source)
                && is_placeholder_name(&existing.name, &exe)
                && !is_placeholder_name(&name, &exe);
            if better || fills_placeholder {
                existing.name = name;
                existing.source = source;
            }
            return;
        }

        if self.entries.len() >= self.limit {
            self.truncated = true;
            return;
        }

        self.index.insert(exe.clone(), self.entries.len());
        self.entries.push(AppEntry {
            name,
            exe,
            path,
            icon: None,
            running,
            source,
        });
    }

    /// Remember where an entry's artwork lives, for sources whose icon is not
    /// the executable. First hint wins, so the source that knows the app best —
    /// which runs first — is the one the icon loader follows.
    pub(crate) fn hint_icon(&mut self, exe_hint: &str, icon: &str) {
        let icon = icon.trim();
        if icon.is_empty() {
            return;
        }
        if let Some(exe) = normalize_exe(exe_hint) {
            self.hints.entry(exe).or_insert_with(|| icon.to_string());
        }
    }

    /// Record a non-fatal source failure. Callers pass a fixed description —
    /// never an application name or a path.
    pub(crate) fn warn(&mut self, what: impl Into<String>) {
        let what = what.into();
        if !self.warnings.contains(&what) {
            self.warnings.push(what);
        }
    }

    /// True once the cap is reached, so a source can stop walking early.
    ///
    /// Stopping here is itself a truncation, but this method does not record
    /// one: the entry count at [`Collector::finish`] says the same thing and
    /// cannot be forgotten by a source that bails out without asking.
    pub(crate) fn is_full(&self) -> bool {
        self.entries.len() >= self.limit
    }

    pub(crate) fn finish(self) -> (AppScan, IconHints) {
        let Collector {
            mut entries,
            hints,
            warnings,
            limit,
            mut truncated,
            ..
        } = self;
        // A scan that ends sitting on its cap is not a complete list. `add`
        // only flags the entries it was handed and refused; a source that sees
        // `is_full` abandons the rest of its walk, so those never reach `add`
        // at all and would otherwise pass for "there was nothing else".
        truncated |= entries.len() >= limit;
        // Running first — those are the apps a user came to the picker for —
        // then alphabetically, case-insensitively, so the list never reshuffles
        // between scans.
        entries.sort_by(|a, b| {
            b.running
                .cmp(&a.running)
                .then_with(|| a.name.to_lowercase().cmp(&b.name.to_lowercase()))
                .then_with(|| a.exe.cmp(&b.exe))
        });
        (
            AppScan {
                apps: entries,
                truncated,
                warnings,
            },
            hints,
        )
    }
}

/// How much a source is trusted for an application's display name. Processes
/// only ever give us a file name, so anything that knows a real title beats them.
fn source_rank(source: &str) -> u8 {
    match source {
        SOURCE_PROCESS => 1,
        SOURCE_STARTMENU => 3,
        SOURCE_REGISTRY | SOURCE_BUNDLE | SOURCE_DESKTOP => 3,
        _ => 0,
    }
}

/// The rule-matching form of an executable reference: file name only, lower
/// case. Accepts either separator so a path from any source normalizes the
/// same, and tolerates the quoting registry values are full of.
pub(crate) fn normalize_exe(hint: &str) -> Option<String> {
    let trimmed = hint.trim().trim_matches('"').trim();
    let name = trimmed
        .rsplit(['\\', '/'])
        .next()
        .unwrap_or(trimmed)
        .trim()
        .trim_matches('"')
        .trim();
    if name.is_empty() || name == "." || name == ".." {
        return None;
    }
    Some(name.to_lowercase())
}

/// The lower-cased file name of `path`, or `None` when there is none.
fn file_name_lower(path: &str) -> Option<String> {
    normalize_exe(path)
}

/// Collapse the whitespace a `.lnk` name or a registry `DisplayName` may carry,
/// and drop control characters that would break the UI's layout.
pub(crate) fn clean_name(raw: &str) -> String {
    let mut out = String::with_capacity(raw.len());
    let mut space = false;
    for ch in raw.trim().chars() {
        if ch.is_whitespace() {
            space = true;
            continue;
        }
        if ch.is_control() {
            continue;
        }
        if space && !out.is_empty() {
            out.push(' ');
        }
        space = false;
        out.push(ch);
    }
    out
}

/// A display name derived from a file name: the stem with its extension
/// dropped, original casing kept. Used when a source knows the binary but not
/// what to call it.
pub(crate) fn pretty_stem(path: &str) -> String {
    let name = path
        .trim()
        .trim_matches('"')
        .rsplit(['\\', '/'])
        .next()
        .unwrap_or("")
        .trim();
    match name.rsplit_once('.') {
        // Only treat the tail as an extension if it looks like one; names such
        // as "Node.js" must survive intact.
        Some((stem, ext)) if !stem.is_empty() && ext.len() <= 4 && is_known_ext(ext) => {
            stem.to_string()
        }
        _ => name.to_string(),
    }
}

fn is_known_ext(ext: &str) -> bool {
    matches!(
        ext.to_lowercase().as_str(),
        "exe" | "com" | "bat" | "cmd" | "app" | "desktop" | "lnk"
    )
}

/// Whether `name` carries no more information than the file name itself.
fn is_placeholder_name(name: &str, exe: &str) -> bool {
    let name = name.trim().to_lowercase();
    name == exe || name == pretty_stem(exe).to_lowercase()
}

/// Installers and uninstallers share folders with the applications they manage
/// and would otherwise show up as pickable apps. They never carry the app's
/// traffic, so a rule naming one is always a mistake.
pub(crate) fn is_installer_exe(exe: &str) -> bool {
    let exe = exe.to_lowercase();
    let stem = exe.strip_suffix(".exe").unwrap_or(&exe);
    if stem.starts_with("unins")
        || stem.starts_with("uninstall")
        || stem == "setup"
        || stem == "install"
        || stem == "installer"
        || stem.ends_with("_uninstall")
        || stem.ends_with("-uninstall")
    {
        return true;
    }
    // The uninstall hive also lists the installer that registered the entry, so
    // redistributables and SDKs arrive named after their own setup file:
    // vc_redist.x64.exe, dotnet-sdk-8.0.423-win-x64.exe, python-3.11.9-amd64.exe.
    // They never carry an application's traffic, and offering them invites a rule
    // that can only ever match nothing.
    //
    // Matched on shape rather than a name list: a suffix ("…setup"), a runtime
    // component ("redist"), or an SDK/runtime bundle whose stem is a versioned
    // build. Deliberately narrow — a wrong guess here hides a real application,
    // which is far worse than one extra row.
    stem.ends_with("setup")
        || stem.ends_with("_setup")
        || stem.ends_with("-setup")
        || stem.contains("redist")
        || stem.starts_with("dotnet-sdk-")
        || stem.starts_with("windowsdesktop-runtime-")
        || stem.starts_with("dotnet-runtime-")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry_named(scan: &AppScan, exe: &str) -> AppEntry {
        scan.apps
            .iter()
            .find(|e| e.exe == exe)
            .unwrap_or_else(|| panic!("no entry for {exe}"))
            .clone()
    }

    #[test]
    fn normalize_exe_takes_the_file_name_in_lower_case() {
        assert_eq!(
            normalize_exe(r"C:\Program Files\Telegram\Telegram.exe").as_deref(),
            Some("telegram.exe")
        );
        assert_eq!(
            normalize_exe("/usr/bin/Firefox").as_deref(),
            Some("firefox")
        );
        assert_eq!(
            normalize_exe("  \"chrome.exe\" ").as_deref(),
            Some("chrome.exe")
        );
        assert_eq!(normalize_exe(""), None);
        assert_eq!(normalize_exe("   "), None);
        assert_eq!(normalize_exe(r"C:\dir\"), None);
    }

    #[test]
    fn clean_name_collapses_whitespace_and_control_chars() {
        assert_eq!(clean_name("  Telegram\t Desktop \n"), "Telegram Desktop");
        assert_eq!(clean_name("A\u{0}B"), "AB");
        assert_eq!(clean_name(""), "");
    }

    #[test]
    fn pretty_stem_drops_only_real_extensions() {
        assert_eq!(pretty_stem(r"C:\x\Telegram.exe"), "Telegram");
        assert_eq!(pretty_stem("/usr/bin/firefox"), "firefox");
        // A dotted product name is not an extension.
        assert_eq!(pretty_stem("Node.js"), "Node.js");
    }

    #[test]
    fn installers_are_recognised() {
        for exe in [
            "unins000.exe",
            "uninstall.exe",
            "setup.exe",
            "app-uninstall.exe",
            // Shapes the uninstall hive actually produced on a real machine.
            "vc_redist.x64.exe",
            "vcredist_x86.exe",
            "dotnet-sdk-8.0.423-win-x64.exe",
            "windowsdesktop-runtime-10.0.6-win-x64.exe",
            "winsdksetup.exe",
            "adguardvpnsetup.exe",
        ] {
            assert!(is_installer_exe(exe), "{exe} should look like an installer");
        }
        // The guard is narrow on purpose: hiding a real application is a worse
        // failure than leaving one installer in the list, so anything that only
        // resembles one has to survive.
        for exe in [
            "telegram.exe",
            "chrome.exe",
            // Ships under a name containing "install" but is the application.
            "installaware.exe",
            // A game launcher — "setup" nowhere near the end of the stem.
            "setupfactoryrunner.exe",
        ] {
            assert!(!is_installer_exe(exe), "{exe} must stay pickable");
        }
    }

    #[test]
    fn duplicate_exe_enriches_instead_of_appending() {
        let mut c = Collector::new(10);
        c.add("telegram.exe", "telegram.exe", None, SOURCE_PROCESS, true);
        c.add(
            "Telegram Desktop",
            r"C:\Program Files\Telegram\Telegram.exe",
            Some(r"C:\Program Files\Telegram\Telegram.exe".into()),
            SOURCE_REGISTRY,
            false,
        );

        let (scan, _) = c.finish();
        assert_eq!(scan.apps.len(), 1);
        let e = &scan.apps[0];
        assert_eq!(e.exe, "telegram.exe");
        // The registry knows the real title; the process sighting keeps the flag.
        assert_eq!(e.name, "Telegram Desktop");
        assert_eq!(e.source, SOURCE_REGISTRY);
        assert!(e.running);
        assert_eq!(
            e.path.as_deref(),
            Some(r"C:\Program Files\Telegram\Telegram.exe")
        );
    }

    #[test]
    fn case_only_differences_are_the_same_app() {
        let mut c = Collector::new(10);
        c.add("Chrome", "Chrome.EXE", None, SOURCE_STARTMENU, false);
        c.add("chrome.exe", "chrome.exe", None, SOURCE_PROCESS, true);
        let (scan, _) = c.finish();
        assert_eq!(scan.apps.len(), 1);
        assert!(scan.apps[0].running);
        assert_eq!(scan.apps[0].exe, "chrome.exe");
    }

    #[test]
    fn a_weaker_source_never_overwrites_a_real_name() {
        let mut c = Collector::new(10);
        c.add(
            "Visual Studio Code",
            "code.exe",
            None,
            SOURCE_STARTMENU,
            false,
        );
        c.add("code.exe", "code.exe", None, SOURCE_PROCESS, true);
        let (scan, _) = c.finish();
        assert_eq!(scan.apps[0].name, "Visual Studio Code");
        assert_eq!(scan.apps[0].source, SOURCE_STARTMENU);
    }

    #[test]
    fn a_peer_source_fills_in_a_placeholder_name() {
        let mut c = Collector::new(10);
        // A desktop entry whose Name= was missing fell back to the file name.
        c.add("firefox", "firefox", None, SOURCE_DESKTOP, false);
        c.add("Mozilla Firefox", "firefox", None, SOURCE_DESKTOP, false);
        let (scan, _) = c.finish();
        assert_eq!(scan.apps[0].name, "Mozilla Firefox");
    }

    #[test]
    fn entries_without_a_file_name_are_dropped() {
        let mut c = Collector::new(10);
        c.add("Nothing", "   ", None, SOURCE_REGISTRY, false);
        c.add("Nothing", "", None, SOURCE_REGISTRY, false);
        assert!(c.finish().0.apps.is_empty());
    }

    #[test]
    fn icon_hints_key_on_the_normalized_exe_and_keep_the_first() {
        let mut c = Collector::new(10);
        c.add("Firefox", "/usr/bin/firefox", None, SOURCE_DESKTOP, false);
        c.hint_icon("/usr/bin/Firefox", "firefox");
        c.hint_icon("firefox", "firefox-esr");
        c.hint_icon("firefox", "   ");

        let (_, hints) = c.finish();
        assert_eq!(hints.get("firefox").map(String::as_str), Some("firefox"));
    }

    #[test]
    fn the_entry_cap_truncates_and_says_so() {
        let mut c = Collector::new(2);
        c.add("A", "a.exe", None, SOURCE_REGISTRY, false);
        c.add("B", "b.exe", None, SOURCE_REGISTRY, false);
        assert!(c.is_full());
        c.add("C", "c.exe", None, SOURCE_REGISTRY, false);
        // A repeat of an existing entry still merges once full.
        c.add("A running", "a.exe", None, SOURCE_PROCESS, true);

        let (scan, _) = c.finish();
        assert_eq!(scan.apps.len(), 2);
        assert!(scan.truncated);
        assert!(entry_named(&scan, "a.exe").running);
        assert!(!scan.apps.iter().any(|e| e.exe == "c.exe"));
    }

    #[test]
    fn a_walk_abandoned_at_the_cap_still_reports_truncation() {
        // A source that asks `is_full` stops before it offers the entry that
        // would not have fitted, so `add` never learns anything was dropped.
        // Ending on the cap is the only evidence left, and it has to count —
        // otherwise a capped list is served as if it were the whole machine.
        let mut c = Collector::new(2);
        c.add("A", "a.exe", None, SOURCE_REGISTRY, false);
        c.add("B", "b.exe", None, SOURCE_REGISTRY, false);
        assert!(c.is_full());

        let (scan, _) = c.finish();
        assert_eq!(scan.apps.len(), 2);
        assert!(scan.truncated);
    }

    #[test]
    fn running_entries_sort_first_then_by_name() {
        let mut c = Collector::new(10);
        c.add("Zed", "zed.exe", None, SOURCE_REGISTRY, false);
        c.add("Alpha", "alpha.exe", None, SOURCE_REGISTRY, false);
        c.add("Middle", "middle.exe", None, SOURCE_PROCESS, true);
        let (scan, _) = c.finish();
        let order: Vec<&str> = scan.apps.iter().map(|e| e.exe.as_str()).collect();
        assert_eq!(order, ["middle.exe", "alpha.exe", "zed.exe"]);
    }

    #[test]
    fn warnings_are_recorded_once_each() {
        let mut c = Collector::new(10);
        c.warn("registry: unreadable");
        c.warn("registry: unreadable");
        c.warn("start menu: unreadable");
        let (scan, _) = c.finish();
        assert_eq!(scan.warnings.len(), 2);
    }

    #[test]
    fn an_exhausted_budget_is_reported_as_truncation() {
        // A budget that is already spent: every source bails immediately, and
        // the scan must still return a well-formed, flagged result.
        let scan = scan(Budget::spent(), MAX_ENTRIES);
        assert!(scan.truncated);
        assert!(scan
            .warnings
            .iter()
            .any(|w| w.contains("time budget exhausted")));
    }

    #[test]
    fn a_scan_never_exceeds_its_entry_cap() {
        let scan = scan(Budget::new(Duration::from_millis(1500)), 5);
        assert!(scan.apps.len() <= 5);
    }

    #[test]
    fn siblings_of_a_missing_folder_are_empty() {
        assert!(siblings_of(r"C:\definitely\not\here\app.exe", 8).is_empty());
        assert!(siblings_of("", 8).is_empty());
    }

    #[test]
    fn siblings_exclude_the_app_itself_and_its_uninstaller() {
        let dir = std::env::temp_dir().join(format!("tenebra-apps-sib-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        for f in ["main.exe", "helper.exe", "unins000.exe", "readme.txt"] {
            std::fs::write(dir.join(f), b"x").unwrap();
        }

        let main = dir.join("main.exe").to_string_lossy().to_string();
        let found: Vec<String> = siblings_of(&main, 8).into_iter().map(|e| e.exe).collect();
        std::fs::remove_dir_all(&dir).unwrap();

        assert!(found.contains(&"helper.exe".to_string()));
        assert!(!found.contains(&"main.exe".to_string()));
        assert!(!found.contains(&"unins000.exe".to_string()));
        if cfg!(windows) {
            assert!(!found.contains(&"readme.txt".to_string()));
        }
    }
}
