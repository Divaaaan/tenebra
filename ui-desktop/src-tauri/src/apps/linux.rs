//! Linux application source: freedesktop `.desktop` entries plus live processes.
//!
//! The whole module compiles on every host — only [`collect`] is dispatched on
//! Linux. Its parsers take their roots as arguments so they can be exercised
//! against fixture trees anywhere, which is the only way to test them without a
//! live desktop session.
//!
//! The hard part is `Exec=`. It is a command line, not a binary: it carries
//! field codes the launcher substitutes (`%U`, `%f`), environment prefixes, and
//! sandbox wrappers. A split rule matches the *binary's* name, so anything left
//! unstripped becomes a rule that silently matches nothing.

use std::collections::HashSet;
use std::path::{Path, PathBuf};

use super::{icons, Budget, Collector, SOURCE_DESKTOP, SOURCE_PROCESS};

/// Icon sizes we will accept from a theme, largest usable first. Everything
/// here is within [`icons::MAX_EDGE`], so a hit can be embedded as found
/// instead of decoded and resampled.
const ICON_SIZES: [&str; 5] = ["48x48", "32x32", "24x24", "22x22", "16x16"];

/// Subdirectories of a theme size that hold application icons.
const ICON_CATEGORIES: [&str; 2] = ["apps", "applications"];

/// How deep to walk an `applications` directory. Desktop entries may sit in
/// subdirectories, but nothing legitimate is buried deeper than this.
const MAX_DEPTH: usize = 3;

/// A parsed `[Desktop Entry]` group. Only the keys that decide whether an entry
/// is a pickable application, and which binary it launches.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub(crate) struct DesktopEntry {
    pub kind: Option<String>,
    pub name: Option<String>,
    pub exec: Option<String>,
    pub try_exec: Option<String>,
    pub icon: Option<String>,
    pub no_display: bool,
    pub hidden: bool,
}

impl DesktopEntry {
    /// Whether this entry belongs in a picker: a real application the desktop
    /// itself would show.
    pub(crate) fn is_visible_application(&self) -> bool {
        !self.no_display
            && !self.hidden
            && self
                .kind
                .as_deref()
                .map(|t| t == "Application")
                .unwrap_or(false)
    }
}

/// What an `Exec=` line ultimately starts.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum ExecTarget {
    /// A binary named directly on the command line; the string is its path or
    /// bare name as written.
    Binary(String),
    /// A `flatpak run` invocation. The process the kernel — and therefore
    /// sing-box — sees is the sandboxed binary, which the command line only
    /// names when `--command=` is given. Otherwise the app's own metadata has
    /// to be consulted; see [`flatpak_command`].
    Flatpak {
        app_id: String,
        command: Option<String>,
    },
}

/// Parse the `[Desktop Entry]` group of a desktop file.
///
/// Returns `None` when the file has no such group, which is what makes it not a
/// desktop entry at all. Later groups (`[Desktop Action …]`) are ignored: their
/// `Exec=` lines launch alternate actions, not the application.
pub(crate) fn parse_desktop_entry(text: &str) -> Option<DesktopEntry> {
    let mut entry = DesktopEntry::default();
    let mut in_group = false;
    let mut seen_group = false;

    for line in text.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if let Some(group) = line.strip_prefix('[').and_then(|l| l.strip_suffix(']')) {
            in_group = group.trim() == "Desktop Entry";
            seen_group |= in_group;
            continue;
        }
        if !in_group {
            continue;
        }
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        let key = key.trim();
        // Localized keys ("Name[ru]") are skipped: the picker must show the same
        // text regardless of the locale the desktop file happens to carry.
        if key.contains('[') {
            continue;
        }
        let value = unescape(value.trim());
        match key {
            "Type" => entry.kind = Some(value),
            "Name" => entry.name = Some(value),
            "Exec" => entry.exec = Some(value),
            "TryExec" => entry.try_exec = Some(value),
            "Icon" => entry.icon = Some(value),
            "NoDisplay" => entry.no_display = value.eq_ignore_ascii_case("true"),
            "Hidden" => entry.hidden = value.eq_ignore_ascii_case("true"),
            _ => {}
        }
    }

    seen_group.then_some(entry)
}

/// Resolve an `Exec=` value to what it actually launches.
///
/// Strips field codes, leading environment assignments, `env` and `sh -c`
/// wrappers, and recognises `flatpak run`. Returns `None` when nothing
/// executable is left — deliberately, because an entry with no binary is better
/// dropped than turned into a rule that matches the wrong process.
pub(crate) fn exec_target(exec: &str) -> Option<ExecTarget> {
    exec_target_depth(exec, 0)
}

fn exec_target_depth(exec: &str, depth: usize) -> Option<ExecTarget> {
    if depth > 2 {
        return None;
    }
    let tokens = tokenize(exec);
    let mut i = 0usize;

    loop {
        let token = tokens.get(i)?.as_str();

        // A leading VAR=value prefix, with or without a preceding `env`.
        if is_env_assignment(token) {
            i += 1;
            continue;
        }

        match base_name(token) {
            // `env [-i] [-u NAME] [VAR=value ...] command ...`
            "env" => {
                i += 1;
                while let Some(next) = tokens.get(i) {
                    if next == "-i" || next == "--ignore-environment" {
                        i += 1;
                    } else if next == "-u" || next == "--unset" {
                        i += 2;
                    } else if is_env_assignment(next) {
                        i += 1;
                    } else {
                        break;
                    }
                }
                continue;
            }
            // A shell builtin, not a program: whatever follows is the binary.
            "exec" => {
                i += 1;
                continue;
            }
            // `sh -c "real command ..."` — the payload is another command line.
            "sh" | "bash" | "dash" | "zsh" => {
                if tokens.get(i + 1).map(String::as_str) == Some("-c") {
                    let inner = tokens.get(i + 2)?;
                    return exec_target_depth(inner, depth + 1);
                }
                return Some(ExecTarget::Binary(token.to_string()));
            }
            "flatpak" => return flatpak_target(&tokens[i..]),
            _ => return Some(ExecTarget::Binary(token.to_string())),
        }
    }
}

/// Pull the app id and any explicit `--command=` out of a `flatpak run …` line.
fn flatpak_target(tokens: &[String]) -> Option<ExecTarget> {
    let mut rest = tokens.iter().skip(1);
    // Skip global options until the `run` subcommand.
    for token in rest.by_ref() {
        if token == "run" {
            break;
        }
        if !token.starts_with('-') {
            // Some other flatpak subcommand — not an application launch.
            return None;
        }
    }

    let mut command = None;
    let mut app_id = None;
    for token in rest {
        if let Some(value) = token.strip_prefix("--command=") {
            command = Some(base_name(value).to_string());
        } else if !token.starts_with('-') && app_id.is_none() {
            app_id = Some(token.clone());
        }
    }

    app_id.map(|app_id| ExecTarget::Flatpak { app_id, command })
}

/// The binary a flatpak app runs inside its sandbox, from the app's own
/// `metadata` file. This is the only place the real process name is written
/// down; the exported desktop entry usually omits it.
pub(crate) fn flatpak_command(app_id: &str, roots: &[PathBuf]) -> Option<String> {
    for root in roots {
        let metadata = root.join(app_id).join("current/active/metadata");
        let Ok(text) = std::fs::read_to_string(&metadata) else {
            continue;
        };
        if let Some(command) = parse_flatpak_metadata(&text) {
            return Some(command);
        }
    }
    None
}

/// `command=` from the `[Application]` group of a flatpak `metadata` file.
pub(crate) fn parse_flatpak_metadata(text: &str) -> Option<String> {
    let mut in_app = false;
    for line in text.lines() {
        let line = line.trim();
        if let Some(group) = line.strip_prefix('[').and_then(|l| l.strip_suffix(']')) {
            in_app = group.trim() == "Application";
            continue;
        }
        if !in_app {
            continue;
        }
        if let Some(value) = line.strip_prefix("command=") {
            let value = base_name(value.trim());
            if !value.is_empty() {
                return Some(value.to_string());
            }
        }
    }
    None
}

/// Split a desktop-entry command line into arguments.
///
/// Follows the spec's quoting (double quotes, backslash escapes inside them)
/// and drops field codes: tokens that are only field codes disappear, and a
/// field code glued onto the end of an argument is trimmed off.
fn tokenize(exec: &str) -> Vec<String> {
    let mut tokens = Vec::new();
    let mut current = String::new();
    let mut started = false;
    let mut quoted = false;
    let mut chars = exec.chars().peekable();

    while let Some(ch) = chars.next() {
        match ch {
            '"' => {
                quoted = !quoted;
                started = true;
            }
            '\\' if quoted => {
                // Inside quotes a backslash escapes the next character.
                if let Some(next) = chars.next() {
                    current.push(next);
                    started = true;
                }
            }
            '%' => match chars.peek() {
                Some('%') => {
                    chars.next();
                    current.push('%');
                    started = true;
                }
                Some(code) if is_field_code(*code) => {
                    chars.next();
                }
                _ => {
                    current.push('%');
                    started = true;
                }
            },
            c if c.is_whitespace() && !quoted => {
                if started {
                    tokens.push(std::mem::take(&mut current));
                }
                started = false;
                current.clear();
            }
            c => {
                current.push(c);
                started = true;
            }
        }
    }
    if started {
        tokens.push(current);
    }
    tokens.retain(|t| !t.is_empty());
    tokens
}

fn is_field_code(c: char) -> bool {
    matches!(
        c,
        'f' | 'F' | 'u' | 'U' | 'd' | 'D' | 'n' | 'N' | 'i' | 'c' | 'k' | 'v' | 'm'
    )
}

fn is_env_assignment(token: &str) -> bool {
    match token.split_once('=') {
        Some((name, _)) => {
            !name.is_empty()
                && !name.starts_with('-')
                && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
        }
        None => false,
    }
}

fn base_name(path: &str) -> &str {
    path.rsplit('/').next().unwrap_or(path)
}

/// Apply the spec's value escapes (`\s`, `\n`, `\t`, `\r`, `\\`).
fn unescape(value: &str) -> String {
    if !value.contains('\\') {
        return value.to_string();
    }
    let mut out = String::with_capacity(value.len());
    let mut chars = value.chars();
    while let Some(ch) = chars.next() {
        if ch != '\\' {
            out.push(ch);
            continue;
        }
        match chars.next() {
            Some('s') => out.push(' '),
            Some('n') => out.push('\n'),
            Some('t') => out.push('\t'),
            Some('r') => out.push('\r'),
            Some('\\') => out.push('\\'),
            Some(other) => {
                out.push('\\');
                out.push(other);
            }
            None => out.push('\\'),
        }
    }
    out
}

/// Walk `roots` for desktop entries and feed them to `collector`.
///
/// A root that cannot be read is skipped silently — on a given desktop most of
/// the standard roots simply do not exist, and reporting each as a failure
/// would bury the warnings that mean something.
pub(crate) fn collect_desktop_entries(
    collector: &mut Collector,
    budget: &Budget,
    roots: &[PathBuf],
    flatpak_roots: &[PathBuf],
) {
    let mut seen = HashSet::new();
    for root in roots {
        if budget.expired() || collector.is_full() {
            return;
        }
        walk_desktop_dir(collector, budget, root, flatpak_roots, 0, &mut seen);
    }
}

fn walk_desktop_dir(
    collector: &mut Collector,
    budget: &Budget,
    dir: &Path,
    flatpak_roots: &[PathBuf],
    depth: usize,
    seen: &mut HashSet<PathBuf>,
) {
    if depth > MAX_DEPTH {
        return;
    }
    let Ok(read) = std::fs::read_dir(dir) else {
        return;
    };
    for item in read.flatten() {
        if budget.expired() || collector.is_full() {
            return;
        }
        let path = item.path();
        if path.is_dir() {
            walk_desktop_dir(collector, budget, &path, flatpak_roots, depth + 1, seen);
            continue;
        }
        if path.extension().map(|e| e != "desktop").unwrap_or(true) {
            continue;
        }
        // Desktop-file ids are unique across roots by name, and earlier roots
        // (the user's own) take precedence over later ones.
        let Some(id) = path.file_name().map(PathBuf::from) else {
            continue;
        };
        if !seen.insert(id) {
            continue;
        }
        let Ok(text) = std::fs::read_to_string(&path) else {
            continue;
        };
        add_desktop_entry(collector, &text, &path, flatpak_roots);
    }
}

/// Turn one desktop file's text into an entry, resolving its binary.
fn add_desktop_entry(
    collector: &mut Collector,
    text: &str,
    path: &Path,
    flatpak_roots: &[PathBuf],
) {
    let Some(entry) = parse_desktop_entry(text) else {
        return;
    };
    if !entry.is_visible_application() {
        return;
    }

    let target = entry.exec.as_deref().and_then(exec_target);
    let (exe_hint, full_path) = match target {
        Some(ExecTarget::Binary(bin)) => {
            let full = bin.starts_with('/').then(|| bin.clone());
            (bin, full)
        }
        Some(ExecTarget::Flatpak { app_id, command }) => {
            // Without a command we would have to guess from the app id, and a
            // guessed executable name is a rule that matches the wrong process
            // (or nothing). Leave it out; if the app is running, the process
            // source below picks up its real name.
            let Some(command) = command.or_else(|| flatpak_command(&app_id, flatpak_roots)) else {
                return;
            };
            (command, None)
        }
        // TryExec names the binary the launcher probes for, which is a fine
        // last resort when Exec is missing or unusable.
        None => match entry.try_exec.as_deref() {
            Some(try_exec) if !try_exec.trim().is_empty() => {
                let bin = try_exec.trim().to_string();
                let full = bin.starts_with('/').then(|| bin.clone());
                (bin, full)
            }
            _ => return,
        },
    };

    let name = entry
        .name
        .clone()
        .unwrap_or_else(|| super::pretty_stem(&path.to_string_lossy()));
    collector.add(&name, &exe_hint, full_path, SOURCE_DESKTOP, false);
    if let Some(icon) = entry.icon.as_deref() {
        collector.hint_icon(&exe_hint, icon);
    }
}

/// The processes running right now, from `/proc`.
///
/// Only `/proc/<pid>/exe` is trusted. `comm` is truncated to 15 characters by
/// the kernel, and a truncated name in a rule matches nothing while looking
/// perfectly plausible in the UI.
pub(crate) fn collect_processes(collector: &mut Collector, budget: &Budget, proc_root: &Path) {
    let Ok(read) = std::fs::read_dir(proc_root) else {
        return;
    };
    for item in read.flatten() {
        if budget.expired() || collector.is_full() {
            return;
        }
        let name = item.file_name();
        let Some(name) = name.to_str() else { continue };
        if !name.bytes().all(|b| b.is_ascii_digit()) {
            continue;
        }
        let Ok(exe) = std::fs::read_link(item.path().join("exe")) else {
            continue;
        };
        let exe = exe.to_string_lossy().to_string();
        if exe.is_empty() || is_system_binary(&exe) {
            continue;
        }
        collector.add(
            &super::pretty_stem(&exe),
            &exe,
            Some(exe.clone()),
            SOURCE_PROCESS,
            true,
        );
    }
}

/// Whether a running binary is plumbing rather than something a person would
/// route. `/usr/bin` stays in — that is where browsers and messengers live.
fn is_system_binary(path: &str) -> bool {
    const SYSTEM_PREFIXES: [&str; 6] = [
        "/usr/lib/",
        "/usr/libexec/",
        "/lib/",
        "/sbin/",
        "/usr/sbin/",
        "/opt/systemd/",
    ];
    // A binary whose file was replaced or removed since it started; its path is
    // no longer meaningful.
    if path.ends_with(" (deleted)") {
        return true;
    }
    SYSTEM_PREFIXES.iter().any(|p| path.starts_with(p))
}

/// Locate a theme icon by `Icon=` name.
///
/// Only sizes at or under [`icons::MAX_EDGE`] are searched, so a hit can be
/// embedded byte-for-byte. SVG and XPM are skipped: rendering them would mean
/// carrying a rasteriser for artwork that is decoration.
pub(crate) fn find_theme_icon(name: &str, roots: &[PathBuf], themes: &[String]) -> Option<PathBuf> {
    let name = name.trim();
    if name.is_empty() {
        return None;
    }

    // An absolute path is the icon, not a name to look up.
    if name.starts_with('/') {
        let path = PathBuf::from(name);
        return (path.extension().map(|e| e == "png").unwrap_or(false) && path.is_file())
            .then_some(path);
    }

    let stem = name.strip_suffix(".png").unwrap_or(name);
    for root in roots {
        for theme in themes {
            for size in ICON_SIZES {
                for category in ICON_CATEGORIES {
                    let candidate = root
                        .join(theme)
                        .join(size)
                        .join(category)
                        .join(format!("{stem}.png"));
                    if candidate.is_file() {
                        return Some(candidate);
                    }
                }
            }
        }
        // Legacy flat directories such as /usr/share/pixmaps.
        let flat = root.join(format!("{stem}.png"));
        if flat.is_file() {
            return Some(flat);
        }
    }
    None
}

/// Read a theme icon and return it as a data URI, or `None` when it is missing,
/// unreadable, or bigger than the cap.
pub(crate) fn icon_data_uri_in(name: &str, roots: &[PathBuf], themes: &[String]) -> Option<String> {
    let path = find_theme_icon(name, roots, themes)?;
    let bytes = std::fs::read(path).ok()?;
    let (w, h) = icons::png_dimensions(&bytes)?;
    // Theme files are already the size their directory claims; anything larger
    // would need a decoder we do not carry, so it is left without an icon.
    (w <= icons::MAX_EDGE && h <= icons::MAX_EDGE).then(|| icons::data_uri(&bytes))
}

/// `gtk-icon-theme-name` from a GTK `settings.ini`, so a themed desktop finds
/// its own artwork before falling back to hicolor.
pub(crate) fn icon_theme_from_settings(text: &str) -> Option<String> {
    for line in text.lines() {
        let line = line.trim();
        if let Some(value) = line.strip_prefix("gtk-icon-theme-name") {
            let value = value.trim_start().strip_prefix('=')?.trim();
            let value = value.trim_matches('"').trim();
            if !value.is_empty() {
                return Some(value.to_string());
            }
        }
    }
    None
}

fn home() -> Option<PathBuf> {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .filter(|p| !p.as_os_str().is_empty())
}

fn xdg_data_dirs() -> Vec<PathBuf> {
    let raw = std::env::var("XDG_DATA_DIRS")
        .ok()
        .filter(|v| !v.trim().is_empty())
        .unwrap_or_else(|| "/usr/local/share:/usr/share".to_string());
    raw.split(':')
        .filter(|p| !p.is_empty())
        .map(PathBuf::from)
        .collect()
}

fn xdg_data_home() -> Option<PathBuf> {
    std::env::var_os("XDG_DATA_HOME")
        .map(PathBuf::from)
        .filter(|p| !p.as_os_str().is_empty())
        .or_else(|| home().map(|h| h.join(".local/share")))
}

/// Where desktop entries live, user-first so a user's own override of a system
/// entry wins.
fn desktop_roots() -> Vec<PathBuf> {
    let mut roots = Vec::new();
    if let Some(data_home) = xdg_data_home() {
        roots.push(data_home.join("applications"));
        roots.push(data_home.join("flatpak/exports/share/applications"));
    }
    for dir in xdg_data_dirs() {
        roots.push(dir.join("applications"));
    }
    roots.push(PathBuf::from("/var/lib/flatpak/exports/share/applications"));
    roots.push(PathBuf::from("/var/lib/snapd/desktop/applications"));
    dedup_paths(roots)
}

fn flatpak_roots() -> Vec<PathBuf> {
    let mut roots = Vec::new();
    if let Some(data_home) = xdg_data_home() {
        roots.push(data_home.join("flatpak/app"));
    }
    roots.push(PathBuf::from("/var/lib/flatpak/app"));
    dedup_paths(roots)
}

fn icon_roots() -> Vec<PathBuf> {
    let mut roots = Vec::new();
    if let Some(h) = home() {
        roots.push(h.join(".icons"));
    }
    if let Some(data_home) = xdg_data_home() {
        roots.push(data_home.join("icons"));
        roots.push(data_home.join("flatpak/exports/share/icons"));
    }
    for dir in xdg_data_dirs() {
        roots.push(dir.join("icons"));
    }
    roots.push(PathBuf::from("/var/lib/flatpak/exports/share/icons"));
    roots.push(PathBuf::from("/usr/share/pixmaps"));
    dedup_paths(roots)
}

/// Themes to search, most specific first: the desktop's own, then the
/// fallbacks every theme is required to inherit from.
fn icon_themes() -> Vec<String> {
    let mut themes = Vec::new();
    if let Some(h) = home() {
        for settings in ["gtk-4.0/settings.ini", "gtk-3.0/settings.ini"] {
            if let Ok(text) = std::fs::read_to_string(h.join(".config").join(settings)) {
                if let Some(theme) = icon_theme_from_settings(&text) {
                    themes.push(theme);
                    break;
                }
            }
        }
    }
    for fallback in ["hicolor", "Adwaita"] {
        if !themes.iter().any(|t| t == fallback) {
            themes.push(fallback.to_string());
        }
    }
    themes
}

fn dedup_paths(paths: Vec<PathBuf>) -> Vec<PathBuf> {
    let mut seen = HashSet::new();
    paths
        .into_iter()
        .filter(|p| seen.insert(p.clone()))
        .collect()
}

/// Resolve a desktop entry's `Icon=` against this session's themes.
pub(crate) fn icon_data_uri(name: &str) -> Option<String> {
    icon_data_uri_in(name, &icon_roots(), &icon_themes())
}

/// Everything this platform knows about installed applications.
pub(crate) fn collect(collector: &mut Collector, budget: &Budget) {
    // Processes first: they are the entries a user came for, so they must not
    // be the ones an entry cap drops.
    collect_processes(collector, budget, Path::new("/proc"));
    collect_desktop_entries(collector, budget, &desktop_roots(), &flatpak_roots());
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    /// A scratch directory that cleans itself up, so fixture trees never leak
    /// into the developer's temp folder.
    struct TempTree(PathBuf);

    impl TempTree {
        fn new(tag: &str) -> TempTree {
            let dir = std::env::temp_dir().join(format!(
                "tenebra-apps-{tag}-{}-{:?}",
                std::process::id(),
                std::thread::current().id()
            ));
            let _ = fs::remove_dir_all(&dir);
            fs::create_dir_all(&dir).unwrap();
            TempTree(dir)
        }

        fn write(&self, rel: &str, body: &str) -> PathBuf {
            let path = self.0.join(rel);
            fs::create_dir_all(path.parent().unwrap()).unwrap();
            fs::write(&path, body).unwrap();
            path
        }

        fn write_bytes(&self, rel: &str, body: &[u8]) -> PathBuf {
            let path = self.0.join(rel);
            fs::create_dir_all(path.parent().unwrap()).unwrap();
            fs::write(&path, body).unwrap();
            path
        }
    }

    impl Drop for TempTree {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    fn binary(exec: &str) -> Option<String> {
        match exec_target(exec) {
            Some(ExecTarget::Binary(b)) => Some(b),
            _ => None,
        }
    }

    #[test]
    fn field_codes_never_reach_the_binary_name() {
        assert_eq!(
            binary("/usr/bin/firefox %u").as_deref(),
            Some("/usr/bin/firefox")
        );
        assert_eq!(
            binary("telegram-desktop -- %u").as_deref(),
            Some("telegram-desktop")
        );
        assert_eq!(binary("code %F").as_deref(), Some("code"));
        // A literal percent is not a field code.
        assert_eq!(binary("weird%%name").as_deref(), Some("weird%name"));
    }

    #[test]
    fn env_and_assignment_prefixes_are_stripped() {
        assert_eq!(
            binary("env GDK_BACKEND=x11 /usr/bin/foo %U").as_deref(),
            Some("/usr/bin/foo")
        );
        assert_eq!(
            binary("/usr/bin/env MOZ_ENABLE_WAYLAND=1 firefox").as_deref(),
            Some("firefox")
        );
        assert_eq!(binary("LC_ALL=C myapp %f").as_deref(), Some("myapp"));
        assert_eq!(binary("env -u DISPLAY -i myapp").as_deref(), Some("myapp"));
    }

    #[test]
    fn a_shell_wrapper_yields_the_wrapped_binary() {
        assert_eq!(
            binary(r#"sh -c "exec /opt/thing/thing --flag %U""#).as_deref(),
            Some("/opt/thing/thing")
        );
        assert_eq!(
            binary(r#"/bin/sh -c "/opt/thing/thing %U""#).as_deref(),
            Some("/opt/thing/thing")
        );
        // A shell without -c is just a shell.
        assert_eq!(binary("bash").as_deref(), Some("bash"));
    }

    #[test]
    fn quoted_paths_survive_intact() {
        assert_eq!(
            binary(r#""/opt/My App/bin/app" %U"#).as_deref(),
            Some("/opt/My App/bin/app")
        );
        assert_eq!(
            binary(r#""/opt/we\"ird/app""#).as_deref(),
            Some(r#"/opt/we"ird/app"#)
        );
    }

    #[test]
    fn flatpak_wrappers_are_recognised_not_taken_literally() {
        assert_eq!(
            exec_target("/usr/bin/flatpak run --branch=stable --arch=x86_64 org.gimp.GIMP %U"),
            Some(ExecTarget::Flatpak {
                app_id: "org.gimp.GIMP".into(),
                command: None,
            })
        );
        assert_eq!(
            exec_target("flatpak run --command=obsidian md.obsidian.Obsidian %U"),
            Some(ExecTarget::Flatpak {
                app_id: "md.obsidian.Obsidian".into(),
                command: Some("obsidian".into()),
            })
        );
        // Not a launch at all.
        assert_eq!(exec_target("flatpak update"), None);
    }

    #[test]
    fn an_empty_or_useless_exec_resolves_to_nothing() {
        assert_eq!(exec_target(""), None);
        assert_eq!(exec_target("   %U  "), None);
    }

    #[test]
    fn desktop_entry_keys_and_escapes_are_parsed() {
        let text = "\
# a comment

[Desktop Entry]
Type=Application
Name=Test\\sApp
Name[ru]=Тест
Exec=/usr/bin/testapp %U
Icon=testapp
Categories=Network;

[Desktop Action New]
Exec=/usr/bin/other
";
        let entry = parse_desktop_entry(text).expect("parses");
        assert_eq!(entry.name.as_deref(), Some("Test App"));
        assert_eq!(entry.exec.as_deref(), Some("/usr/bin/testapp %U"));
        assert_eq!(entry.icon.as_deref(), Some("testapp"));
        assert!(entry.is_visible_application());
    }

    #[test]
    fn hidden_and_non_application_entries_are_not_pickable() {
        let base = "[Desktop Entry]\nType=Application\nName=X\nExec=/usr/bin/x\n";
        assert!(parse_desktop_entry(base).unwrap().is_visible_application());
        assert!(!parse_desktop_entry(&format!("{base}NoDisplay=true\n"))
            .unwrap()
            .is_visible_application());
        assert!(!parse_desktop_entry(&format!("{base}Hidden=TRUE\n"))
            .unwrap()
            .is_visible_application());
        assert!(
            !parse_desktop_entry("[Desktop Entry]\nType=Directory\nName=X\n")
                .unwrap()
                .is_visible_application()
        );
        // Not a desktop file at all.
        assert!(parse_desktop_entry("just some text\n").is_none());
    }

    #[test]
    fn a_desktop_tree_becomes_deduplicated_entries() {
        let tree = TempTree::new("desktop");
        tree.write(
            "apps/firefox.desktop",
            "[Desktop Entry]\nType=Application\nName=Firefox\nExec=/usr/bin/firefox %u\nIcon=firefox\n",
        );
        // Same binary, second root: must merge, not duplicate.
        tree.write(
            "apps2/firefox.desktop",
            "[Desktop Entry]\nType=Application\nName=Firefox ESR\nExec=firefox\n",
        );
        tree.write(
            "apps/nested/thing.desktop",
            "[Desktop Entry]\nType=Application\nName=Thing\nExec=thing %F\n",
        );
        tree.write(
            "apps/hidden.desktop",
            "[Desktop Entry]\nType=Application\nName=Hidden\nExec=hidden\nNoDisplay=true\n",
        );
        tree.write("apps/notes.txt", "ignore me");

        let mut c = Collector::new(50);
        collect_desktop_entries(
            &mut c,
            &Budget::new(std::time::Duration::from_secs(5)),
            &[tree.0.join("apps"), tree.0.join("apps2")],
            &[],
        );
        let (scan, hints) = c.finish();

        let exes: Vec<&str> = scan.apps.iter().map(|e| e.exe.as_str()).collect();
        assert!(exes.contains(&"firefox"));
        assert!(exes.contains(&"thing"));
        assert!(!exes.contains(&"hidden"));
        assert_eq!(exes.iter().filter(|e| **e == "firefox").count(), 1);
        assert_eq!(hints.get("firefox").map(String::as_str), Some("firefox"));
        assert_eq!(
            scan.apps
                .iter()
                .find(|e| e.exe == "firefox")
                .unwrap()
                .path
                .as_deref(),
            Some("/usr/bin/firefox")
        );
    }

    #[test]
    fn a_flatpak_entry_takes_its_binary_from_app_metadata() {
        let tree = TempTree::new("flatpak");
        tree.write(
            "apps/org.telegram.desktop.desktop",
            "[Desktop Entry]\nType=Application\nName=Messenger\nExec=/usr/bin/flatpak run --branch=stable org.telegram.desktop %u\n",
        );
        tree.write(
            "flatpak/org.telegram.desktop/current/active/metadata",
            "[Application]\nname=org.telegram.desktop\ncommand=telegram-desktop\n\n[Context]\nshared=network;\n",
        );

        let mut c = Collector::new(50);
        collect_desktop_entries(
            &mut c,
            &Budget::new(std::time::Duration::from_secs(5)),
            &[tree.0.join("apps")],
            &[tree.0.join("flatpak")],
        );
        let (scan, _) = c.finish();
        assert_eq!(scan.apps.len(), 1);
        assert_eq!(scan.apps[0].exe, "telegram-desktop");
        assert_eq!(scan.apps[0].name, "Messenger");
    }

    #[test]
    fn a_flatpak_entry_with_no_resolvable_binary_is_left_out() {
        let tree = TempTree::new("flatpak-unknown");
        tree.write(
            "apps/org.example.App.desktop",
            "[Desktop Entry]\nType=Application\nName=Example\nExec=flatpak run org.example.App\n",
        );

        let mut c = Collector::new(50);
        collect_desktop_entries(
            &mut c,
            &Budget::new(std::time::Duration::from_secs(5)),
            &[tree.0.join("apps")],
            &[tree.0.join("missing")],
        );
        // Guessing "App" from the id would put a name in the rule that no
        // process ever has.
        assert!(c.finish().0.apps.is_empty());
    }

    #[test]
    fn flatpak_metadata_without_a_command_yields_nothing() {
        assert_eq!(
            parse_flatpak_metadata("[Application]\nname=org.x.Y\ncommand=thing\n").as_deref(),
            Some("thing")
        );
        assert_eq!(
            parse_flatpak_metadata("[Context]\ncommand=not-here\n"),
            None
        );
        assert_eq!(parse_flatpak_metadata(""), None);
    }

    #[test]
    fn an_unreadable_root_does_not_stop_the_others() {
        let tree = TempTree::new("partial");
        tree.write(
            "good/ok.desktop",
            "[Desktop Entry]\nType=Application\nName=Ok\nExec=ok\n",
        );

        let mut c = Collector::new(50);
        collect_desktop_entries(
            &mut c,
            &Budget::new(std::time::Duration::from_secs(5)),
            &[tree.0.join("does-not-exist"), tree.0.join("good")],
            &[],
        );
        let (scan, _) = c.finish();
        assert_eq!(scan.apps.len(), 1);
        assert_eq!(scan.apps[0].exe, "ok");
    }

    #[test]
    fn an_exhausted_budget_stops_the_walk_immediately() {
        let tree = TempTree::new("budget");
        tree.write(
            "apps/a.desktop",
            "[Desktop Entry]\nType=Application\nName=A\nExec=a\n",
        );

        let mut c = Collector::new(50);
        collect_desktop_entries(&mut c, &Budget::spent(), &[tree.0.join("apps")], &[]);
        assert!(c.finish().0.apps.is_empty());
    }

    #[test]
    fn a_full_collector_stops_the_walk() {
        let tree = TempTree::new("full");
        for i in 0..5 {
            tree.write(
                &format!("apps/app{i}.desktop"),
                &format!("[Desktop Entry]\nType=Application\nName=App{i}\nExec=app{i}\n"),
            );
        }
        let mut c = Collector::new(2);
        collect_desktop_entries(
            &mut c,
            &Budget::new(std::time::Duration::from_secs(5)),
            &[tree.0.join("apps")],
            &[],
        );
        let (scan, _) = c.finish();
        assert_eq!(scan.apps.len(), 2);
        assert!(scan.truncated);
    }

    #[test]
    fn theme_icons_are_found_by_size_preference() {
        let tree = TempTree::new("icons");
        let small = icons::encode_png_rgba(2, 2, &[7; 16]).unwrap();
        tree.write_bytes("icons/hicolor/16x16/apps/thing.png", &small);
        tree.write_bytes("icons/hicolor/48x48/apps/thing.png", &small);
        tree.write_bytes("icons/hicolor/256x256/apps/thing.png", &small);

        let roots = vec![tree.0.join("icons")];
        let themes = vec!["hicolor".to_string()];
        let found = find_theme_icon("thing", &roots, &themes).expect("finds icon");
        assert!(
            found.to_string_lossy().contains("48x48"),
            "largest size within the cap wins: {found:?}"
        );
        assert!(find_theme_icon("absent", &roots, &themes).is_none());
        assert!(find_theme_icon("", &roots, &themes).is_none());
    }

    #[test]
    fn a_flat_pixmap_is_a_last_resort() {
        let tree = TempTree::new("pixmaps");
        let png = icons::encode_png_rgba(1, 1, &[1, 2, 3, 4]).unwrap();
        tree.write_bytes("pixmaps/thing.png", &png);
        let roots = vec![tree.0.join("pixmaps")];
        assert!(find_theme_icon("thing.png", &roots, &["hicolor".into()]).is_some());
    }

    #[test]
    fn an_oversized_or_undecodable_icon_yields_none() {
        let tree = TempTree::new("bigicon");
        let big = icons::encode_png_rgba(64, 64, &vec![9; 64 * 64 * 4]).unwrap();
        tree.write_bytes("icons/hicolor/48x48/apps/big.png", &big);
        tree.write_bytes("icons/hicolor/48x48/apps/bogus.png", b"not a png");

        let roots = vec![tree.0.join("icons")];
        let themes = vec!["hicolor".to_string()];
        assert!(icon_data_uri_in("big", &roots, &themes).is_none());
        assert!(icon_data_uri_in("bogus", &roots, &themes).is_none());

        let ok = icons::encode_png_rgba(4, 4, &[3; 64]).unwrap();
        tree.write_bytes("icons/hicolor/32x32/apps/fine.png", &ok);
        let uri = icon_data_uri_in("fine", &roots, &themes).expect("small icon embeds");
        assert!(uri.starts_with("data:image/png;base64,"));
    }

    #[test]
    fn the_gtk_icon_theme_is_read_from_settings() {
        assert_eq!(
            icon_theme_from_settings("[Settings]\ngtk-icon-theme-name=Papirus-Dark\n").as_deref(),
            Some("Papirus-Dark")
        );
        assert_eq!(
            icon_theme_from_settings("gtk-icon-theme-name = \"Yaru\"\n").as_deref(),
            Some("Yaru")
        );
        assert_eq!(
            icon_theme_from_settings("[Settings]\ngtk-theme-name=X\n"),
            None
        );
    }

    #[test]
    fn process_collection_survives_a_missing_proc() {
        let mut c = Collector::new(10);
        collect_processes(
            &mut c,
            &Budget::new(std::time::Duration::from_secs(5)),
            Path::new("/definitely-not-proc"),
        );
        assert!(c.finish().0.apps.is_empty());
    }

    #[test]
    fn system_binaries_are_not_offered_as_apps() {
        assert!(is_system_binary("/usr/lib/systemd/systemd-journald"));
        assert!(is_system_binary("/usr/libexec/gvfsd"));
        assert!(is_system_binary("/usr/bin/old (deleted)"));
        assert!(!is_system_binary("/usr/bin/firefox"));
        assert!(!is_system_binary("/opt/telegram/telegram-desktop"));
    }
}
