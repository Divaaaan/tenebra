//! Pure readiness/transport policy, shared by the installer helper and GUI.
pub fn allow_windows_sidecar(debug_build: bool, pipe_override: Option<&str>) -> bool {
    debug_build && matches!(pipe_override, Some("off" | "0"))
}

pub fn verify_version(actual: Option<&str>, expected: &str) -> Result<(), String> {
    if actual == Some(expected) {
        return Ok(());
    }
    Err(format!("Tenebra service version {} does not match app {expected}; rerun the installer to repair the service", actual.unwrap_or("unknown")))
}

// Installer registrations contain one quoted absolute executable and no args.
pub fn registered_image(command: &str) -> Option<&str> {
    let image = command.strip_prefix('"')?.strip_suffix('"')?;
    if image.contains('"') || !std::path::Path::new(image).is_absolute() {
        return None;
    }
    Some(image)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn installed_build_never_switches_profile_store() {
        for value in [None, Some(""), Some("off"), Some("0"), Some("other-pipe")] {
            assert!(!allow_windows_sidecar(false, value));
        }
        assert!(!allow_windows_sidecar(true, None));
        assert!(allow_windows_sidecar(true, Some("off")));
    }
    #[test]
    fn readiness_requires_exact_known_version() {
        assert!(verify_version(Some("0.5.11"), "0.5.11").is_ok());
        assert!(verify_version(Some("0.5.10"), "0.5.11").is_err());
        assert!(verify_version(None, "0.5.11").is_err());
        assert!(verify_version(Some("0.5.11-beta.1"), "0.5.11").is_err());
    }

    #[test]
    #[cfg(windows)]
    fn registered_image_rejects_ambiguous_or_relative_commands() {
        assert_eq!(
            registered_image(r#""C:\Program Files\Tenebra\tenebra-core.exe""#),
            Some(r"C:\Program Files\Tenebra\tenebra-core.exe")
        );
        for command in [
            r"C:\Program Files\Tenebra\tenebra-core.exe",
            r#""relative.exe""#,
            r#""C:\core.exe" --pipe"#,
            "",
        ] {
            assert!(registered_image(command).is_none());
        }
    }
}
