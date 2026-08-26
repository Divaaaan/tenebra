//! Local, opt-in crash diagnostics — the Mullvad model: everything stays on the
//! machine and nothing is ever sent without an explicit click.
//!
//! A panic in the GUI process (the Rust [`install_panic_hook`]) and an uncaught
//! error in the webview ([`record_web_crash`]) are both appended to a single
//! local file, `crash-gui.txt`, next to the core log in the Tenebra data
//! directory. The file is written unconditionally — it is local diagnostics, not
//! a report — but it is never transmitted. When the user has opted in, the front
//! end offers, after the fact, to review the file ([`check_crash_report`]) and to
//! open a pre-filled GitHub issue in their browser ([`open_report_url`]). There
//! is no network path here and no telemetry: the whole flow is local file I/O
//! plus, on an explicit click, handing one fixed-host URL to the OS browser.
//!
//! The URL is opened with the `open` crate straight from Rust rather than through
//! a webview capability: the window intentionally has no `shell:open` permission
//! (untrusted subscription names must never reach the OS opener), so the webview
//! cannot open URLs at all and cannot influence the destination host — Rust builds
//! the entire URL from a fixed constant plus a title derived from the local file.
//!
//! The same opener also serves the "report a problem" flow ([`open_problem_url`]),
//! which has no crash behind it: there the title carries only the version and OS
//! this process reads for itself. That report travels on the clipboard, so the
//! rule above holds unchanged — nothing the user typed or pasted enters a URL.

use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use url::Url;

/// File name of the GUI crash log, written alongside the core's `core.log` in the
/// Tenebra data directory.
const CRASH_FILE: &str = "crash-gui.txt";

/// Entry kinds, recorded verbatim in the file so a reader can tell a native GUI
/// panic apart from an uncaught webview error.
const KIND_PANIC: &str = "gui-panic";
const KIND_WEB: &str = "webview-error";

/// Field caps so a runaway payload (a huge panic message or stack) can neither
/// bloat the file nor blow past a browser's URL limit downstream.
const MAX_MESSAGE: usize = 500;
const MAX_DETAIL: usize = 4000;

/// New-issue endpoint for the Tenebra repository. A hard-coded constant: the
/// webview never supplies a URL, so the destination host can't be redirected.
const ISSUE_BASE: &str = "https://github.com/Divaaaan/tenebra/issues/new";

/// The Tenebra data directory (`%LOCALAPPDATA%\Tenebra` on Windows,
/// `$XDG_DATA_HOME/Tenebra` on Linux, the temp dir elsewhere), created if
/// missing. Pure `std::env` so it works from a panic hook set before Tauri
/// starts, and shared with the sidecar's `core.log` path so both files land in
/// the same, one-place-to-share directory.
///
/// Linux gets its own branch rather than the temp-dir fallback, and not only
/// for tidiness: `/tmp` is world-writable and shared between users, so a
/// pre-created `Tenebra/crash-gui.txt` symlink there would redirect this
/// append-only writer at a file of somebody else's choosing. A per-user data
/// directory is not shared, and survives a reboot besides.
pub fn data_dir() -> Option<PathBuf> {
    let base = std::env::var_os("LOCALAPPDATA")
        .map(PathBuf::from)
        .or_else(linux_data_home)
        .unwrap_or_else(std::env::temp_dir);
    let dir = base.join("Tenebra");
    std::fs::create_dir_all(&dir).ok()?;
    Some(dir)
}

/// The XDG base directory for per-user data, as `$XDG_DATA_HOME` or the
/// `$HOME/.local/share` the spec defaults it to. `None` when neither is set (a
/// service-like environment with no home), leaving the temp-dir fallback.
/// Absent off Linux, where the platform has its own convention.
fn linux_data_home() -> Option<PathBuf> {
    #[cfg(target_os = "linux")]
    {
        std::env::var_os("XDG_DATA_HOME")
            .map(PathBuf::from)
            .filter(|p| p.is_absolute())
            .or_else(|| {
                std::env::var_os("HOME").map(|home| PathBuf::from(home).join(".local/share"))
            })
    }
    #[cfg(not(target_os = "linux"))]
    None
}

fn crash_file() -> Option<PathBuf> {
    Some(data_dir()?.join(CRASH_FILE))
}

/// The running app version, from Cargo at build time.
fn version() -> &'static str {
    env!("CARGO_PKG_VERSION")
}

/// OS and architecture, e.g. `windows x86_64`.
fn os_arch() -> String {
    format!("{} {}", std::env::consts::OS, std::env::consts::ARCH)
}

fn unix_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_secs())
}

/// The panic report the front end shows and can copy, plus a cheap change
/// signature (file size + mtime) the UI stores once the user dismisses it, so the
/// same crash isn't offered again next launch but a newer one (different
/// signature) is.
#[derive(serde::Serialize)]
pub struct CrashReport {
    text: String,
    signature: String,
}

/// Turn a panic payload into a readable message. Panics carry either a
/// `&'static str` (`panic!("literal")`) or a `String` (`panic!("{}", x)`); other
/// payloads are rare and fall back to a placeholder.
fn payload_str(payload: &(dyn std::any::Any + Send)) -> String {
    if let Some(s) = payload.downcast_ref::<&str>() {
        (*s).to_string()
    } else if let Some(s) = payload.downcast_ref::<String>() {
        s.clone()
    } else {
        "unknown panic payload".to_string()
    }
}

/// Collapse a value to a single physical line and cap its length, so each field
/// stays one greppable line in the file.
fn one_line(s: &str, max: usize) -> String {
    truncate_chars(&s.replace(['\n', '\r'], " "), max)
}

/// Cap a string to `max` characters (not bytes, so it never splits a codepoint),
/// marking a cut with an ellipsis.
fn truncate_chars(s: &str, max: usize) -> String {
    if s.chars().count() <= max {
        s.to_string()
    } else {
        let head: String = s.chars().take(max).collect();
        format!("{head}…")
    }
}

/// Append one crash entry to `dir/crash-gui.txt`. Best-effort and non-panicking
/// (a crash reporter must never crash): any I/O error is swallowed. Append-only,
/// like the core log, so a crash-restart loop never wipes the tail that explains
/// the crash.
fn append_entry(
    dir: &Path,
    kind: &str,
    message: &str,
    location: Option<&str>,
    detail: Option<&str>,
) {
    use std::fmt::Write as _;
    use std::io::Write as _;

    let mut entry = String::from("\n--- tenebra gui crash ---\n");
    let _ = writeln!(entry, "ts: {}", unix_secs());
    let _ = writeln!(entry, "version: {}", version());
    let _ = writeln!(entry, "os: {}", os_arch());
    let _ = writeln!(entry, "kind: {kind}");
    if let Some(loc) = location {
        let _ = writeln!(entry, "location: {loc}");
    }
    let _ = writeln!(entry, "message: {}", one_line(message, MAX_MESSAGE));
    if let Some(d) = detail {
        let _ = writeln!(entry, "detail: {}", truncate_chars(d, MAX_DETAIL));
    }

    if let Ok(mut f) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(dir.join(CRASH_FILE))
    {
        let _ = f.write_all(entry.as_bytes());
    }
}

/// Read the crash file into a [`CrashReport`], or `None` when it is absent or
/// blank (the common, healthy case). Split from the command so it can be tested
/// against a path under a temp dir.
fn read_report(path: &Path) -> Option<CrashReport> {
    let meta = std::fs::metadata(path).ok()?;
    let text = std::fs::read_to_string(path).ok()?;
    if text.trim().is_empty() {
        return None;
    }
    Some(CrashReport {
        text,
        signature: signature(&meta),
    })
}

/// A cheap file-change signature: size plus mtime seconds. Enough for the UI to
/// tell a dismissed crash from a fresh one without hashing the whole file.
fn signature(meta: &std::fs::Metadata) -> String {
    let mtime = meta
        .modified()
        .ok()
        .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
        .map_or(0, |d| d.as_secs());
    format!("{}-{mtime}", meta.len())
}

/// The value of the most recent `message:` line in the file — the latest crash's
/// summary, used to prefill the issue title. Empty when there is none.
fn latest_message(report: &str) -> String {
    report
        .lines()
        .filter_map(|l| l.strip_prefix("message: "))
        .next_back()
        .unwrap_or("")
        .to_string()
}

/// Placeholder dropped into the issue form's log field, telling the reporter
/// where the report they just copied goes. English, like the rest of the issue
/// template and the bundle it will be pasted next to — this is text a maintainer
/// reads on GitHub, not app chrome, and the webview supplies none of it.
const PASTE_HINT: &str = "Paste the report Tenebra copied to your clipboard here.";

/// Build the pre-filled new-issue URL from a fixed base plus an encoded title.
/// Only the title varies, and it is derived from the local crash file (our own
/// version, OS and panic summary) — never from webview input — and percent-encoded
/// via the `url` crate, so the result is always a well-formed URL on the pinned
/// host. The full report is pasted by the user from the Copy button, keeping the
/// URL short.
fn build_issue_url(version: &str, os: &str, summary: &str) -> String {
    let title = if summary.is_empty() {
        format!("Crash report (v{version}, {os})")
    } else {
        format!("Crash: {} (v{version}, {os})", truncate_chars(summary, 120))
    };
    let mut url = Url::parse(ISSUE_BASE).expect("issue base URL is a valid constant");
    url.query_pairs_mut()
        .append_pair("template", "bug_report.yml")
        .append_pair("title", &title);
    url.to_string()
}

/// Build the new-issue URL for a problem the user is reporting by hand — the
/// ordinary failure with no crash behind it, so there is no local file to
/// summarise.
///
/// Same discipline as [`build_issue_url`], and for the same reason: every part
/// of the URL comes from this process (the `Cargo.toml` version and
/// `std::env::consts`) and none of it from the webview, so no subscription name
/// or pasted string can steer the destination. Only the metadata the shell
/// already knows travels here; the report itself goes on the clipboard, being
/// far past any browser's URL limit.
fn build_problem_url(version: &str, os: &str) -> String {
    let mut url = Url::parse(ISSUE_BASE).expect("issue base URL is a valid constant");
    url.query_pairs_mut()
        .append_pair("template", "bug_report.yml")
        .append_pair("title", &format!("Problem report (v{version}, {os})"))
        .append_pair("version", version)
        .append_pair("logs", PASTE_HINT);
    url.to_string()
}

/// Install the process panic hook. It appends the panic (message + location) to
/// the crash file, then chains the previous hook so the default stderr backtrace
/// still fires. Because the release profile is `panic = "abort"`, the hook is the
/// only chance to persist a panic before the process dies — so it writes
/// synchronously and never itself unwinds. Call it once, before Tauri starts.
pub fn install_panic_hook() {
    let prev = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        if let Some(dir) = data_dir() {
            let message = payload_str(info.payload());
            let location = info
                .location()
                .map(|l| format!("{}:{}", l.file(), l.line()));
            append_entry(&dir, KIND_PANIC, &message, location.as_deref(), None);
        }
        prev(info);
    }));
}

/// Read the local crash file for the front end. `None` (absent/blank) is the
/// healthy case. Never touches the network.
#[tauri::command]
pub fn check_crash_report() -> Option<CrashReport> {
    read_report(&crash_file()?)
}

/// Append an uncaught webview error to the crash file. The webview cannot write
/// files (CSP/capability), so its global error handler hands the message and a
/// short stack excerpt here. Best-effort and bounded, like the panic hook.
#[tauri::command(rename_all = "snake_case")]
pub fn record_web_crash(message: String, stack_excerpt: String) {
    if let Some(dir) = data_dir() {
        let detail = (!stack_excerpt.is_empty()).then_some(stack_excerpt.as_str());
        append_entry(&dir, KIND_WEB, &message, None, detail);
    }
}

/// Open a pre-filled GitHub issue for the recorded crash in the user's default
/// browser. The URL host is fixed (the Tenebra repo); only a short title derived
/// from the local file is added. Opened via the `open` crate from Rust so no
/// webview `shell:open` capability is involved.
#[tauri::command]
pub fn open_report_url() -> Result<(), String> {
    let summary = crash_file()
        .and_then(|p| std::fs::read_to_string(p).ok())
        .map(|t| latest_message(&t))
        .unwrap_or_default();
    let url = build_issue_url(version(), &os_arch(), &summary);
    open::that_detached(&url).map_err(|e| format!("could not open the browser: {e}"))
}

/// Open the new-issue form for a hand-written problem report. Reachable from the
/// app at any time — no crash file, no consent gate — because the failures worth
/// hearing about mostly aren't crashes.
///
/// Called only from the front end's own "open the issue form" button, never as a
/// side effect of assembling a report: building one has to stay a local act.
#[tauri::command]
pub fn open_problem_url() -> Result<(), String> {
    let url = build_problem_url(version(), &os_arch());
    open::that_detached(&url).map_err(|e| format!("could not open the browser: {e}"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    /// A unique scratch dir under the temp dir, removed on drop — the same
    /// dependency-free pattern the sidecar tests use.
    struct TempDir(PathBuf);

    impl TempDir {
        fn new(tag: &str) -> Self {
            use std::sync::atomic::{AtomicU32, Ordering};
            static SEQ: AtomicU32 = AtomicU32::new(0);
            let n = SEQ.fetch_add(1, Ordering::Relaxed);
            let dir = std::env::temp_dir().join(format!(
                "tenebra-crash-test-{}-{tag}-{n}",
                std::process::id()
            ));
            std::fs::create_dir_all(&dir).unwrap();
            TempDir(dir)
        }
    }

    impl Drop for TempDir {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }

    #[test]
    fn append_entry_writes_a_readable_panic_record() {
        let dir = TempDir::new("panic");
        append_entry(
            &dir.0,
            KIND_PANIC,
            "boom happened",
            Some("src/x.rs:42"),
            None,
        );

        let text = std::fs::read_to_string(dir.0.join(CRASH_FILE)).unwrap();
        assert!(text.contains("kind: gui-panic"), "{text}");
        assert!(text.contains("message: boom happened"), "{text}");
        assert!(text.contains("location: src/x.rs:42"), "{text}");
        assert!(text.contains(&format!("version: {}", version())), "{text}");
    }

    #[test]
    fn append_entry_is_append_only_across_calls() {
        let dir = TempDir::new("append");
        append_entry(&dir.0, KIND_PANIC, "first", None, None);
        append_entry(&dir.0, KIND_WEB, "second", None, Some("stack line"));

        let text = std::fs::read_to_string(dir.0.join(CRASH_FILE)).unwrap();
        assert!(text.contains("message: first"), "{text}");
        assert!(text.contains("message: second"), "{text}");
        assert!(text.contains("detail: stack line"), "{text}");
        assert_eq!(
            text.matches("--- tenebra gui crash ---").count(),
            2,
            "{text}"
        );
    }

    #[test]
    fn message_is_collapsed_to_one_line() {
        let dir = TempDir::new("oneline");
        append_entry(&dir.0, KIND_WEB, "line one\nline two", None, None);

        let text = std::fs::read_to_string(dir.0.join(CRASH_FILE)).unwrap();
        let msg = text
            .lines()
            .find(|l| l.starts_with("message: "))
            .expect("a message line");
        assert_eq!(msg, "message: line one line two");
    }

    #[test]
    fn payload_str_reads_str_and_string_payloads() {
        let s: &str = "static message";
        assert_eq!(payload_str(&s), "static message");
        let owned = String::from("owned message");
        assert_eq!(payload_str(&owned), "owned message");
    }

    #[test]
    fn read_report_is_none_when_absent_or_blank() {
        let dir = TempDir::new("empty");
        assert!(read_report(&dir.0.join(CRASH_FILE)).is_none());
        std::fs::write(dir.0.join(CRASH_FILE), "   \n").unwrap();
        assert!(read_report(&dir.0.join(CRASH_FILE)).is_none());
    }

    #[test]
    fn read_report_returns_text_and_signature() {
        let dir = TempDir::new("present");
        append_entry(&dir.0, KIND_PANIC, "boom", None, None);

        let report = read_report(&dir.0.join(CRASH_FILE)).expect("a report");
        assert!(report.text.contains("boom"));
        assert!(!report.signature.is_empty());
    }

    #[test]
    fn latest_message_picks_the_most_recent_entry() {
        let report = "kind: gui-panic\nmessage: older\n\nkind: webview-error\nmessage: newer\n";
        assert_eq!(latest_message(report), "newer");
        assert_eq!(latest_message("nothing here"), "");
    }

    #[test]
    fn build_issue_url_pins_the_repo_and_encodes_the_title() {
        let url = build_issue_url("0.3.7", "windows x86_64", "index out of bounds");
        assert!(
            url.starts_with("https://github.com/Divaaaan/tenebra/issues/new?"),
            "{url}"
        );
        assert!(url.contains("template=bug_report.yml"), "{url}");
        // The title is percent-encoded, never raw (a raw space would be invalid).
        assert!(!url.contains("index out of bounds"), "{url}");
        assert!(url.contains("Crash"), "{url}");
    }

    #[test]
    fn build_issue_url_handles_an_empty_summary() {
        let url = build_issue_url("0.3.7", "windows x86_64", "");
        assert!(
            url.contains("Crash+report") || url.contains("Crash%20report"),
            "{url}"
        );
    }

    #[test]
    fn build_problem_url_carries_the_version_and_the_os() {
        let url = build_problem_url("0.5.5", "windows x86_64");
        assert!(url.contains("0.5.5"), "{url}");
        assert!(url.contains("windows"), "{url}");
        assert!(url.contains("x86_64"), "{url}");
        assert!(url.contains("template=bug_report.yml"), "{url}");
    }

    /// The whole URL is built here from constants and `std::env`; the webview
    /// supplies nothing. Whatever the inputs, it has to stay on the repo.
    #[test]
    fn build_problem_url_never_leaves_the_pinned_host() {
        for (version, os) in [
            ("0.5.5", "windows x86_64"),
            ("", ""),
            ("https://evil.example/?x=", "//evil.example"),
            ("0.5.5 ?&#/\\", "linux aarch64"),
        ] {
            let url = build_problem_url(version, os);
            let parsed = Url::parse(&url).expect("a well-formed URL");
            assert_eq!(parsed.host_str(), Some("github.com"), "{url}");
            assert_eq!(parsed.path(), "/Divaaaan/tenebra/issues/new", "{url}");
            assert_eq!(parsed.scheme(), "https", "{url}");
        }
    }

    /// The report itself travels on the clipboard, not in the query string: a
    /// bundle is far past any browser's URL limit. The form should still say
    /// where it goes.
    #[test]
    fn build_problem_url_asks_for_the_report_to_be_pasted() {
        let url = build_problem_url("0.5.5", "windows x86_64");
        let parsed = Url::parse(&url).expect("a well-formed URL");
        let logs = parsed
            .query_pairs()
            .find(|(k, _)| k == "logs")
            .map(|(_, v)| v.into_owned())
            .expect("a logs field");
        assert!(logs.to_lowercase().contains("paste"), "{logs}");
        assert!(url.len() < 1000, "URL should stay short: {}", url.len());
    }
}
