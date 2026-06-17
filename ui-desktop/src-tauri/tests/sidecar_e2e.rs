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

    // 4) set_split exclude -> the core normalizes the app names (lowercases,
    //    de-duplicates, sorts) and echoes them back in the State. This is the
    //    same framing SidecarBackend::set_split uses.
    let split = serde_json::json!({
        "id": 4,
        "cmd": "set_split",
        "mode": "exclude",
        "apps": ["Chrome.exe", "steam.exe", "chrome.exe"],
    });
    core.send(&serde_json::to_string(&split).unwrap());
    let split_resp = core.next_response();
    assert_eq!(split_resp["id"].as_i64(), Some(4), "set_split id mismatch");
    assert_eq!(
        split_resp["ok"].as_bool(),
        Some(true),
        "set_split failed: {split_resp}"
    );
    assert_eq!(
        split_resp["data"]["split"].as_str(),
        Some("exclude"),
        "expected exclude split, got {}",
        split_resp["data"]
    );
    let apps: Vec<&str> = split_resp["data"]["split_apps"]
        .as_array()
        .expect("split_apps array")
        .iter()
        .filter_map(|a| a.as_str())
        .collect();
    assert_eq!(
        apps,
        vec!["chrome.exe", "steam.exe"],
        "split_apps not normalized: {split_resp}"
    );

    // 5) status -> the split is reflected in the reported state.
    core.send(r#"{"id":5,"cmd":"status"}"#);
    let after = core.next_response();
    assert_eq!(after["id"].as_i64(), Some(5), "status id mismatch");
    assert_eq!(
        after["data"]["split"].as_str(),
        Some("exclude"),
        "status did not reflect the split: {after}"
    );

    // 6) set_split off -> the split clears (the fields are omitted entirely).
    core.send(r#"{"id":6,"cmd":"set_split","mode":"off"}"#);
    let off = core.next_response();
    assert_eq!(off["id"].as_i64(), Some(6), "set_split off id mismatch");
    assert_eq!(
        off["ok"].as_bool(),
        Some(true),
        "set_split off failed: {off}"
    );
    assert!(
        off["data"].get("split").is_none(),
        "off should omit split, got {}",
        off["data"]
    );

    // 7) leak_check -> the core runs the IP/DNS probes itself and returns the
    //    assembled verdict. This proves the command is dispatched (a missing case
    //    would come back as ok:false "unknown command") and that the result has
    //    the documented shape SidecarBackend::leak_check deserializes. We assert
    //    only the structure and the honesty invariants, not the live values: the
    //    test runs on an idle store, so it is not connected, and whether the
    //    public-IP/DNS echoes are reachable from CI is irrelevant to correctness.
    core.send(r#"{"id":7,"cmd":"leak_check"}"#);
    let leak = core.next_response();
    assert_eq!(leak["id"].as_i64(), Some(7), "leak_check id mismatch");
    assert_eq!(
        leak["ok"].as_bool(),
        Some(true),
        "leak_check failed (is it dispatched?): {leak}"
    );
    let data = &leak["data"];
    // Idle store: the core must report not-connected, with no exit verdict.
    assert_eq!(
        data["connected"].as_bool(),
        Some(false),
        "expected not-connected on an idle store, got {data}"
    );
    // The IP finding always carries a verdict from the known set and a message.
    let ip_verdict = data["ip_verdict"]
        .as_str()
        .unwrap_or_else(|| panic!("leak_check has no ip_verdict: {data}"));
    assert!(
        ["ok", "warn", "neutral", "error"].contains(&ip_verdict),
        "unexpected ip_verdict {ip_verdict:?}: {data}"
    );
    assert!(
        data["ip_message"].as_str().is_some_and(|m| !m.is_empty()),
        "leak_check ip_message missing/empty: {data}"
    );
    // The DNS block must be present with a status from the known set and a
    // message — and it must never be a fabricated pass: on a store with no
    // tunnel the honest outcomes are inconclusive or unavailable, never "ok".
    let dns_status = data["dns"]["status"]
        .as_str()
        .unwrap_or_else(|| panic!("leak_check dns has no status: {data}"));
    assert!(
        ["ok", "leak", "inconclusive", "unavailable"].contains(&dns_status),
        "unexpected dns status {dns_status:?}: {data}"
    );
    assert_ne!(
        dns_status, "ok",
        "DNS reported a pass on an idle store — that would be dishonest: {data}"
    );
    assert!(
        data["dns"]["message"]
            .as_str()
            .is_some_and(|m| !m.is_empty()),
        "leak_check dns message missing/empty: {data}"
    );

    eprintln!(
        "OK: status/import_link/list_profiles/set_split/leak_check round-tripped against the real core"
    );
}
