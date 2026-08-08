//! Windows application source: the uninstall registry, the Start Menu, Store
//! app aliases and live processes.
//!
//! Only the collectors are gated to Windows; the three binary/text formats this
//! has to understand — shell links, app-execution-alias reparse points and
//! `DisplayIcon` values — are parsed by ordinary functions that compile and are
//! tested on any host.
//!
//! This is also the platform where scanning from the GUI process matters most.
//! The core runs as a LocalSystem service: `HKCU`, `%APPDATA%` and the user's
//! Start Menu do not exist for it, and per-user installs — which is most of
//! them nowadays — would be invisible.

#[cfg(windows)]
use std::path::Path;
use std::path::PathBuf;

use super::{Budget, Collector, SOURCE_PROCESS, SOURCE_REGISTRY, SOURCE_STARTMENU};

/// Shell link header size and class id (MS-SHLLINK 2.1). Both are fixed, and a
/// file that disagrees is not a shortcut.
const LNK_HEADER_SIZE: usize = 0x4C;
const LNK_CLSID: [u8; 16] = [
    0x01, 0x14, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46,
];

const LNK_HAS_TARGET_ID_LIST: u32 = 0x0000_0001;
const LNK_HAS_LINK_INFO: u32 = 0x0000_0002;
const LNK_HAS_NAME: u32 = 0x0000_0004;
const LNK_HAS_RELATIVE_PATH: u32 = 0x0000_0008;
const LNK_IS_UNICODE: u32 = 0x0000_0080;

/// `IO_REPARSE_TAG_APPEXECLINK` — the reparse point behind every Store app's
/// execution alias.
const IO_REPARSE_TAG_APPEXECLINK: u32 = 0x8000_001B;

/// How deep to walk the Start Menu. Vendors nest a folder or two; nothing goes
/// deeper than that on purpose.
const MAX_START_MENU_DEPTH: usize = 4;

/// Ceiling on uninstall entries examined per registry view, so a hive stuffed
/// with update records cannot eat the budget on its own.
#[cfg(windows)]
const MAX_REGISTRY_KEYS: usize = 800;

/// What a shortcut points at.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub(crate) struct ShellLink {
    /// The absolute target recorded in the LinkInfo block.
    pub local_path: Option<String>,
    /// The target relative to the shortcut's own folder. Some shortcuts carry
    /// only this, and it survives the app moving with its parent folder.
    pub relative_path: Option<String>,
}

/// Parse a `.lnk` far enough to learn what it launches.
///
/// Deliberately partial: only the header, the LinkInfo block and the string
/// data are read. Shortcuts to shell items with no filesystem path — a Store
/// app addressed by AppUserModelId, a control panel page — yield `None`, which
/// is correct. There is no executable name in them to match on.
pub(crate) fn parse_shell_link(bytes: &[u8]) -> Option<ShellLink> {
    if bytes.len() < LNK_HEADER_SIZE
        || u32_at(bytes, 0)? as usize != LNK_HEADER_SIZE
        || bytes.get(4..20)? != LNK_CLSID
    {
        return None;
    }

    let flags = u32_at(bytes, 20)?;
    let mut offset = LNK_HEADER_SIZE;

    if flags & LNK_HAS_TARGET_ID_LIST != 0 {
        let size = u16_at(bytes, offset)? as usize;
        offset = offset.checked_add(2)?.checked_add(size)?;
    }

    let mut link = ShellLink::default();

    if flags & LNK_HAS_LINK_INFO != 0 {
        let base = offset;
        let info_size = u32_at(bytes, base)? as usize;
        let header_size = u32_at(bytes, base + 4)? as usize;
        // 0x1C is the smallest header this structure can have, and it is part
        // of the structure, so a header larger than the whole block is a file
        // whose offsets cannot be trusted at all.
        if info_size < 0x1C
            || header_size < 0x1C
            || header_size > info_size
            || base.checked_add(info_size)? > bytes.len()
        {
            return None;
        }
        let info_flags = u32_at(bytes, base + 8)?;

        // Bit 0: the volume id and local base path are present. Without it the
        // target is on a network share, which has no local executable to match.
        if info_flags & 0x1 != 0 {
            // The unicode offsets only exist in the larger header, and are the
            // ones that survive a non-ASCII install path. A writer that has no
            // unicode copy to offer leaves them zero even in a 0x24 header, so
            // an absent field has to fall through to the ANSI pair rather than
            // be read at face value.
            let unicode = if header_size >= 0x24 {
                let path = string_at(bytes, base, header_size, info_size, 28, utf16z_at);
                let suffix = string_at(bytes, base, header_size, info_size, 32, utf16z_at);
                path.map(|p| format!("{p}{}", suffix.unwrap_or_default()))
            } else {
                None
            };
            link.local_path = unicode.or_else(|| {
                let path = string_at(bytes, base, header_size, info_size, 16, ansiz_at)?;
                let suffix = string_at(bytes, base, header_size, info_size, 24, ansiz_at);
                Some(format!("{path}{}", suffix.unwrap_or_default()))
            });
        }
        offset = base + info_size;
    }

    let unicode = flags & LNK_IS_UNICODE != 0;
    if flags & LNK_HAS_NAME != 0 {
        offset = skip_string_data(bytes, offset, unicode)?;
    }
    if flags & LNK_HAS_RELATIVE_PATH != 0 {
        let (value, _) = read_string_data(bytes, offset, unicode)?;
        link.relative_path = Some(value).filter(|v| !v.is_empty());
    }

    Some(link)
}

/// Resolve a shortcut's relative target against the folder the shortcut is in.
pub(crate) fn resolve_relative(dir: &std::path::Path, relative: &str) -> Option<PathBuf> {
    let mut out = dir.to_path_buf();
    for part in relative.split(['\\', '/']) {
        match part {
            "" | "." => {}
            ".." => {
                if !out.pop() {
                    return None;
                }
            }
            other => out.push(other),
        }
    }
    Some(out)
}

/// Pull the target executable out of an app-execution-alias reparse point.
///
/// The buffer is `REPARSE_DATA_BUFFER`: tag, data length, reserved, then a
/// version word and four NUL-separated UTF-16 strings — package family name,
/// AppUserModelId, executable, application type. The executable is the whole
/// point: an alias named `notepad.exe` can front a process called `Notepad.exe`,
/// and only the third string says which name a rule has to carry.
pub(crate) fn parse_app_exec_link(buffer: &[u8]) -> Option<String> {
    if u32_at(buffer, 0)? != IO_REPARSE_TAG_APPEXECLINK {
        return None;
    }
    let length = u16_at(buffer, 4)? as usize;
    let data = buffer.get(8..8usize.checked_add(length)?)?;
    // Version 3 is what Windows writes; refuse anything that predates the
    // layout rather than reading fields that may not be there.
    if u32_at(data, 0)? < 3 {
        return None;
    }

    let mut strings = utf16_string_list(&data[4..]);
    if strings.len() < 3 {
        return None;
    }
    let executable = strings.swap_remove(2);
    (!executable.trim().is_empty()).then_some(executable)
}

/// Split a run of NUL-terminated UTF-16 strings.
fn utf16_string_list(data: &[u8]) -> Vec<String> {
    let units: Vec<u16> = data
        .chunks_exact(2)
        .map(|c| u16::from_le_bytes([c[0], c[1]]))
        .collect();
    units
        .split(|&u| u == 0)
        .filter(|s| !s.is_empty())
        .map(String::from_utf16_lossy)
        .collect()
}

/// The executable an uninstall entry's `DisplayIcon` points at.
///
/// The value is `path[,index]`, sometimes quoted, and often names an `.ico` or
/// a DLL instead of the program. Only an `.exe` is accepted: this is the
/// cheapest reliable way to learn an installed app's binary, but a wrong answer
/// here becomes a rule matching nothing.
pub(crate) fn display_icon_exe(raw: &str) -> Option<String> {
    let value = raw.trim().trim_matches('"').trim();
    if value.is_empty() {
        return None;
    }
    // Strip a trailing icon index, but only when it really is one — a comma in
    // a directory name must survive.
    let path = match value.rsplit_once(',') {
        Some((head, tail)) if tail.trim().parse::<i32>().is_ok() && !head.trim().is_empty() => {
            head.trim()
        }
        _ => value,
    };
    let path = path.trim().trim_matches('"').trim();
    let is_exe = path
        .rsplit('.')
        .next()
        .map(|e| e.eq_ignore_ascii_case("exe"))
        == Some(true);
    (is_exe && path.len() > 4).then(|| path.to_string())
}

fn u16_at(bytes: &[u8], offset: usize) -> Option<u16> {
    Some(u16::from_le_bytes(
        bytes.get(offset..offset + 2)?.try_into().ok()?,
    ))
}

fn u32_at(bytes: &[u8], offset: usize) -> Option<u32> {
    Some(u32::from_le_bytes(
        bytes.get(offset..offset + 4)?.try_into().ok()?,
    ))
}

/// Read one of LinkInfo's path strings, given the offset field that locates it.
///
/// Offsets in that structure are counted from the start of the block and point
/// past its fixed header, so a value inside the header — zero above all, which
/// is how a writer says "this field is not here" — names no string. Read at
/// face value it would decode the block's own size field as text. The string is
/// also kept inside the block, so a missing terminator cannot run on into the
/// StringData that follows.
fn string_at(
    bytes: &[u8],
    base: usize,
    header_size: usize,
    info_size: usize,
    field: usize,
    read: fn(&[u8], usize) -> Option<String>,
) -> Option<String> {
    let offset = u32_at(bytes, base.checked_add(field)?)? as usize;
    if offset < header_size || offset >= info_size {
        return None;
    }
    let block = bytes.get(..base.checked_add(info_size)?)?;
    read(block, base.checked_add(offset)?)
}

/// A NUL-terminated UTF-16 string at `offset`.
fn utf16z_at(bytes: &[u8], offset: usize) -> Option<String> {
    let tail = bytes.get(offset..)?;
    let units: Vec<u16> = tail
        .chunks_exact(2)
        .map(|c| u16::from_le_bytes([c[0], c[1]]))
        .take_while(|&u| u != 0)
        .collect();
    (!units.is_empty()).then(|| String::from_utf16_lossy(&units))
}

/// A NUL-terminated single-byte string at `offset`, decoded as Latin-1.
///
/// The field is really in the system's ANSI code page, which we cannot know
/// from the bytes. Latin-1 is exact for the ASCII paths this covers in
/// practice, and anything it garbles fails the "does this file exist" check
/// that follows.
fn ansiz_at(bytes: &[u8], offset: usize) -> Option<String> {
    let tail = bytes.get(offset..)?;
    let text: String = tail
        .iter()
        .take_while(|&&b| b != 0)
        .map(|&b| b as char)
        .collect();
    (!text.is_empty()).then_some(text)
}

/// Read one StringData item (a character count followed by the characters).
fn read_string_data(bytes: &[u8], offset: usize, unicode: bool) -> Option<(String, usize)> {
    let count = u16_at(bytes, offset)? as usize;
    let start = offset + 2;
    if unicode {
        let end = start.checked_add(count.checked_mul(2)?)?;
        let raw = bytes.get(start..end)?;
        let units: Vec<u16> = raw
            .chunks_exact(2)
            .map(|c| u16::from_le_bytes([c[0], c[1]]))
            .collect();
        Some((String::from_utf16_lossy(&units), end))
    } else {
        let end = start.checked_add(count)?;
        let raw = bytes.get(start..end)?;
        Some((raw.iter().map(|&b| b as char).collect(), end))
    }
}

fn skip_string_data(bytes: &[u8], offset: usize, unicode: bool) -> Option<usize> {
    read_string_data(bytes, offset, unicode).map(|(_, next)| next)
}

/// Whether a Start Menu shortcut is worth offering. Uninstallers, help files
/// and documentation shortcuts share those folders with the applications.
fn is_useful_shortcut(name: &str, exe: &str) -> bool {
    let lowered = name.to_lowercase();
    !super::is_installer_exe(exe)
        && !lowered.contains("uninstall")
        && !lowered.contains("удал")
        && !lowered.starts_with("readme")
}

// ---------------------------------------------------------------------------
// Native collectors.
// ---------------------------------------------------------------------------

/// A NUL-terminated wide string for the Win32 entry points below.
#[cfg(windows)]
pub(crate) fn wide(value: &str) -> Vec<u16> {
    value.encode_utf16().chain(std::iter::once(0)).collect()
}

#[cfg(windows)]
fn from_wide(buffer: &[u16]) -> String {
    let end = buffer.iter().position(|&c| c == 0).unwrap_or(buffer.len());
    String::from_utf16_lossy(&buffer[..end])
}

/// An open registry key, closed on drop.
#[cfg(windows)]
struct RegKey(windows_sys::Win32::System::Registry::HKEY);

#[cfg(windows)]
impl Drop for RegKey {
    fn drop(&mut self) {
        // SAFETY: the handle came from RegOpenKeyExW and is closed exactly once.
        unsafe { windows_sys::Win32::System::Registry::RegCloseKey(self.0) };
    }
}

#[cfg(windows)]
impl RegKey {
    /// Open `path` under `root`. `sam_extra` selects a registry view
    /// (`KEY_WOW64_32KEY` / `KEY_WOW64_64KEY`); a 32-bit application's
    /// uninstall entry lives only in the 32-bit view.
    fn open(
        root: windows_sys::Win32::System::Registry::HKEY,
        path: &str,
        sam_extra: u32,
    ) -> Option<RegKey> {
        use windows_sys::Win32::Foundation::ERROR_SUCCESS;
        use windows_sys::Win32::System::Registry::{RegOpenKeyExW, HKEY, KEY_READ};

        let wide_path = wide(path);
        let mut handle: HKEY = std::ptr::null_mut();
        // SAFETY: `wide_path` is NUL-terminated and outlives the call; `handle`
        // is only read when the call reports success.
        let status = unsafe {
            RegOpenKeyExW(
                root,
                wide_path.as_ptr(),
                0,
                KEY_READ | sam_extra,
                &mut handle,
            )
        };
        (status == ERROR_SUCCESS && !handle.is_null()).then_some(RegKey(handle))
    }

    /// Names of the immediate subkeys, up to `max`.
    fn subkeys(&self, budget: &Budget, max: usize) -> Vec<String> {
        use windows_sys::Win32::Foundation::ERROR_SUCCESS;
        use windows_sys::Win32::System::Registry::RegEnumKeyExW;

        let mut names = Vec::new();
        // Registry key names are capped at 255 characters.
        let mut buffer = [0u16; 256];
        for index in 0..max as u32 {
            if budget.expired() {
                break;
            }
            let mut length = buffer.len() as u32;
            // SAFETY: `buffer` is sized from `length`, which the call updates
            // with the characters actually written.
            let status = unsafe {
                RegEnumKeyExW(
                    self.0,
                    index,
                    buffer.as_mut_ptr(),
                    &mut length,
                    std::ptr::null(),
                    std::ptr::null_mut(),
                    std::ptr::null_mut(),
                    std::ptr::null_mut(),
                )
            };
            if status != ERROR_SUCCESS {
                break;
            }
            names.push(from_wide(&buffer[..length as usize]));
        }
        names
    }

    /// A string value of a subkey, with `REG_EXPAND_SZ` expanded.
    fn string(&self, subkey: &str, name: &str) -> Option<String> {
        use windows_sys::Win32::Foundation::{ERROR_MORE_DATA, ERROR_SUCCESS};
        use windows_sys::Win32::System::Registry::{
            RegGetValueW, RRF_RT_REG_EXPAND_SZ, RRF_RT_REG_SZ,
        };

        let wide_sub = wide(subkey);
        let wide_name = wide(name);
        let mut buffer = vec![0u16; 512];
        loop {
            let mut size = (buffer.len() * 2) as u32;
            // SAFETY: both wide strings are NUL-terminated and live across the
            // call; `size` describes `buffer` in bytes, as the API expects.
            let status = unsafe {
                RegGetValueW(
                    self.0,
                    wide_sub.as_ptr(),
                    wide_name.as_ptr(),
                    RRF_RT_REG_SZ | RRF_RT_REG_EXPAND_SZ,
                    std::ptr::null_mut(),
                    buffer.as_mut_ptr() as *mut core::ffi::c_void,
                    &mut size,
                )
            };
            match status {
                ERROR_SUCCESS => {
                    let chars = (size as usize / 2).min(buffer.len());
                    let value = from_wide(&buffer[..chars]);
                    return (!value.trim().is_empty()).then_some(value);
                }
                // Grow once; a registry string past 32 KiB is not a path.
                ERROR_MORE_DATA if buffer.len() < 16_384 => {
                    buffer = vec![0u16; (size as usize / 2 + 1).min(16_384)];
                }
                _ => return None,
            }
        }
    }

    /// A DWORD value of a subkey.
    fn dword(&self, subkey: &str, name: &str) -> Option<u32> {
        use windows_sys::Win32::Foundation::ERROR_SUCCESS;
        use windows_sys::Win32::System::Registry::{RegGetValueW, RRF_RT_REG_DWORD};

        let wide_sub = wide(subkey);
        let wide_name = wide(name);
        let mut value = 0u32;
        let mut size = std::mem::size_of::<u32>() as u32;
        // SAFETY: `value` is exactly the four bytes `size` promises.
        let status = unsafe {
            RegGetValueW(
                self.0,
                wide_sub.as_ptr(),
                wide_name.as_ptr(),
                RRF_RT_REG_DWORD,
                std::ptr::null_mut(),
                &mut value as *mut u32 as *mut core::ffi::c_void,
                &mut size,
            )
        };
        (status == ERROR_SUCCESS).then_some(value)
    }
}

/// The uninstall registry, in all three views a program can register itself in.
#[cfg(windows)]
fn collect_registry(collector: &mut Collector, budget: &Budget) {
    use windows_sys::Win32::System::Registry::{
        HKEY_CURRENT_USER, HKEY_LOCAL_MACHINE, KEY_WOW64_32KEY, KEY_WOW64_64KEY,
    };

    const UNINSTALL: &str = r"SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall";
    let views = [
        (HKEY_LOCAL_MACHINE, KEY_WOW64_64KEY),
        (HKEY_LOCAL_MACHINE, KEY_WOW64_32KEY),
        (HKEY_CURRENT_USER, 0),
    ];

    let mut opened = 0usize;
    for (root, sam) in views {
        if budget.expired() || collector.is_full() {
            return;
        }
        let Some(key) = RegKey::open(root, UNINSTALL, sam) else {
            continue;
        };
        opened += 1;
        for subkey in key.subkeys(budget, MAX_REGISTRY_KEYS) {
            if budget.expired() || collector.is_full() {
                return;
            }
            add_registry_entry(collector, &key, &subkey, budget);
        }
    }

    if opened == 0 {
        collector.warn("registry: uninstall keys unreadable");
    }
}

/// Turn one uninstall subkey into an entry, if it describes a real application
/// whose executable we can name.
#[cfg(windows)]
fn add_registry_entry(collector: &mut Collector, parent: &RegKey, subkey: &str, budget: &Budget) {
    let Some(display_name) = parent.string(subkey, "DisplayName") else {
        return;
    };
    // Components, updates and patches all register here and are not
    // applications a person would recognise, let alone route.
    if parent.dword(subkey, "SystemComponent") == Some(1)
        || parent.string(subkey, "ParentKeyName").is_some()
        || parent
            .string(subkey, "ReleaseType")
            .map(|t| t != "Product")
            .unwrap_or(false)
    {
        return;
    }

    let from_icon = parent
        .string(subkey, "DisplayIcon")
        .as_deref()
        .and_then(display_icon_exe)
        .filter(|path| Path::new(path).is_file());

    let exe_path = match from_icon {
        Some(path) => Some(path),
        // Falling back to a directory listing costs a read per application, so
        // it only happens while there is budget left to spend on it.
        None if !budget.expired() => parent
            .string(subkey, "InstallLocation")
            .and_then(|dir| best_exe_in(Path::new(&dir), &display_name)),
        None => None,
    };

    let Some(exe_path) = exe_path else {
        return;
    };
    let Some(exe) = super::normalize_exe(&exe_path) else {
        return;
    };
    if super::is_installer_exe(&exe) {
        return;
    }
    collector.add(&display_name, &exe, Some(exe_path), SOURCE_REGISTRY, false);
}

/// The executable in `dir` most likely to be the application `display_name`
/// refers to.
///
/// Prefers a file name that echoes the display name, then falls back to the
/// only candidate when there is exactly one. A directory with several unrelated
/// executables yields nothing rather than a coin flip.
#[cfg(windows)]
fn best_exe_in(dir: &Path, display_name: &str) -> Option<String> {
    if dir.as_os_str().is_empty() {
        return None;
    }
    let mut candidates = Vec::new();
    for item in std::fs::read_dir(dir).ok()?.flatten().take(200) {
        let path = item.path();
        if path.extension().map(|e| e.eq_ignore_ascii_case("exe")) != Some(true) {
            continue;
        }
        let Some(stem) = path.file_stem().and_then(|s| s.to_str()) else {
            continue;
        };
        if super::is_installer_exe(&format!("{stem}.exe")) {
            continue;
        }
        candidates.push((stem.to_lowercase(), path.to_string_lossy().to_string()));
    }

    let wanted: String = display_name
        .to_lowercase()
        .chars()
        .filter(|c| c.is_alphanumeric())
        .collect();
    if !wanted.is_empty() {
        if let Some((_, path)) = candidates.iter().find(|(stem, _)| {
            let squashed: String = stem.chars().filter(|c| c.is_alphanumeric()).collect();
            !squashed.is_empty() && (wanted.starts_with(&squashed) || squashed.starts_with(&wanted))
        }) {
            return Some(path.clone());
        }
    }

    (candidates.len() == 1).then(|| candidates.remove(0).1)
}

/// Start Menu shortcuts, machine-wide and per-user.
#[cfg(windows)]
fn collect_start_menu(collector: &mut Collector, budget: &Budget) {
    let mut roots = Vec::new();
    for (var, tail) in [
        ("APPDATA", r"Microsoft\Windows\Start Menu\Programs"),
        ("ProgramData", r"Microsoft\Windows\Start Menu\Programs"),
    ] {
        if let Some(base) = std::env::var_os(var) {
            roots.push(PathBuf::from(base).join(tail));
        }
    }
    if roots.is_empty() {
        collector.warn("start menu: location unknown");
        return;
    }
    for root in roots {
        if budget.expired() || collector.is_full() {
            return;
        }
        walk_start_menu(collector, budget, &root, 0);
    }
}

#[cfg(windows)]
fn walk_start_menu(collector: &mut Collector, budget: &Budget, dir: &Path, depth: usize) {
    if depth > MAX_START_MENU_DEPTH {
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
            walk_start_menu(collector, budget, &path, depth + 1);
            continue;
        }
        if path.extension().map(|e| e.eq_ignore_ascii_case("lnk")) != Some(true) {
            continue;
        }
        let Ok(bytes) = std::fs::read(&path) else {
            continue;
        };
        let Some(link) = parse_shell_link(&bytes) else {
            continue;
        };

        let target = link
            .local_path
            .map(PathBuf::from)
            .filter(|p| p.is_file())
            .or_else(|| {
                link.relative_path
                    .as_deref()
                    .and_then(|rel| resolve_relative(path.parent()?, rel))
                    .filter(|p| p.is_file())
            });
        let Some(target) = target else { continue };
        if target.extension().map(|e| e.eq_ignore_ascii_case("exe")) != Some(true) {
            continue;
        }

        let full = target.to_string_lossy().to_string();
        let Some(exe) = super::normalize_exe(&full) else {
            continue;
        };
        let name = path
            .file_stem()
            .map(|s| s.to_string_lossy().to_string())
            .unwrap_or_else(|| super::pretty_stem(&full));
        if !is_useful_shortcut(&name, &exe) {
            continue;
        }
        collector.add(&name, &exe, Some(full), SOURCE_STARTMENU, false);
    }
}

/// Store applications, through the execution aliases Windows publishes for them.
///
/// The alias directory is the only part of a Store install an unelevated
/// process can read — `WindowsApps` itself denies the interactive user — and
/// each alias records the real executable name behind it.
#[cfg(windows)]
fn collect_store_aliases(collector: &mut Collector, budget: &Budget) {
    let Some(local) = std::env::var_os("LOCALAPPDATA") else {
        return;
    };
    let root = PathBuf::from(local).join(r"Microsoft\WindowsApps");
    walk_store_aliases(collector, budget, &root, 0);
}

#[cfg(windows)]
fn walk_store_aliases(collector: &mut Collector, budget: &Budget, dir: &Path, depth: usize) {
    let Ok(read) = std::fs::read_dir(dir) else {
        return;
    };
    for item in read.flatten() {
        if budget.expired() || collector.is_full() {
            return;
        }
        let path = item.path();
        // One level down holds the per-package aliases, which are not always
        // mirrored at the top level.
        if path.is_dir() {
            if depth == 0 {
                walk_store_aliases(collector, budget, &path, depth + 1);
            }
            continue;
        }
        if path.extension().map(|e| e.eq_ignore_ascii_case("exe")) != Some(true) {
            continue;
        }
        let Some(target) = read_app_exec_link(&path) else {
            continue;
        };
        let Some(exe) = super::normalize_exe(&target) else {
            continue;
        };
        // The alias name is what a user types; the target is what the kernel
        // reports. Name the row after the target so the two never disagree.
        collector.add(
            &super::pretty_stem(&target),
            &exe,
            None,
            SOURCE_REGISTRY,
            false,
        );
    }
}

/// Read the reparse point of an execution alias without following it.
#[cfg(windows)]
fn read_app_exec_link(path: &Path) -> Option<String> {
    use std::os::windows::ffi::OsStrExt;
    use windows_sys::Win32::Foundation::{CloseHandle, INVALID_HANDLE_VALUE};
    use windows_sys::Win32::Storage::FileSystem::{
        CreateFileW, FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT,
        FILE_READ_ATTRIBUTES, FILE_SHARE_DELETE, FILE_SHARE_READ, FILE_SHARE_WRITE, OPEN_EXISTING,
    };
    use windows_sys::Win32::System::Ioctl::FSCTL_GET_REPARSE_POINT;
    use windows_sys::Win32::System::IO::DeviceIoControl;

    let wide_path: Vec<u16> = path
        .as_os_str()
        .encode_wide()
        .chain(std::iter::once(0))
        .collect();

    // SAFETY: `wide_path` is NUL-terminated and outlives the call. The reparse
    // flag is what keeps this from opening the aliased executable itself, which
    // the ACLs on WindowsApps would refuse anyway.
    let handle = unsafe {
        CreateFileW(
            wide_path.as_ptr(),
            FILE_READ_ATTRIBUTES,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            std::ptr::null(),
            OPEN_EXISTING,
            FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS,
            std::ptr::null_mut(),
        )
    };
    if handle == INVALID_HANDLE_VALUE || handle.is_null() {
        return None;
    }

    let mut buffer = vec![0u8; 16_384];
    let mut returned = 0u32;
    // SAFETY: the output buffer is `buffer.len()` bytes and the handle is live
    // until CloseHandle below.
    let ok = unsafe {
        DeviceIoControl(
            handle,
            FSCTL_GET_REPARSE_POINT,
            std::ptr::null(),
            0,
            buffer.as_mut_ptr() as *mut core::ffi::c_void,
            buffer.len() as u32,
            &mut returned,
            std::ptr::null_mut(),
        )
    };
    // SAFETY: the handle came from CreateFileW and is closed exactly once.
    unsafe { CloseHandle(handle) };

    (ok != 0).then_some(())?;
    buffer.truncate(returned as usize);
    parse_app_exec_link(&buffer)
}

/// The processes running right now.
///
/// Only processes whose image path we can read are kept. That is exactly the
/// set an unelevated GUI can open — the user's own — which is also the only set
/// worth showing: system services are not things a person routes, and a process
/// we cannot resolve would enter the list on a name alone.
#[cfg(windows)]
fn collect_processes(collector: &mut Collector, budget: &Budget) {
    use windows_sys::Win32::Foundation::{CloseHandle, INVALID_HANDLE_VALUE};
    use windows_sys::Win32::System::Diagnostics::ToolHelp::{
        CreateToolhelp32Snapshot, Process32FirstW, Process32NextW, PROCESSENTRY32W,
        TH32CS_SNAPPROCESS,
    };

    // SAFETY: a process snapshot takes no input buffer; the handle is checked
    // before use and closed below.
    let snapshot = unsafe { CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0) };
    if snapshot == INVALID_HANDLE_VALUE || snapshot.is_null() {
        collector.warn("processes: snapshot unavailable");
        return;
    }

    let system_root = std::env::var("SystemRoot")
        .unwrap_or_else(|_| r"C:\Windows".to_string())
        .to_lowercase();

    let mut entry = PROCESSENTRY32W {
        dwSize: std::mem::size_of::<PROCESSENTRY32W>() as u32,
        ..Default::default()
    };
    // SAFETY: `entry.dwSize` is set as the API requires, and the snapshot is live.
    let mut has_entry = unsafe { Process32FirstW(snapshot, &mut entry) } != 0;
    while has_entry {
        if budget.expired() || collector.is_full() {
            break;
        }
        let name = from_wide(&entry.szExeFile);
        if let Some(path) = process_image_path(entry.th32ProcessID) {
            if !path.to_lowercase().starts_with(&system_root) {
                collector.add(
                    &super::pretty_stem(&path),
                    &name,
                    Some(path),
                    SOURCE_PROCESS,
                    true,
                );
            }
        }
        // SAFETY: same snapshot, same correctly sized entry.
        has_entry = unsafe { Process32NextW(snapshot, &mut entry) } != 0;
    }

    // SAFETY: the snapshot handle came from CreateToolhelp32Snapshot and is
    // closed exactly once.
    unsafe { CloseHandle(snapshot) };
}

/// Full image path of a process, or `None` when we may not ask.
#[cfg(windows)]
fn process_image_path(pid: u32) -> Option<String> {
    use windows_sys::Win32::Foundation::CloseHandle;
    use windows_sys::Win32::System::Threading::{
        OpenProcess, QueryFullProcessImageNameW, PROCESS_QUERY_LIMITED_INFORMATION,
    };

    // SAFETY: no buffers involved; a null return means access was refused.
    let handle = unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid) };
    if handle.is_null() {
        return None;
    }

    let mut buffer = [0u16; 512];
    let mut size = buffer.len() as u32;
    // SAFETY: `size` describes `buffer` in characters, as this API expects, and
    // is updated with the count actually written.
    let ok = unsafe { QueryFullProcessImageNameW(handle, 0, buffer.as_mut_ptr(), &mut size) };
    // SAFETY: the handle came from OpenProcess and is closed exactly once.
    unsafe { CloseHandle(handle) };

    (ok != 0).then(|| from_wide(&buffer[..size as usize]))
}

/// Everything this platform knows about installed applications.
#[cfg(windows)]
pub(crate) fn collect(collector: &mut Collector, budget: &Budget) {
    // Processes first so a full list never costs a user the app they are
    // looking at; the registry and Start Menu then upgrade those rows with
    // real names.
    collect_processes(collector, budget);
    collect_registry(collector, budget);
    collect_start_menu(collector, budget);
    collect_store_aliases(collector, budget);
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Build a shell link with a LinkInfo block, as the Start Menu writes them.
    fn lnk_with_link_info(path: &str, unicode: bool, id_list: bool) -> Vec<u8> {
        let mut flags = LNK_HAS_LINK_INFO | LNK_IS_UNICODE;
        if id_list {
            flags |= LNK_HAS_TARGET_ID_LIST;
        }

        let mut out = vec![0u8; LNK_HEADER_SIZE];
        out[0..4].copy_from_slice(&(LNK_HEADER_SIZE as u32).to_le_bytes());
        out[4..20].copy_from_slice(&LNK_CLSID);
        out[20..24].copy_from_slice(&flags.to_le_bytes());

        if id_list {
            // An opaque shell item list we must skip over intact.
            let junk = [0xABu8; 12];
            out.extend_from_slice(&(junk.len() as u16).to_le_bytes());
            out.extend_from_slice(&junk);
        }

        // LinkInfo: fixed 0x24 header, then the path strings after it.
        let header_size = 0x24usize;
        let mut info = vec![0u8; header_size];
        let mut strings = Vec::new();

        let (ansi_off, unicode_off) = if unicode {
            let off = header_size + strings.len();
            for unit in path.encode_utf16().chain(std::iter::once(0)) {
                strings.extend_from_slice(&unit.to_le_bytes());
            }
            // An ANSI copy still present but deliberately wrong, to prove the
            // unicode field is the one that wins.
            let ansi = header_size + strings.len();
            strings.extend_from_slice(b"C:\\wrong\\ansi.exe\0");
            (ansi, off)
        } else {
            let off = header_size + strings.len();
            strings.extend_from_slice(path.as_bytes());
            strings.push(0);
            (off, 0)
        };

        let empty_suffix = header_size + strings.len();
        strings.push(0);
        let empty_suffix_unicode = header_size + strings.len();
        strings.extend_from_slice(&[0, 0]);

        let total = header_size + strings.len();
        info[0..4].copy_from_slice(&(total as u32).to_le_bytes());
        info[4..8].copy_from_slice(&(header_size as u32).to_le_bytes());
        info[8..12].copy_from_slice(&1u32.to_le_bytes()); // VolumeIDAndLocalBasePath
        info[16..20].copy_from_slice(&(ansi_off as u32).to_le_bytes());
        info[24..28].copy_from_slice(&(empty_suffix as u32).to_le_bytes());
        if unicode {
            info[28..32].copy_from_slice(&(unicode_off as u32).to_le_bytes());
            info[32..36].copy_from_slice(&(empty_suffix_unicode as u32).to_le_bytes());
        }
        info.extend_from_slice(&strings);
        out.extend_from_slice(&info);
        out
    }

    fn lnk_with_relative(relative: &str, name: &str) -> Vec<u8> {
        let flags = LNK_HAS_NAME | LNK_HAS_RELATIVE_PATH | LNK_IS_UNICODE;
        let mut out = vec![0u8; LNK_HEADER_SIZE];
        out[0..4].copy_from_slice(&(LNK_HEADER_SIZE as u32).to_le_bytes());
        out[4..20].copy_from_slice(&LNK_CLSID);
        out[20..24].copy_from_slice(&flags.to_le_bytes());

        for value in [name, relative] {
            let units: Vec<u16> = value.encode_utf16().collect();
            out.extend_from_slice(&(units.len() as u16).to_le_bytes());
            for unit in units {
                out.extend_from_slice(&unit.to_le_bytes());
            }
        }
        out
    }

    fn app_exec_link(strings: &[&str]) -> Vec<u8> {
        let mut data = Vec::from(3u32.to_le_bytes());
        for value in strings {
            for unit in value.encode_utf16().chain(std::iter::once(0)) {
                data.extend_from_slice(&unit.to_le_bytes());
            }
        }
        let mut out = Vec::from(IO_REPARSE_TAG_APPEXECLINK.to_le_bytes());
        out.extend_from_slice(&(data.len() as u16).to_le_bytes());
        out.extend_from_slice(&0u16.to_le_bytes());
        out.extend_from_slice(&data);
        out
    }

    #[test]
    fn a_shortcut_yields_its_unicode_target() {
        let bytes = lnk_with_link_info(r"C:\Program Files\Telegram\Telegram.exe", true, false);
        let link = parse_shell_link(&bytes).expect("parses");
        assert_eq!(
            link.local_path.as_deref(),
            Some(r"C:\Program Files\Telegram\Telegram.exe")
        );
    }

    #[test]
    fn a_target_id_list_is_skipped_not_misread() {
        let bytes = lnk_with_link_info(r"C:\apps\thing.exe", true, true);
        let link = parse_shell_link(&bytes).expect("parses");
        assert_eq!(link.local_path.as_deref(), Some(r"C:\apps\thing.exe"));
    }

    #[test]
    fn an_ansi_only_shortcut_still_resolves() {
        let bytes = lnk_with_link_info(r"C:\apps\legacy.exe", false, false);
        let link = parse_shell_link(&bytes).expect("parses");
        assert_eq!(link.local_path.as_deref(), Some(r"C:\apps\legacy.exe"));
    }

    #[test]
    fn a_bogus_link_info_yields_no_path_rather_than_garbage() {
        // An offset past the end of the block points at the StringData that
        // follows, never at a path.
        let mut strayed = lnk_with_link_info(r"C:\apps\legacy.exe", false, false);
        let local_base_path = LNK_HEADER_SIZE + 16;
        strayed[local_base_path..local_base_path + 4].copy_from_slice(&0xFFFFu32.to_le_bytes());
        assert_eq!(parse_shell_link(&strayed).expect("parses").local_path, None);

        // A header larger than the structure it heads makes every offset in it
        // meaningless, so the shortcut is refused instead of half-read.
        let mut oversized = lnk_with_link_info(r"C:\apps\legacy.exe", false, false);
        let header_size = LNK_HEADER_SIZE + 4;
        oversized[header_size..header_size + 4].copy_from_slice(&0xFFFFu32.to_le_bytes());
        assert!(parse_shell_link(&oversized).is_none());
    }

    #[test]
    fn a_non_ascii_target_survives_the_unicode_field() {
        let path = r"C:\Программы\Мессенджер\app.exe";
        let bytes = lnk_with_link_info(path, true, false);
        assert_eq!(
            parse_shell_link(&bytes).unwrap().local_path.as_deref(),
            Some(path)
        );
    }

    #[test]
    fn a_relative_only_shortcut_exposes_its_relative_path() {
        let bytes = lnk_with_relative(r"..\..\App\app.exe", "Some App");
        let link = parse_shell_link(&bytes).expect("parses");
        assert_eq!(link.local_path, None);
        assert_eq!(link.relative_path.as_deref(), Some(r"..\..\App\app.exe"));
    }

    #[test]
    fn relative_targets_resolve_against_the_shortcut_folder() {
        let dir = std::path::Path::new(r"C:\Users\x\Start Menu\Programs\Vendor");
        assert_eq!(
            resolve_relative(dir, r"..\..\App\app.exe"),
            Some(PathBuf::from(r"C:\Users\x\Start Menu\App\app.exe"))
        );
        assert_eq!(
            resolve_relative(dir, r".\app.exe"),
            Some(dir.join("app.exe"))
        );
        // Climbing past the root is a malformed shortcut, not a target.
        assert_eq!(resolve_relative(std::path::Path::new(""), ".."), None);
    }

    #[test]
    fn garbage_is_not_mistaken_for_a_shortcut() {
        assert!(parse_shell_link(&[]).is_none());
        assert!(parse_shell_link(&[0u8; LNK_HEADER_SIZE]).is_none());
        // Right size, wrong class id.
        let mut wrong = vec![0u8; LNK_HEADER_SIZE];
        wrong[0..4].copy_from_slice(&(LNK_HEADER_SIZE as u32).to_le_bytes());
        assert!(parse_shell_link(&wrong).is_none());
        // Truncated mid-LinkInfo.
        let mut short = lnk_with_link_info(r"C:\apps\thing.exe", true, false);
        short.truncate(LNK_HEADER_SIZE + 8);
        assert!(parse_shell_link(&short).is_none());
    }

    #[test]
    fn an_execution_alias_reveals_the_real_process_name() {
        let buffer = app_exec_link(&[
            "Microsoft.WindowsNotepad_8wekyb3d8bbwe",
            "Microsoft.WindowsNotepad_8wekyb3d8bbwe!App",
            r"C:\Program Files\WindowsApps\Microsoft.WindowsNotepad_11.0\Notepad\Notepad.exe",
            "Desktop",
        ]);
        let target = parse_app_exec_link(&buffer).expect("parses");
        assert!(target.ends_with(r"\Notepad.exe"));
        assert_eq!(
            super::super::normalize_exe(&target).as_deref(),
            Some("notepad.exe")
        );
    }

    #[test]
    fn a_foreign_reparse_point_is_ignored() {
        let mut buffer = app_exec_link(&["a", "b", "c", "d"]);
        // Symlink tag rather than an execution alias.
        buffer[0..4].copy_from_slice(&0xA000_000Cu32.to_le_bytes());
        assert!(parse_app_exec_link(&buffer).is_none());
        assert!(parse_app_exec_link(&[]).is_none());

        // Too few strings to hold an executable.
        let short = app_exec_link(&["only", "two"]);
        assert!(parse_app_exec_link(&short).is_none());
    }

    #[test]
    fn display_icon_values_are_reduced_to_an_executable() {
        assert_eq!(
            display_icon_exe(r#""C:\Program Files\App\app.exe",0"#).as_deref(),
            Some(r"C:\Program Files\App\app.exe")
        );
        assert_eq!(
            display_icon_exe(r"C:\App\app.exe,-1").as_deref(),
            Some(r"C:\App\app.exe")
        );
        assert_eq!(
            display_icon_exe(r"C:\App\app.exe").as_deref(),
            Some(r"C:\App\app.exe")
        );
        // A comma inside a folder name is not an icon index.
        assert_eq!(
            display_icon_exe(r"C:\Sam, Inc\app.exe").as_deref(),
            Some(r"C:\Sam, Inc\app.exe")
        );
    }

    #[test]
    fn icon_only_display_icon_values_are_rejected() {
        assert_eq!(display_icon_exe(r"C:\App\app.ico"), None);
        assert_eq!(display_icon_exe(r"C:\Windows\system32\shell32.dll,5"), None);
        assert_eq!(display_icon_exe(""), None);
        assert_eq!(display_icon_exe("   "), None);
    }

    #[test]
    fn documentation_and_uninstaller_shortcuts_are_not_offered() {
        assert!(is_useful_shortcut("Telegram Desktop", "telegram.exe"));
        assert!(!is_useful_shortcut("Uninstall Telegram", "telegram.exe"));
        assert!(!is_useful_shortcut("Anything", "unins000.exe"));
        assert!(!is_useful_shortcut("ReadMe", "notepad.exe"));
    }

    #[test]
    fn utf16_string_lists_drop_empty_runs() {
        let mut data = Vec::new();
        for unit in "a\0\0b\0".encode_utf16() {
            data.extend_from_slice(&unit.to_le_bytes());
        }
        assert_eq!(utf16_string_list(&data), ["a", "b"]);
    }

    /// A manual diagnostic, not part of the suite: it reports on the machine it
    /// runs on. `cargo test -- --ignored live_scan` prints counts and a short
    /// sample so the scanner can be sanity-checked against a real system.
    #[test]
    #[ignore = "reports on the developer's own machine"]
    fn live_scan() {
        use std::time::Instant;

        let started = Instant::now();
        let scan = super::super::scan(
            Budget::new(super::super::SCAN_BUDGET),
            super::super::MAX_ENTRIES,
        );
        let elapsed = started.elapsed();

        let with_icon = scan.apps.iter().filter(|a| a.icon.is_some()).count();
        let running = scan.apps.iter().filter(|a| a.running).count();
        let bytes: usize = scan
            .apps
            .iter()
            .filter_map(|a| a.icon.as_ref())
            .map(String::len)
            .sum();
        println!(
            "entries={} icons={} running={} truncated={} icon_bytes={} elapsed={:?}",
            scan.apps.len(),
            with_icon,
            running,
            scan.truncated,
            bytes,
            elapsed
        );
        println!("warnings={:?}", scan.warnings);

        let mut by_source: std::collections::BTreeMap<&str, usize> = Default::default();
        for app in &scan.apps {
            *by_source.entry(app.source).or_default() += 1;
        }
        println!("by_source={by_source:?}");

        // Both halves of the list, since the running-first order otherwise
        // hides every installed-but-idle row behind the processes.
        let show = |label: &str, rows: Vec<&super::super::AppEntry>| {
            println!("{label}:");
            for app in rows {
                println!(
                    "  {:<34} {:<26} {:<10} running={} icon={}",
                    app.name,
                    app.exe,
                    app.source,
                    app.running,
                    app.icon.is_some()
                );
            }
        };
        show(
            "running",
            scan.apps.iter().filter(|a| a.running).take(10).collect(),
        );
        show(
            "installed",
            scan.apps.iter().filter(|a| !a.running).take(10).collect(),
        );
    }
}
