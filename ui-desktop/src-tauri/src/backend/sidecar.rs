//! Real backend: a thin client over the `tenebra-core` sidecar.
//!
//! The core owns sing-box and the tunnel; this type owns the core process and
//! speaks the line-delimited JSON control protocol to it over stdin/stdout (see
//! `docs/control-protocol.md`). Every [`Backend`] method turns into one request
//! line on the child's stdin and blocks on the correlated response; a background
//! reader frames the child's stdout by newline, completes pending requests by
//! `id`, and forwards id-less events to the webview through the same
//! [`EventSink`] the mock uses.
//!
//! Spawning uses `std::process` rather than the shell plugin's `sidecar()`
//! helper: that helper is async and tied to an `AppHandle`, whereas the
//! `Backend` trait is synchronous and the integration test drives this type
//! without a Tauri app at all. Resolving the externalBin path ourselves keeps
//! the same binary working in both the GUI and a headless test, and the test is
//! the contract we verify. The path resolution mirrors Tauri's externalBin
//! naming (`tenebra-core-<target-triple>`), with a plain `tenebra-core` and the
//! repo's build output as fallbacks.

use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use serde::de::DeserializeOwned;
use serde_json::{json, Value};

use super::{
    Backend, EventSink, LeakCheck, PingResult, Profile, RoutingMode, State, EVENT_LOG, EVENT_STATE,
    EVENT_TRAFFIC,
};

/// How long a request waits for its correlated response before giving up. The
/// core answers control commands locally and fast; the few that touch the
/// network (subscription fetch, ping) bound themselves, so a generous ceiling
/// here only guards against the child wedging or dying without notice.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(60);

/// A response delivered back to a waiting request: `Ok(data)` for a successful
/// reply (the raw `data` payload, or `null` when the command returns nothing),
/// `Err(message)` for a protocol-level error.
type ReplyResult = Result<Value, String>;
type Pending = Arc<Mutex<HashMap<u64, Sender<ReplyResult>>>>;

/// Client over a running `tenebra-core` child process.
pub struct SidecarBackend {
    inner: Arc<Shared>,
}

/// State shared between the public façade and the background reader thread.
struct Shared {
    /// The child's stdin, guarded so concurrent command calls can't interleave
    /// two half-written lines.
    stdin: Mutex<ChildStdin>,
    /// In-flight requests awaiting a response, keyed by request id.
    pending: Pending,
    /// Monotonic request-id source. Starts at 1 so ids match the protocol's
    /// examples and never collide with the "unknown id" 0 the core uses for a
    /// malformed line.
    next_id: AtomicU64,
    /// Set once the child has exited or its stdout closed; further requests fail
    /// fast instead of blocking until the timeout.
    closed: AtomicBool,
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
        let mut child = Command::new(&program)
            .env("TENEBRA_SINGBOX", singbox_path.into())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            // Leave stderr inherited: the core logs diagnostics there and they
            // are useful in the console / test output, never on the protocol
            // channel.
            .stderr(Stdio::inherit())
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

        let pending: Pending = Arc::new(Mutex::new(HashMap::new()));
        let shared = Arc::new(Shared {
            stdin: Mutex::new(stdin),
            pending: Arc::clone(&pending),
            next_id: AtomicU64::new(1),
            closed: AtomicBool::new(false),
            child: Mutex::new(child),
        });

        let reader_shared = Arc::clone(&shared);
        thread::Builder::new()
            .name("tenebra-core-reader".into())
            .spawn(move || read_loop(stdout, reader_shared, sink))
            .map_err(|e| format!("failed to start core reader thread: {e}"))?;

        Ok(Self { inner: shared })
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

impl Drop for SidecarBackend {
    fn drop(&mut self) {
        // Closing stdin lets the core's Serve loop return on EOF and tear the
        // tunnel down cleanly; killing the child is the backstop if it doesn't.
        self.inner.closed.store(true, Ordering::SeqCst);
        if let Ok(mut child) = self.inner.child.lock() {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

impl Shared {
    /// Send one request line and block for the correlated response. `params`
    /// must serialize to a JSON object; the `id` and `cmd` are spliced in. The
    /// returned value is the response's `data` payload (or `null`).
    fn request(&self, cmd: &str, params: Value) -> Result<Value, String> {
        if self.closed.load(Ordering::SeqCst) {
            return Err("tenebra-core is not running".into());
        }

        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let line = build_request(id, cmd, params)?;

        let (tx, rx): (Sender<ReplyResult>, Receiver<ReplyResult>) = mpsc::channel();
        self.pending.lock().unwrap().insert(id, tx);

        if let Err(e) = self.write_line(&line) {
            self.pending.lock().unwrap().remove(&id);
            return Err(e);
        }

        match rx.recv_timeout(REQUEST_TIMEOUT) {
            Ok(reply) => reply,
            Err(mpsc::RecvTimeoutError::Timeout) => {
                self.pending.lock().unwrap().remove(&id);
                Err(format!("tenebra-core did not respond to {cmd} in time"))
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                self.pending.lock().unwrap().remove(&id);
                Err("tenebra-core exited before responding".into())
            }
        }
    }

    /// A typed request: send and decode the `data` payload into `T`.
    fn request_into<T: DeserializeOwned>(&self, cmd: &str, params: Value) -> Result<T, String> {
        let data = self.request(cmd, params)?;
        serde_json::from_value(data)
            .map_err(|e| format!("malformed {cmd} response from tenebra-core: {e}"))
    }

    fn write_line(&self, line: &[u8]) -> Result<(), String> {
        let mut stdin = self.stdin.lock().unwrap();
        stdin
            .write_all(line)
            .and_then(|_| stdin.flush())
            .map_err(|e| format!("failed to send request to tenebra-core: {e}"))
    }
}

/// Serialize `params` (an object) into a single request line with `id`/`cmd`
/// merged in, terminated by a newline.
fn build_request(id: u64, cmd: &str, params: Value) -> Result<Vec<u8>, String> {
    let mut obj = match params {
        Value::Object(map) => map,
        Value::Null => serde_json::Map::new(),
        other => {
            return Err(format!(
                "internal: request params for {cmd} must be an object, got {other}"
            ))
        }
    };
    obj.insert("id".into(), json!(id));
    obj.insert("cmd".into(), json!(cmd));
    let mut line = serde_json::to_vec(&Value::Object(obj))
        .map_err(|e| format!("failed to encode {cmd} request: {e}"))?;
    line.push(b'\n');
    Ok(line)
}

/// Read the child's stdout to EOF, framing by newline. Each complete line is a
/// JSON object: one carrying an `id` completes the matching pending request; one
/// carrying an `event` is forwarded to the UI. On EOF or error the loop marks
/// the backend closed and fails every still-pending request so no caller hangs.
fn read_loop(stdout: impl std::io::Read, shared: Arc<Shared>, sink: Arc<dyn EventSink>) {
    let reader = BufReader::new(stdout);
    // read_line frames on '\n' and buffers partial chunks internally, so a line
    // split across OS reads still arrives whole.
    for line in reader.lines() {
        let line = match line {
            Ok(l) => l,
            Err(_) => break, // stdout broke; treat as the child going away
        };
        let trimmed = line.trim();
        if trimmed.is_empty() {
            continue;
        }
        let value: Value = match serde_json::from_str(trimmed) {
            Ok(v) => v,
            Err(_) => {
                // A non-JSON line on the protocol channel is a core bug; surface
                // it as a log rather than crashing the reader.
                sink.log(
                    "warn",
                    &format!("ignored non-JSON line from core: {trimmed}"),
                );
                continue;
            }
        };

        if value.get("event").is_some() {
            forward_event(&value, sink.as_ref());
        } else if let Some(id) = value.get("id").and_then(Value::as_u64) {
            complete_request(&shared.pending, id, &value);
        }
        // Anything else (no id, no event) is unexpected and ignored.
    }

    // Stdout closed: the core is gone. Fail outstanding requests so blocked
    // callers return an error instead of waiting out the timeout.
    shared.closed.store(true, Ordering::SeqCst);
    let mut pending = shared.pending.lock().unwrap();
    for (_, tx) in pending.drain() {
        let _ = tx.send(Err("tenebra-core exited before responding".into()));
    }
}

/// Hand a response back to the request waiting on `id`. A `data` field (or its
/// absence, for commands that return nothing) becomes `Ok`; an `error` becomes
/// `Err`. Responses for unknown ids (e.g. id 0 for a line the core could not
/// parse) have no waiter and are dropped.
fn complete_request(pending: &Pending, id: u64, value: &Value) {
    let tx = match pending.lock().unwrap().remove(&id) {
        Some(tx) => tx,
        None => return,
    };
    let ok = value.get("ok").and_then(Value::as_bool).unwrap_or(false);
    let reply = if ok {
        Ok(value.get("data").cloned().unwrap_or(Value::Null))
    } else {
        let msg = value
            .get("error")
            .and_then(Value::as_str)
            .unwrap_or("tenebra-core reported an error")
            .to_string();
        Err(msg)
    };
    let _ = tx.send(reply);
}

/// Forward a protocol event to the webview on the same channel the mock uses, so
/// the front-end listeners don't care which backend produced it. Events are flat
/// objects: `{"event":"state", ...state fields}`.
fn forward_event(value: &Value, sink: &dyn EventSink) {
    match value.get("event").and_then(Value::as_str) {
        Some(EVENT_STATE) => {
            if let Ok(state) = serde_json::from_value::<State>(value.clone()) {
                sink.state(&state);
            }
        }
        Some(EVENT_TRAFFIC) => {
            let n = |k: &str| value.get(k).and_then(Value::as_u64).unwrap_or(0);
            sink.traffic(n("up"), n("down"), n("up_rate"), n("down_rate"));
        }
        Some(EVENT_LOG) => {
            let level = value.get("level").and_then(Value::as_str).unwrap_or("info");
            let msg = value.get("msg").and_then(Value::as_str).unwrap_or_default();
            sink.log(level, msg);
        }
        _ => {} // unknown event kind: ignore rather than guess
    }
}

/// Drop `None` values so optional request fields are simply absent, matching the
/// protocol (the core treats a missing field and an empty one the same, but
/// omitting keeps lines minimal and the wire form clean).
fn obj(pairs: impl IntoIterator<Item = (&'static str, Value)>) -> Value {
    let mut map = serde_json::Map::new();
    for (k, v) in pairs {
        if !v.is_null() {
            map.insert(k.into(), v);
        }
    }
    Value::Object(map)
}

impl Backend for SidecarBackend {
    fn status(&self) -> Result<State, String> {
        self.inner.request_into("status", obj([]))
    }

    fn list_profiles(&self) -> Result<Vec<Profile>, String> {
        let wrap: ProfileList = self.inner.request_into("list_profiles", obj([]))?;
        Ok(wrap.profiles)
    }

    fn import_subscription(&self, url: String, name: String) -> Result<Profile, String> {
        let wrap: ProfileWrap = self.inner.request_into(
            "import_subscription",
            obj([("url", json!(url)), ("name", json!(name))]),
        )?;
        Ok(wrap.profile)
    }

    fn import_link(&self, link: String, name: Option<String>) -> Result<Profile, String> {
        let wrap: ProfileWrap = self.inner.request_into(
            "import_link",
            obj([
                ("link", json!(link)),
                ("name", name.map(Value::from).unwrap_or(Value::Null)),
            ]),
        )?;
        Ok(wrap.profile)
    }

    fn remove_profile(&self, profile: String) -> Result<(), String> {
        self.inner
            .request("remove_profile", obj([("profile", json!(profile))]))?;
        Ok(())
    }

    fn refresh_subscription(&self, profile: String) -> Result<Profile, String> {
        let wrap: ProfileWrap = self
            .inner
            .request_into("refresh_subscription", obj([("profile", json!(profile))]))?;
        Ok(wrap.profile)
    }

    fn connect(&self, profile: String, node: Option<String>) -> Result<State, String> {
        self.inner.request_into(
            "connect",
            obj([
                ("profile", json!(profile)),
                ("node", node.map(Value::from).unwrap_or(Value::Null)),
            ]),
        )
    }

    fn disconnect(&self) -> Result<State, String> {
        self.inner.request_into("disconnect", obj([]))
    }

    fn ping(&self, profile: String) -> Result<Vec<PingResult>, String> {
        let wrap: PingList = self
            .inner
            .request_into("ping", obj([("profile", json!(profile))]))?;
        Ok(wrap.results)
    }

    fn set_routing(&self, mode: RoutingMode) -> Result<State, String> {
        let mode = match mode {
            RoutingMode::Smart => "smart",
            RoutingMode::Global => "global",
            RoutingMode::Direct => "direct",
        };
        self.inner
            .request_into("set_routing", obj([("mode", json!(mode))]))
    }

    fn leak_check(&self) -> Result<LeakCheck, String> {
        // leak_check has no core-side command in this iteration; the protocol
        // table stops at set_routing. Report it as unsupported rather than
        // sending a command the core will reject as unknown.
        Err("leak_check is not supported by the core yet".into())
    }
}

// Response envelopes mirroring the core's wrapped payloads. The inner `Profile`
// / `PingResult` / `State` types live in `backend::mod` and deserialize from the
// core's JSON (extra node fields the UI doesn't model are ignored).
#[derive(serde::Deserialize)]
struct ProfileList {
    profiles: Vec<Profile>,
}

#[derive(serde::Deserialize)]
struct ProfileWrap {
    profile: Profile,
}

#[derive(serde::Deserialize)]
struct PingList {
    results: Vec<PingResult>,
}
