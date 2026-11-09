//! Which installs the GUI updater can safely replace in full.

pub fn supports_in_app_updates(target_os: &str, has_appimage: bool) -> bool {
    match target_os {
        // The macOS package owns both the app and a privileged daemon. Tauri's
        // app-only updater would leave the daemon and engine on the old version.
        "macos" => false,
        "linux" => has_appimage,
        _ => true,
    }
}

#[cfg(test)]
mod tests {
    use super::supports_in_app_updates;

    #[test]
    fn macos_requires_the_full_package() {
        assert!(!supports_in_app_updates("macos", false));
        assert!(!supports_in_app_updates("macos", true));
    }

    #[test]
    fn windows_keeps_in_app_updates() {
        assert!(supports_in_app_updates("windows", false));
    }

    #[test]
    fn linux_appimage_keeps_in_app_updates() {
        assert!(supports_in_app_updates("linux", true));
    }

    #[test]
    fn linux_native_packages_use_the_package_manager() {
        assert!(!supports_in_app_updates("linux", false));
    }
}
