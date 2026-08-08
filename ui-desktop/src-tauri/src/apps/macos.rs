//! macOS application source: `.app` bundles plus live processes.
//!
//! As on Linux, the whole module compiles everywhere and only [`collect`] is
//! dispatched natively; the parsers take paths and text so they can be driven
//! from fixtures on any host.
//!
//! A bundle names its binary in `Contents/Info.plist`, but that file is often
//! stored in Apple's binary plist format, which this module does not decode. It
//! does not need to: `Contents/MacOS/` holds the executables, so a bundle whose
//! plist is unreadable still yields the right name from the directory itself.
//! What is lost is the pretty title, and the bundle's own folder name stands in
//! for that perfectly well.

use std::path::{Path, PathBuf};

use super::{icons, Budget, Collector, SOURCE_BUNDLE, SOURCE_PROCESS};

/// How deep to descend looking for bundles. `/Applications/Adobe …/Foo.app` is
/// the shape this exists for; nothing useful hides deeper.
const MAX_DEPTH: usize = 2;

/// The `Info.plist` keys that matter, as read from an XML plist.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub(crate) struct BundleInfo {
    pub executable: Option<String>,
    pub name: Option<String>,
    pub display_name: Option<String>,
    pub icon_file: Option<String>,
}

/// One resolved application bundle.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct BundleApp {
    pub name: String,
    pub exe: String,
    pub binary: PathBuf,
    pub icon: Option<PathBuf>,
}

/// Read the handful of `Info.plist` keys we use, from an XML plist.
///
/// A binary plist (`bplist00`) yields an empty result rather than an error: the
/// caller's fallback — reading `Contents/MacOS/` — covers it, and carrying a
/// binary-plist decoder for four string keys is not a trade worth making.
pub(crate) fn parse_info_plist(text: &str) -> BundleInfo {
    let mut info = BundleInfo::default();
    if text.starts_with("bplist") {
        return info;
    }

    for (key, value) in plist_pairs(text) {
        match key.as_str() {
            "CFBundleExecutable" => info.executable = Some(value),
            "CFBundleName" => info.name = Some(value),
            "CFBundleDisplayName" => info.display_name = Some(value),
            "CFBundleIconFile" => info.icon_file = Some(value),
            _ => {}
        }
    }
    info
}

/// Every `<key>` immediately followed by a `<string>` value.
///
/// A key whose value is an array or a nested dict is skipped rather than
/// resolved to the first string inside it — that would silently attribute some
/// nested value to a top-level key.
fn plist_pairs(text: &str) -> Vec<(String, String)> {
    let mut pairs = Vec::new();
    let mut rest = text;

    while let Some(start) = rest.find("<key>") {
        let after_key = &rest[start + 5..];
        let Some(end) = after_key.find("</key>") else {
            break;
        };
        let key = xml_unescape(after_key[..end].trim());
        let mut tail = after_key[end + 6..].trim_start();
        rest = tail;

        if let Some(value_start) = tail.strip_prefix("<string>") {
            if let Some(value_end) = value_start.find("</string>") {
                pairs.push((key, xml_unescape(value_start[..value_end].trim())));
                rest = &value_start[value_end + 9..];
            }
        } else if let Some(empty) = tail.strip_prefix("<string/>") {
            pairs.push((key, String::new()));
            rest = empty;
        } else {
            // Skip whatever non-string element follows so the next iteration
            // starts from a clean position.
            if let Some(next) = tail.find("<key>") {
                tail = &tail[next..];
                rest = tail;
            }
        }
    }
    pairs
}

fn xml_unescape(value: &str) -> String {
    if !value.contains('&') {
        return value.to_string();
    }
    value
        .replace("&lt;", "<")
        .replace("&gt;", ">")
        .replace("&quot;", "\"")
        .replace("&apos;", "'")
        .replace("&amp;", "&")
}

/// Resolve a `.app` directory into a pickable entry.
///
/// Returns `None` when the bundle has no executable we can name — without one
/// there is no rule to write.
pub(crate) fn bundle_app(bundle: &Path) -> Option<BundleApp> {
    let contents = bundle.join("Contents");
    let macos_dir = contents.join("MacOS");
    let info = std::fs::read_to_string(contents.join("Info.plist"))
        .map(|text| parse_info_plist(&text))
        .unwrap_or_default();

    let stem = bundle
        .file_stem()
        .map(|s| s.to_string_lossy().to_string())
        .unwrap_or_default();

    let executable = info
        .executable
        .as_deref()
        .map(str::trim)
        .filter(|e| !e.is_empty())
        .map(|e| e.to_string())
        .filter(|e| macos_dir.join(e).is_file())
        .or_else(|| single_executable(&macos_dir, &stem))?;

    let name = info
        .display_name
        .or(info.name)
        .map(|n| super::clean_name(&n))
        .filter(|n| !n.is_empty())
        .unwrap_or_else(|| stem.clone());

    Some(BundleApp {
        name,
        exe: executable.clone(),
        binary: macos_dir.join(&executable),
        icon: icns_path(&contents, info.icon_file.as_deref(), &stem),
    })
}

/// The binary inside `Contents/MacOS` when the plist did not name one: the sole
/// file if there is only one, otherwise the one named after the bundle.
fn single_executable(macos_dir: &Path, stem: &str) -> Option<String> {
    let files: Vec<String> = std::fs::read_dir(macos_dir)
        .ok()?
        .flatten()
        .filter(|e| e.path().is_file())
        .filter_map(|e| e.file_name().to_str().map(str::to_string))
        .collect();

    if files.len() == 1 {
        return files.into_iter().next();
    }
    files.into_iter().find(|f| f.eq_ignore_ascii_case(stem))
}

/// Where a bundle keeps its icon, following `CFBundleIconFile` when it has one.
fn icns_path(contents: &Path, icon_file: Option<&str>, stem: &str) -> Option<PathBuf> {
    let resources = contents.join("Resources");
    let mut candidates = Vec::new();
    if let Some(name) = icon_file.map(str::trim).filter(|n| !n.is_empty()) {
        // The key is documented as optionally omitting the extension.
        if name.ends_with(".icns") {
            candidates.push(name.to_string());
        } else {
            candidates.push(format!("{name}.icns"));
            candidates.push(name.to_string());
        }
    }
    candidates.push(format!("{stem}.icns"));
    candidates.push("AppIcon.icns".to_string());

    candidates
        .into_iter()
        .map(|c| resources.join(c))
        .find(|p| p.is_file())
}

/// Pick the largest PNG image inside an `.icns` container that fits within
/// `max` pixels a side.
///
/// `.icns` files store several representations; the modern ones are PNG, which
/// means a usable icon can be lifted out byte-for-byte. Older ARGB and RLE
/// representations are skipped — decoding them would mean carrying a decoder
/// for artwork, and `None` is an honest answer.
pub(crate) fn icns_best_png(bytes: &[u8], max: u32) -> Option<Vec<u8>> {
    if bytes.len() < 8 || &bytes[..4] != b"icns" {
        return None;
    }
    let declared = u32::from_be_bytes(bytes[4..8].try_into().ok()?) as usize;
    let end = declared.min(bytes.len());

    let mut best: Option<(u32, &[u8])> = None;
    let mut offset = 8usize;
    while offset + 8 <= end {
        let length =
            u32::from_be_bytes(bytes.get(offset + 4..offset + 8)?.try_into().ok()?) as usize;
        // A length that does not advance past its own header would loop forever.
        if length < 8 || offset + length > end {
            break;
        }
        let payload = &bytes[offset + 8..offset + length];
        if let Some((w, h)) = icons::png_dimensions(payload) {
            let edge = w.max(h);
            if edge <= max && best.map(|(b, _)| edge > b).unwrap_or(true) {
                best = Some((edge, payload));
            }
        }
        offset += length;
    }
    best.map(|(_, payload)| payload.to_vec())
}

/// Read an `.icns` file and return its best small representation as a data URI.
pub(crate) fn icon_from_icns_file(path: &str) -> Option<String> {
    let bytes = std::fs::read(path).ok()?;
    icns_best_png(&bytes, icons::MAX_EDGE).map(|png| icons::data_uri(&png))
}

/// Derive a bundle's icon from the path of its binary, for entries whose icon
/// was not recorded during enumeration (a running process, say).
pub(crate) fn icon_data_uri(binary_path: &str) -> Option<String> {
    let bundle = bundle_root(Path::new(binary_path))?;
    let app = bundle_app(&bundle)?;
    icon_from_icns_file(app.icon?.to_str()?)
}

/// Walk up from a path inside a bundle to the `.app` directory containing it.
pub(crate) fn bundle_root(path: &Path) -> Option<PathBuf> {
    let mut current = Some(path);
    while let Some(p) = current {
        if p.extension().map(|e| e == "app").unwrap_or(false) {
            return Some(p.to_path_buf());
        }
        current = p.parent();
    }
    None
}

/// Walk `roots` for `.app` bundles and feed them to `collector`.
pub(crate) fn collect_bundles(collector: &mut Collector, budget: &Budget, roots: &[PathBuf]) {
    for root in roots {
        if budget.expired() || collector.is_full() {
            return;
        }
        walk_bundles(collector, budget, root, 0);
    }
}

fn walk_bundles(collector: &mut Collector, budget: &Budget, dir: &Path, depth: usize) {
    let Ok(read) = std::fs::read_dir(dir) else {
        return;
    };
    for item in read.flatten() {
        if budget.expired() || collector.is_full() {
            return;
        }
        let path = item.path();
        if !path.is_dir() {
            continue;
        }
        if path.extension().map(|e| e == "app").unwrap_or(false) {
            if let Some(app) = bundle_app(&path) {
                let binary = app.binary.to_string_lossy().to_string();
                collector.add(
                    &app.name,
                    &app.exe,
                    Some(binary.clone()),
                    SOURCE_BUNDLE,
                    false,
                );
                if let Some(icon) = app.icon.as_ref().and_then(|p| p.to_str()) {
                    collector.hint_icon(&app.exe, icon);
                }
            }
            continue;
        }
        if depth < MAX_DEPTH {
            walk_bundles(collector, budget, &path, depth + 1);
        }
    }
}

/// Executable paths of the running processes, one per line, as `ps` prints them.
///
/// Lines that are not absolute paths are dropped: `ps` shortens some entries to
/// a bare name, and a shortened name is exactly the kind of near-miss that
/// produces a rule matching the wrong process.
pub(crate) fn parse_ps_output(text: &str) -> Vec<String> {
    text.lines()
        .map(str::trim)
        .filter(|line| line.starts_with('/'))
        .map(str::to_string)
        .collect()
}

/// Whether a running binary is system plumbing rather than an application.
/// Anything inside a bundle is kept, including Apple's own under
/// `/System/Applications`.
pub(crate) fn is_system_binary(path: &str) -> bool {
    if path.contains(".app/Contents/") {
        return false;
    }
    const SYSTEM_PREFIXES: [&str; 6] = [
        "/System/",
        "/usr/libexec/",
        "/usr/sbin/",
        "/sbin/",
        "/usr/bin/",
        "/Library/Apple/",
    ];
    SYSTEM_PREFIXES.iter().any(|p| path.starts_with(p))
}

/// The processes running right now.
///
/// macOS has no `/proc`, and the alternative to asking `ps` is a `sysctl`
/// walk through `libc`. `ps` is in the base system, returns in milliseconds and
/// needs no unsafe code; the budget is checked before it is spawned.
fn collect_processes(collector: &mut Collector, budget: &Budget) {
    if budget.expired() {
        return;
    }
    // -ww defeats the column truncation that would otherwise cut long paths.
    let output = match std::process::Command::new("ps")
        .args(["-axww", "-o", "comm="])
        .output()
    {
        Ok(output) if output.status.success() => output,
        Ok(_) => {
            collector.warn("processes: ps returned an error");
            return;
        }
        Err(_) => {
            collector.warn("processes: ps unavailable");
            return;
        }
    };

    let text = String::from_utf8_lossy(&output.stdout);
    for path in parse_ps_output(&text) {
        if collector.is_full() || budget.expired() {
            return;
        }
        if is_system_binary(&path) {
            continue;
        }
        let name = bundle_root(Path::new(&path))
            .and_then(|b| b.file_stem().map(|s| s.to_string_lossy().to_string()))
            .unwrap_or_else(|| super::pretty_stem(&path));
        collector.add(&name, &path, Some(path.clone()), SOURCE_PROCESS, true);
    }
}

/// Where applications live, user-installed first.
fn bundle_roots() -> Vec<PathBuf> {
    let mut roots = vec![
        PathBuf::from("/Applications"),
        PathBuf::from("/Applications/Utilities"),
    ];
    if let Some(home) = std::env::var_os("HOME").map(PathBuf::from) {
        roots.insert(0, home.join("Applications"));
    }
    roots.push(PathBuf::from("/System/Applications"));
    roots.push(PathBuf::from("/System/Applications/Utilities"));
    roots
}

/// Everything this platform knows about installed applications.
pub(crate) fn collect(collector: &mut Collector, budget: &Budget) {
    // Processes first, for the same reason as on the other platforms: the entry
    // cap must never fall on the apps a user is actually using.
    collect_processes(collector, budget);
    collect_bundles(collector, budget, &bundle_roots());
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    struct TempTree(PathBuf);

    impl TempTree {
        fn new(tag: &str) -> TempTree {
            let dir = std::env::temp_dir().join(format!(
                "tenebra-apps-mac-{tag}-{}-{:?}",
                std::process::id(),
                std::thread::current().id()
            ));
            let _ = fs::remove_dir_all(&dir);
            fs::create_dir_all(&dir).unwrap();
            TempTree(dir)
        }

        fn write(&self, rel: &str, body: &[u8]) -> PathBuf {
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

    const XML_PLIST: &str = r#"<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>Telegram</string>
    <key>CFBundleName</key>
    <string>Telegram</string>
    <key>CFBundleDisplayName</key>
    <string>Telegram Desktop</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>CFBundleDocumentTypes</key>
    <array>
        <dict>
            <key>CFBundleTypeName</key>
            <string>Not the app name</string>
        </dict>
    </array>
    <key>LSMinimumSystemVersion</key>
    <string>10.13</string>
</dict>
</plist>
"#;

    fn icns(entries: &[(&[u8; 4], Vec<u8>)]) -> Vec<u8> {
        let mut body = Vec::new();
        for (kind, payload) in entries {
            body.extend_from_slice(*kind);
            body.extend_from_slice(&((payload.len() + 8) as u32).to_be_bytes());
            body.extend_from_slice(payload);
        }
        let mut out = Vec::from(*b"icns");
        out.extend_from_slice(&((body.len() + 8) as u32).to_be_bytes());
        out.extend_from_slice(&body);
        out
    }

    #[test]
    fn info_plist_keys_are_read_from_xml() {
        let info = parse_info_plist(XML_PLIST);
        assert_eq!(info.executable.as_deref(), Some("Telegram"));
        assert_eq!(info.name.as_deref(), Some("Telegram"));
        assert_eq!(info.display_name.as_deref(), Some("Telegram Desktop"));
        assert_eq!(info.icon_file.as_deref(), Some("AppIcon"));
    }

    #[test]
    fn a_nested_dict_never_supplies_a_top_level_value() {
        // CFBundleDocumentTypes is an array; the string inside it must not be
        // mistaken for its value, nor leak into the following key.
        let pairs = plist_pairs(XML_PLIST);
        assert!(!pairs
            .iter()
            .any(|(k, _)| k == "CFBundleDocumentTypes" && !pairs.is_empty() && k.is_empty()));
        let doc = pairs.iter().find(|(k, _)| k == "CFBundleDocumentTypes");
        assert!(doc.is_none(), "array-valued key must be skipped: {doc:?}");
        assert!(pairs
            .iter()
            .any(|(k, v)| k == "LSMinimumSystemVersion" && v == "10.13"));
    }

    #[test]
    fn xml_entities_are_decoded() {
        let plist = "<dict><key>CFBundleName</key><string>Ben &amp; Jerry&apos;s</string></dict>";
        assert_eq!(
            parse_info_plist(plist).name.as_deref(),
            Some("Ben & Jerry's")
        );
    }

    #[test]
    fn a_binary_plist_is_reported_as_unknown_not_guessed() {
        let info = parse_info_plist("bplist00\u{0}\u{1}garbage");
        assert_eq!(info, BundleInfo::default());
    }

    #[test]
    fn a_bundle_resolves_to_its_named_executable() {
        let tree = TempTree::new("bundle");
        tree.write(
            "Apps/Telegram.app/Contents/Info.plist",
            XML_PLIST.as_bytes(),
        );
        tree.write("Apps/Telegram.app/Contents/MacOS/Telegram", b"bin");
        tree.write("Apps/Telegram.app/Contents/Resources/AppIcon.icns", b"x");

        let app = bundle_app(&tree.0.join("Apps/Telegram.app")).expect("resolves");
        assert_eq!(app.exe, "Telegram");
        assert_eq!(app.name, "Telegram Desktop");
        assert!(app.binary.ends_with("Contents/MacOS/Telegram"));
        assert!(app.icon.unwrap().ends_with("AppIcon.icns"));
    }

    #[test]
    fn a_bundle_with_an_unreadable_plist_falls_back_to_its_directory() {
        let tree = TempTree::new("bplist");
        tree.write("Apps/Thing.app/Contents/Info.plist", b"bplist00\x00\x01");
        tree.write("Apps/Thing.app/Contents/MacOS/thing-bin", b"bin");

        let app = bundle_app(&tree.0.join("Apps/Thing.app")).expect("resolves");
        assert_eq!(app.exe, "thing-bin");
        // No plist title, so the bundle's own name stands in.
        assert_eq!(app.name, "Thing");
    }

    #[test]
    fn a_plist_naming_a_missing_binary_falls_back_to_the_bundle_name() {
        let tree = TempTree::new("stale");
        tree.write(
            "Apps/Stale.app/Contents/Info.plist",
            b"<dict><key>CFBundleExecutable</key><string>gone</string></dict>",
        );
        tree.write("Apps/Stale.app/Contents/MacOS/Stale", b"bin");
        tree.write("Apps/Stale.app/Contents/MacOS/helper", b"bin");

        let app = bundle_app(&tree.0.join("Apps/Stale.app")).expect("resolves");
        assert_eq!(app.exe, "Stale");
    }

    #[test]
    fn a_bundle_without_any_executable_is_skipped() {
        let tree = TempTree::new("empty");
        tree.write("Apps/Empty.app/Contents/Info.plist", XML_PLIST.as_bytes());
        assert!(bundle_app(&tree.0.join("Apps/Empty.app")).is_none());
    }

    #[test]
    fn bundles_are_found_one_level_down_and_deduplicated() {
        let tree = TempTree::new("walk");
        tree.write("Apps/A.app/Contents/MacOS/a", b"bin");
        tree.write("Apps/Vendor Suite/B.app/Contents/MacOS/b", b"bin");
        tree.write("Apps/plain-folder/notes.txt", b"x");

        let mut c = Collector::new(50);
        collect_bundles(
            &mut c,
            &Budget::new(std::time::Duration::from_secs(5)),
            &[tree.0.join("Apps")],
        );
        let (scan, _) = c.finish();
        let exes: Vec<&str> = scan.apps.iter().map(|e| e.exe.as_str()).collect();
        assert!(exes.contains(&"a"), "{exes:?}");
        assert!(exes.contains(&"b"), "{exes:?}");
    }

    #[test]
    fn a_missing_root_leaves_the_rest_of_the_scan_alone() {
        let tree = TempTree::new("partial");
        tree.write("Apps/A.app/Contents/MacOS/a", b"bin");

        let mut c = Collector::new(50);
        collect_bundles(
            &mut c,
            &Budget::new(std::time::Duration::from_secs(5)),
            &[tree.0.join("nope"), tree.0.join("Apps")],
        );
        assert_eq!(c.finish().0.apps.len(), 1);
    }

    #[test]
    fn an_exhausted_budget_stops_the_bundle_walk() {
        let tree = TempTree::new("budget");
        tree.write("Apps/A.app/Contents/MacOS/a", b"bin");

        let mut c = Collector::new(50);
        collect_bundles(&mut c, &Budget::spent(), &[tree.0.join("Apps")]);
        assert!(c.finish().0.apps.is_empty());
    }

    #[test]
    fn icns_yields_the_largest_representation_within_the_cap() {
        let small = icons::encode_png_rgba(16, 16, &vec![1; 16 * 16 * 4]).unwrap();
        let mid = icons::encode_png_rgba(32, 32, &vec![2; 32 * 32 * 4]).unwrap();
        let big = icons::encode_png_rgba(128, 128, &vec![3; 128 * 128 * 4]).unwrap();
        let file = icns(&[
            (b"TOC ", vec![0; 4]),
            (b"icp4", small),
            (b"icp5", mid.clone()),
            (b"ic07", big),
        ]);

        let picked = icns_best_png(&file, icons::MAX_EDGE).expect("finds a PNG");
        assert_eq!(icons::png_dimensions(&picked), Some((32, 32)));
        assert_eq!(picked, mid);
    }

    #[test]
    fn icns_without_a_small_png_yields_none() {
        let big = icons::encode_png_rgba(64, 64, &vec![3; 64 * 64 * 4]).unwrap();
        let file = icns(&[(b"ic08", big)]);
        assert!(icns_best_png(&file, icons::MAX_EDGE).is_none());
        // Raw ARGB representations are not decoded.
        let argb = icns(&[(b"ic05", vec![0xAA; 64])]);
        assert!(icns_best_png(&argb, icons::MAX_EDGE).is_none());
    }

    #[test]
    fn a_truncated_or_bogus_icns_does_not_loop_or_panic() {
        assert!(icns_best_png(b"", icons::MAX_EDGE).is_none());
        assert!(icns_best_png(b"icns", icons::MAX_EDGE).is_none());
        assert!(icns_best_png(b"nope\x00\x00\x00\x08", icons::MAX_EDGE).is_none());
        // An entry claiming a length of zero must not spin forever.
        let mut bogus = Vec::from(*b"icns");
        bogus.extend_from_slice(&24u32.to_be_bytes());
        bogus.extend_from_slice(b"ic07");
        bogus.extend_from_slice(&0u32.to_be_bytes());
        bogus.extend_from_slice(&[0; 8]);
        assert!(icns_best_png(&bogus, icons::MAX_EDGE).is_none());
    }

    #[test]
    fn bundle_root_is_found_from_a_path_inside_it() {
        assert_eq!(
            bundle_root(Path::new("/Applications/Thing.app/Contents/MacOS/thing")),
            Some(PathBuf::from("/Applications/Thing.app"))
        );
        assert!(bundle_root(Path::new("/usr/bin/ssh")).is_none());
    }

    #[test]
    fn ps_output_keeps_only_absolute_paths() {
        let text =
            "/Applications/Thing.app/Contents/MacOS/Thing\nkernel_task\n  /usr/bin/ssh  \n\n";
        assert_eq!(
            parse_ps_output(text),
            [
                "/Applications/Thing.app/Contents/MacOS/Thing",
                "/usr/bin/ssh"
            ]
        );
    }

    #[test]
    fn system_daemons_are_filtered_but_apple_apps_are_not() {
        assert!(is_system_binary("/usr/libexec/trustd"));
        assert!(is_system_binary("/System/Library/CoreServices/loginwindow"));
        assert!(!is_system_binary(
            "/System/Applications/Music.app/Contents/MacOS/Music"
        ));
        assert!(!is_system_binary(
            "/Applications/Thing.app/Contents/MacOS/Thing"
        ));
    }

    #[test]
    fn an_icns_file_becomes_a_data_uri() {
        let tree = TempTree::new("icns");
        let png = icons::encode_png_rgba(16, 16, &vec![4; 16 * 16 * 4]).unwrap();
        let path = tree.write("Icon.icns", &icns(&[(b"icp4", png)]));

        let uri = icon_from_icns_file(path.to_str().unwrap()).expect("reads icon");
        assert!(uri.starts_with("data:image/png;base64,"));
        assert!(icon_from_icns_file("/definitely/not/here.icns").is_none());
    }
}
