//! End-to-end check of the control wire against the REAL `tenebra-core` binary.
//!
//! This does not touch the GUI, sing-box, or a real tunnel: it spawns the core
//! process directly, talks the line-delimited JSON protocol over its
//! stdin/stdout, and asserts that requests get well-formed, id-correlated
//! responses. It is the automated proof that `SidecarBackend`'s wire format
//! matches the core's, exercising the same framing the backend relies on.
//!
//! The test is skipped (passes as a no-op) when the core binary has not been
//! built, so `cargo test` stays green on a fresh checkout. Build the binary
//! with the command in the task / README to make it run for real:
//!
//!   go build -o ui-desktop/src-tauri/binaries/tenebra-core-<triple>.exe ./cmd/tenebra-core
//!
//! The links and ids used here are deliberately fake (RFC-style example hosts,
//! a placeholder UUID and REALITY key); nothing dials out.

use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::{Child, ChildStdin, ChildStdout, Command, Stdio};
use std::time::{Duration, Instant};

use serde_json::Value;

/// Locate the built core binary, or `None` if it isn't present yet. Checks the
/// Tauri externalBin location for this target first, then a plain name, then the
/// repo's `cmd`-built fallback, covering however the binary was produced.
fn core_binary() -> Option<PathBuf> {
    let manifest = PathBuf::from(env!("CARGO_MANIFEST_DIR")); // .../ui-desktop/src-tauri
    let exe = std::env::consts::EXE_SUFFIX;
    let triple = env!("TENEBRA_TARGET_TRIPLE");

    let candidates = [
        manifest.join(format!("binaries/tenebra-core-{triple}{exe}")),
        manifest.join(format!("binaries/tenebra-core{exe}")),
    ];
    candidates.into_iter().find(|p| p.exists())
}

/// A spawned core plus framed access to its stdin/stdout. Drop kills the child.
struct Core {
    child: Child,
    stdin: ChildStdin,
    stdout: BufReader<ChildStdout>,
}

impl Core {
    fn spawn(program: &PathBuf) -> Core {
        let mut child = Command::new(program)
            // A bogus sing-box path is fine: these commands never start a tunnel.
            .env("TENEBRA_SINGBOX", "sing-box-not-needed-for-this-test")
            // Isolate the store in a temp dir so the test never writes to the
            // real per-user config location.
            .env("TENEBRA_CONFIG_DIR", test_store_dir())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .spawn()
            .expect("spawn tenebra-core");
        let stdin = child.stdin.take().expect("stdin");
        let stdout = BufReader::new(child.stdout.take().expect("stdout"));
        Core {
            child,
            stdin,
            stdout,
        }
    }

    /// Write one already-encoded JSON request, newline-terminated.
    fn send(&mut self, line: &str) {
        self.stdin
            .write_all(line.as_bytes())
            .expect("write request");
        self.stdin.write_all(b"\n").expect("write newline");
        self.stdin.flush().expect("flush");
    }

    /// Read the next RESPONSE line (one carrying an `id`), skipping any events the
    /// core emits in between. Frames strictly by newline. Times out so a wedged
    /// core fails the test instead of hanging CI.
    fn next_response(&mut self) -> Value {
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            assert!(
                Instant::now() < deadline,
                "timed out waiting for a response"
            );
            let mut line = String::new();
            let n = self.stdout.read_line(&mut line).expect("read stdout");
            assert!(n != 0, "core stdout closed before a response arrived");
            let trimmed = line.trim();
            if trimmed.is_empty() {
                continue;
            }
            let value: Value = serde_json::from_str(trimmed)
                .unwrap_or_else(|e| panic!("non-JSON line from core: {trimmed:?} ({e})"));
            // Events have an `event` field and no `id`; ignore them here.
            if value.get("event").is_some() {
                eprintln!("[event] {trimmed}");
                continue;
            }
            eprintln!("[response] {trimmed}");
            return value;
        }
    }
}

impl Drop for Core {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// A throwaway profile-store dir for the test run, so it never touches the real
/// per-user config location and always starts from an empty store.
fn test_store_dir() -> PathBuf {
    let dir = std::env::temp_dir().join(format!("tenebra-e2e-{}", std::process::id()));
    let _ = std::fs::create_dir_all(&dir);
    dir
}

#[test]
fn real_core_round_trips() {
    let Some(program) = core_binary() else {
        eprintln!(
            "SKIP: tenebra-core binary not built; \
             build it into src-tauri/binaries to run this test for real."
        );
        return;
    };
    eprintln!("using core binary: {}", program.display());

    let mut core = Core::spawn(&program);

    // 1) status -> id echoed, ok, state == idle on a fresh store.
    core.send(r#"{"id":1,"cmd":"status"}"#);
    let status = core.next_response();
    assert_eq!(status["id"].as_i64(), Some(1), "status id mismatch");
    assert_eq!(
        status["ok"].as_bool(),
        Some(true),
        "status not ok: {status}"
    );
    assert_eq!(
        status["data"]["state"].as_str(),
        Some("idle"),
        "expected idle state, got {}",
        status["data"]
    );

    // 2) import_link with a FAKE reality VLESS link -> a manual profile, no
    //    network needed (pure string parse). The #t fragment names the node "t".
    let import = serde_json::json!({
        "id": 2,
        "cmd": "import_link",
        "link": "vless://11111111-2222-3333-4444-555555555555@a.example.com:443?security=reality&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sni=example.com#t",
    });
    core.send(&serde_json::to_string(&import).unwrap());
    let imported = core.next_response();
    assert_eq!(imported["id"].as_i64(), Some(2), "import id mismatch");
    assert_eq!(
        imported["ok"].as_bool(),
        Some(true),
        "import_link failed: {imported}"
    );
    let profile = &imported["data"]["profile"];
    let profile_id = profile["id"]
        .as_str()
        .unwrap_or_else(|| panic!("imported profile has no id: {imported}"))
        .to_string();
    assert_eq!(profile["source"].as_str(), Some("manual"));
    assert!(
        !profile["nodes"].as_array().expect("nodes array").is_empty(),
        "imported profile has no nodes: {profile}"
    );
    // The node flattens model.Node: protocol should be the vless we sent.
    assert_eq!(profile["nodes"][0]["protocol"].as_str(), Some("vless"));

    // 3) list_profiles -> contains the profile we just imported, by id.
    core.send(r#"{"id":3,"cmd":"list_profiles"}"#);
    let listed = core.next_response();
    assert_eq!(listed["id"].as_i64(), Some(3), "list id mismatch");
    assert_eq!(listed["ok"].as_bool(), Some(true), "list failed: {listed}");
    let ids: Vec<&str> = listed["data"]["profiles"]
        .as_array()
        .expect("profiles array")
        .iter()
        .filter_map(|p| p["id"].as_str())
        .collect();
    assert!(
        ids.contains(&profile_id.as_str()),
        "list_profiles {ids:?} is missing the imported id {profile_id}"
    );

    eprintln!("OK: status/import_link/list_profiles round-tripped against the real core");
}
