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
    /// `tenebra-core`, and finally let the OS resolve a bare `tenebra-core` from
    /// PATH. The integration test points at the build output directly and does
    /// not use this.
    pub fn default_program() -> PathBuf {
        let exe_dir = std::env::current_exe()
            .ok()
            .and_then(|p| p.parent().map(|d| d.to_path_buf()));
        let suffix = std::env::consts::EXE_SUFFIX;
        if let Some(dir) = exe_dir {
            let triple = dir.join(format!(
                "tenebra-core-{}{}",
                env!("TENEBRA_TARGET_TRIPLE"),
                suffix
            ));
            if triple.exists() {
                return triple;
            }
            let plain = dir.join(format!("tenebra-core{suffix}"));
            if plain.exists() {
                return plain;
            }
        }
        PathBuf::from("tenebra-core")
    }
}

impl WireSession for SidecarBackend {
    /// The sidecar has exactly one session for its whole life: the child's
    /// stdio. Once the child dies the client is closed and requests fail fast
    /// with the wire layer's own message.
    fn session(&self) -> Result<Arc<WireClient>, String> {
        Ok(Arc::clone(&self.client))
    }
}

/// Where the core's stderr diagnostics are written. Next to the app's data
/// (`%LOCALAPPDATA%\Tenebra` on Windows), falling back to the temp dir, so a
/// user hitting a problem has one file to share.
fn core_log_path() -> Option<PathBuf> {
    let base = std::env::var_os("LOCALAPPDATA")
        .map(PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    let dir = base.join("Tenebra");
    std::fs::create_dir_all(&dir).ok()?;
    Some(dir.join("core.log"))
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
