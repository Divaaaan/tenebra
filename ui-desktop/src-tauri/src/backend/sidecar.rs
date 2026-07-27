//! The sidecar transport: a `tenebra-core` child process owned by the GUI.
//!
//! The core owns sing-box and the tunnel; this type owns the core process and
//! wires its stdin/stdout to the transport-agnostic protocol client in
//! [`wire`](super::wire) — requests go in on the child's stdin, the reader
//! thread frames its stdout and completes them. The [`Backend`] impl comes from
//! the blanket impl over [`WireSession`]; nothing protocol-shaped lives here.
//!
//! Spawning uses `std::process` rather than the shell plugin's `sidecar()`
//! helper: that helper is async and tied to an `AppHandle`, whereas the
//! `Backend` trait is synchronous and the integration test drives this type
//! without a Tauri app at all. Resolving the externalBin path ourselves keeps
//! the same binary working in both the GUI and a headless test, and the test is
//! the contract we verify. The path resolution mirrors Tauri's externalBin
//! naming (`tenebra-core-<target-triple>`), with a plain `tenebra-core` and the
//! repo's build output as fallbacks.

use std::io::Write;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};
use std::thread;

use super::wire::{read_loop, WireClient, WireSession};
use super::EventSink;

/// Client over a running `tenebra-core` child process.
pub struct SidecarBackend {
    client: Arc<WireClient>,
    /// The child handle, kept so dropping the backend kills the core.
    child: Mutex<Child>,
}

impl SidecarBackend {
    /// Spawn the core at `program` and start serving. Events are forwarded to
    /// `sink`. `singbox_path` is handed to the child as `TENEBRA_SINGBOX` so it
    /// can locate the sing-box binary (bundling that as a Tauri resource is a
    /// follow-up).
    pub fn spawn(
        program: impl Into<PathBuf>,
        singbox_path: impl Into<PathBuf>,
        sink: Arc<dyn EventSink>,
    ) -> Result<Self, String> {
        let program = program.into();
        let mut command = Command::new(&program);
        command
            .env("TENEBRA_SINGBOX", singbox_path.into())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            // Send the core's stderr diagnostics to a log file rather than
            // inheriting our own. A GUI app has no console, so its stderr handle
            // is invalid; inheriting it makes CreateProcess fail with
            // STARTF_USESTDHANDLES and the sidecar never starts. A real file (or
            // the null device) is always a valid handle, and the log is handy
            // when a user reports a problem.
            .stderr(core_log_stderr());
        // Don't flash a console window when the GUI spawns the console-subsystem
        // core; the pipes carry everything we need.
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            const CREATE_NO_WINDOW: u32 = 0x0800_0000;
            command.creation_flags(CREATE_NO_WINDOW);
        }
        let mut child = command
            .spawn()
            .map_err(|e| format!("failed to start tenebra-core ({}): {e}", program.display()))?;

        let stdin = child
            .stdin
            .take()
            .ok_or_else(|| "tenebra-core stdin was not captured".to_string())?;
        let stdout = child
            .stdout
            .take()
            .ok_or_else(|| "tenebra-core stdout was not captured".to_string())?;

        let client = WireClient::new(stdin);
        let reader_client = Arc::clone(&client);
        thread::Builder::new()
            .name("tenebra-core-reader".into())
            .spawn(move || read_loop(stdout, reader_client, sink))
            .map_err(|e| format!("failed to start core reader thread: {e}"))?;

        Ok(Self {
            client,
            child: Mutex::new(child),
        })
    }

    /// Resolve the path to the core binary the way the GUI ships it: Tauri lays
    /// an externalBin down next to the app executable as
    /// `tenebra-core-<target-triple>(.exe)`. We try that first, then a plain
    /// `tenebra-core` in the same directory. The integration test points at the
    /// build output directly and does not use this.
    ///
    /// Fails closed: if neither is found beside the app we return an error rather
    /// than a bare `tenebra-core`. A bare name is resolved by the OS relative to
    /// the current directory (and then PATH), so a `tenebra-core` planted in an
    /// attacker-chosen CWD could be spawned with the app's privileges. The
    /// candidates here are joined onto the (absolute) executable directory, so
    /// they can't be redirected by the CWD.
    pub fn default_program() -> Result<PathBuf, String> {
        let exe = std::env::current_exe().map_err(|e| {
            format!("cannot locate the app executable to resolve tenebra-core beside it: {e}")
        })?;
        let dir = exe
            .parent()
            .ok_or_else(|| format!("app executable {} has no parent directory", exe.display()))?;
        match resolve_core_in(dir) {
            Ok(path) => Ok(path),
            // A native package need not keep the two side by side the way a
            // Tauri bundle does, so fall back to the same system locations the
            // bundled resources are looked for in (see
            // `crate::packaged_resource_paths`) before giving up. Absolute
            // paths only, so this stays as fail-closed as the directory scan.
            Err(e) => {
                let name = format!("tenebra-core{}", std::env::consts::EXE_SUFFIX);
                crate::packaged_resource_paths(&name)
                    .into_iter()
                    .find(|p| p.exists())
                    .ok_or(e)
            }
        }
    }
}

/// Pick the core binary out of `dir`: the target-triple externalBin first, then
/// a plain `tenebra-core`, both absolute (joined onto `dir`). Returns an error
/// naming what was looked for when neither exists — the caller fails closed
/// rather than spawning a bare name from the current directory or PATH. Split
/// out from [`SidecarBackend::default_program`] so the fail-closed behaviour can
/// be tested against a directory we control.
fn resolve_core_in(dir: &std::path::Path) -> Result<PathBuf, String> {
    let suffix = std::env::consts::EXE_SUFFIX;
    let triple = dir.join(format!(
        "tenebra-core-{}{}",
        env!("TENEBRA_TARGET_TRIPLE"),
        suffix
    ));
    if triple.exists() {
        return Ok(triple);
    }
    let plain = dir.join(format!("tenebra-core{suffix}"));
    if plain.exists() {
        return Ok(plain);
    }
    Err(format!(
        "tenebra-core not found beside the app (looked for {} and {}); \
         refusing to resolve a bare name from CWD/PATH",
        triple.display(),
        plain.display()
    ))
}

impl WireSession for SidecarBackend {
    /// The sidecar has exactly one session for its whole life: the child's
    /// stdio. Once the child dies the client is closed and requests fail fast
    /// with the wire layer's own message.
    fn session(&self) -> Result<Arc<WireClient>, String> {
        Ok(Arc::clone(&self.client))
    }
}

/// Where the core's stderr diagnostics are written: `core.log` in the Tenebra
/// data directory (per platform — see [`crash::data_dir`](crate::crash::data_dir)),
/// so a user hitting a problem has one file to share. Shares that directory with
/// the GUI crash log so both land in the same place.
fn core_log_path() -> Option<PathBuf> {
    crate::crash::data_dir().map(|d| d.join("core.log"))
}

/// A valid stderr target for the core: the log file if it can be opened,
/// otherwise the null device. Never an inherited handle — see `spawn`.
///
/// Opened for append, not truncate: a crash-restart loop would otherwise wipe
/// the very tail that explains the crash on every relaunch. We write a short
/// separator first so successive sessions stay legible in the one file.
fn core_log_stderr() -> Stdio {
    core_log_path()
        .and_then(|p| {
            let mut file = std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(p)
                .ok()?;
            let _ = writeln!(file, "\n--- tenebra-core session start ---");
            Some(file)
        })
        .map(Stdio::from)
        .unwrap_or_else(Stdio::null)
}

impl Drop for SidecarBackend {
    fn drop(&mut self) {
        // Fail any in-flight requests first so no caller blocks on a child
        // we're about to kill. Closing stdin lets the core's Serve loop return
        // on EOF and tear the tunnel down cleanly; killing the child is the
        // backstop if it doesn't.
        self.client.close();
        if let Ok(mut child) = self.child.lock() {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

#[cfg(test)]
mod tests {
    //! The protocol machinery is covered in `wire.rs`; the round-trip against
    //! the real core binary in `tests/sidecar_e2e.rs`. Here we only pin the
    //! spawn-failure surface `make_backend` relies on for its mock fallback.

    use super::*;
    use crate::backend::testutil::Rec;

    /// A unique scratch directory under the temp dir, removed on drop, so the
    /// resolver tests don't collide or leave litter. No dev-dependency needed.
    struct TempDir(PathBuf);

    impl TempDir {
        fn new(tag: &str) -> Self {
            use std::sync::atomic::{AtomicU32, Ordering};
            static SEQ: AtomicU32 = AtomicU32::new(0);
            let n = SEQ.fetch_add(1, Ordering::Relaxed);
            let dir = std::env::temp_dir().join(format!(
                "tenebra-sidecar-test-{}-{tag}-{n}",
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
    fn resolve_core_in_errors_when_absent_instead_of_a_bare_name() {
        // An empty directory has no core beside the app. The resolver must fail
        // closed rather than hand back a bare `tenebra-core` that the OS would
        // look up from the current directory / PATH — the binary-planting vector.
        let dir = TempDir::new("absent");
        let err = resolve_core_in(&dir.0).expect_err("empty dir must not resolve a core");
        assert!(
            err.contains("refusing to resolve a bare name"),
            "error should explain the fail-closed choice: {err}"
        );
    }

    #[test]
    fn resolve_core_in_returns_the_absolute_plain_binary_when_present() {
        // A `tenebra-core` sitting beside the app resolves to that exact absolute
        // path — anchored on the given directory, never a relative bare name.
        let dir = TempDir::new("plain");
        let suffix = std::env::consts::EXE_SUFFIX;
        let plain = dir.0.join(format!("tenebra-core{suffix}"));
        std::fs::write(&plain, b"").unwrap();

        let resolved = resolve_core_in(&dir.0).expect("present core must resolve");
        assert_eq!(resolved, plain);
        assert!(resolved.is_absolute(), "resolved path must be absolute");
        assert!(
            resolved.starts_with(&dir.0),
            "resolved path must stay under the app directory: {}",
            resolved.display()
        );
    }

    #[test]
    fn resolve_core_in_prefers_the_target_triple_binary() {
        // With both the triple externalBin and the plain name present, the triple
        // one (what Tauri actually ships) wins.
        let dir = TempDir::new("triple");
        let suffix = std::env::consts::EXE_SUFFIX;
        let triple = dir.0.join(format!(
            "tenebra-core-{}{}",
            env!("TENEBRA_TARGET_TRIPLE"),
            suffix
        ));
        std::fs::write(&triple, b"").unwrap();
        std::fs::write(dir.0.join(format!("tenebra-core{suffix}")), b"").unwrap();

        assert_eq!(resolve_core_in(&dir.0).unwrap(), triple);
    }

    #[test]
    fn spawn_failure_reports_the_program() {
        let sink: Arc<dyn EventSink> = Arc::new(Rec::default());
        let err = match SidecarBackend::spawn(
            "tenebra-core-that-does-not-exist",
            "sing-box-irrelevant",
            sink,
        ) {
            Ok(_) => panic!("spawning a nonexistent program must fail"),
            Err(e) => e,
        };
        assert!(
            err.contains("failed to start tenebra-core"),
            "unexpected error: {err}"
        );
        assert!(
            err.contains("tenebra-core-that-does-not-exist"),
            "error should name the program: {err}"
        );
    }
}
