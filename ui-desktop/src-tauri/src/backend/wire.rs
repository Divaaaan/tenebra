//! The transport-agnostic control-protocol client.
//!
//! The core speaks line-delimited JSON over a byte stream; which byte stream —
//! a sidecar's stdin/stdout or the service's named pipe — is the transport's
//! business, not the protocol's (the core draws the same line, see
//! `docs/control-protocol.md`). This module owns everything above the stream:
//! encoding requests, correlating responses by `id`, forwarding id-less events
//! to the UI, and failing callers fast when the stream dies.
//!
//! A transport hands its write half to [`WireClient::new`] and drives
//! [`read_loop`] with the read half on a thread it owns. Every [`Backend`]
//! method is provided by the blanket impl at the bottom for any type that can
//! produce the current [`WireClient`] via [`WireSession`], so the sidecar and
//! pipe backends share one protocol mapping.

use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::de::DeserializeOwned;
use serde_json::{json, Value};

use super::{
    Backend, EventSink, ImportLinksResult, LeakCheck, PingResult, Profile, RoutingMode, SplitMode,
    State, TunStack, EVENT_LOG, EVENT_PROFILES, EVENT_STATE, EVENT_TRAFFIC,
};

/// How long a request waits for its correlated response before giving up. The
/// core answers control commands locally and fast; the few that touch the
/// network (subscription fetch, ping) bound themselves, so a generous ceiling
/// here only guards against the core wedging or dying without notice.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(60);

/// A response delivered back to a waiting request: `Ok(data)` for a successful
/// reply (the raw `data` payload, or `null` when the command returns nothing),
/// `Err(message)` for a protocol-level error.
pub type ReplyResult = Result<Value, String>;
type Pending = Arc<Mutex<HashMap<u64, Sender<ReplyResult>>>>;

/// One live protocol session over some byte stream: the write half plus the
/// request-correlation state the reader completes. Created per connection; a
/// client that reconnects builds a fresh one per session.
pub struct WireClient {
    /// The stream's write half, guarded so concurrent command calls can't
    /// interleave two half-written lines.
    writer: Mutex<Box<dyn Write + Send>>,
    /// In-flight requests awaiting a response, keyed by request id.
    pending: Pending,
    /// Monotonic request-id source. Starts at 1 so ids match the protocol's
    /// examples and never collide with the "unknown id" 0 the core uses for a
    /// malformed line.
    next_id: AtomicU64,
    /// Set once the stream is gone (reader hit EOF/error, or the owner closed
    /// the session); further requests fail fast instead of blocking until the
    /// timeout.
    closed: AtomicBool,
}

impl WireClient {
    /// Wrap the write half of a connected stream. The caller must run
    /// [`read_loop`] with the matching read half for responses and events to
    /// flow.
    pub fn new(writer: impl Write + Send + 'static) -> Arc<Self> {
        Arc::new(Self {
            writer: Mutex::new(Box::new(writer)),
            pending: Arc::new(Mutex::new(HashMap::new())),
            next_id: AtomicU64::new(1),
            closed: AtomicBool::new(false),
        })
    }

    /// Mark the session closed and fail every in-flight request immediately.
    /// Idempotent. The reader calls this on every exit path; owners call it
    /// when tearing a session down so no caller waits out the full timeout.
    pub fn close(&self) {
        self.closed.store(true, Ordering::SeqCst);
        fail_all_pending(&self.pending);
    }

    /// Send one request line and block for the correlated response. `params`
    /// must serialize to a JSON object; the `id` and `cmd` are spliced in. The
    /// returned value is the response's `data` payload (or `null`).
    pub fn request(&self, cmd: &str, params: Value) -> Result<Value, String> {
        if self.closed.load(Ordering::SeqCst) {
            return Err("the connection to tenebra-core is closed".into());
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
                Err("the connection to tenebra-core closed before a response arrived".into())
            }
        }
    }

    /// A typed request: send and decode the `data` payload into `T`.
    pub fn request_into<T: DeserializeOwned>(&self, cmd: &str, params: Value) -> Result<T, String> {
        let data = self.request(cmd, params)?;
        serde_json::from_value(data)
            .map_err(|e| format!("malformed {cmd} response from tenebra-core: {e}"))
    }

    fn write_line(&self, line: &[u8]) -> Result<(), String> {
        let mut writer = self.writer.lock().unwrap();
        writer
            .write_all(line)
            .and_then(|_| writer.flush())
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

/// Read the stream to EOF, framing by newline. Each complete line is a JSON
/// object: one carrying an `id` completes the matching pending request; one
/// carrying an `event` is forwarded to the UI. When the loop ends — normal EOF,
/// a read error, OR a panic in a sink call unwinding the thread — the client is
/// closed and every still-pending request is failed so no caller hangs out the
/// full timeout. That cleanup lives in `ReaderGuard::drop`, which runs on every
/// exit path including a panic, so the "reader stops ⇒ pending drained + closed
/// set" invariant holds unconditionally.
pub fn read_loop(reader: impl Read, client: Arc<WireClient>, sink: Arc<dyn EventSink>) {
    // Fail-fast cleanup, guaranteed to run even if `forward_event` panics: the
    // guard's Drop fires during unwind, so a sink panic can't leave callers
    // blocked on a reader that's no longer reading.
    let _guard = ReaderGuard {
        client: Arc::clone(&client),
    };

    let reader = BufReader::new(reader);
    // read_line frames on '\n' and buffers partial chunks internally, so a line
    // split across stream reads still arrives whole.
    for line in reader.lines() {
        let line = match line {
            Ok(l) => l,
            Err(_) => break, // the stream broke; treat as the core going away
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
            complete_request(&client.pending, id, &value);
        }
        // Anything else (no id, no event) is unexpected and ignored.
    }
}

/// Runs the reader's close-and-drain on drop so it fires on every exit path,
/// panic included. Closes the client and fails outstanding requests, so a
/// blocked caller returns an error immediately instead of waiting out the
/// timeout when the reader stops for any reason.
struct ReaderGuard {
    client: Arc<WireClient>,
}

impl Drop for ReaderGuard {
    fn drop(&mut self) {
        self.client.close();
    }
}

/// Fail every outstanding request with the "connection gone" error and clear
/// the map. Recovers from a poisoned lock: if a holder panicked mid-mutation we
/// still must drain, a poisoned mutex being no reason to strand callers for the
/// full timeout.
fn fail_all_pending(pending: &Pending) {
    let mut map = match pending.lock() {
        Ok(guard) => guard,
        Err(poisoned) => poisoned.into_inner(),
    };
    for (_, tx) in map.drain() {
        let _ = tx.send(Err(
            "the connection to tenebra-core closed before a response arrived".into(),
        ));
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
        Some(EVENT_PROFILES) => {
            // Signal-only: the body (if any) is ignored; the UI re-fetches.
            sink.profiles();
        }
        _ => {} // unknown event kind: ignore rather than guess
    }
}

/// Drop `None` values so optional request fields are simply absent, matching the
/// protocol (the core treats a missing field and an empty one the same, but
/// omitting keeps lines minimal and the wire form clean).
pub fn obj(pairs: impl IntoIterator<Item = (&'static str, Value)>) -> Value {
    let mut map = serde_json::Map::new();
    for (k, v) in pairs {
        if !v.is_null() {
            map.insert(k.into(), v);
        }
    }
    Value::Object(map)
}

/// Access to the current protocol session. A transport backend implements just
/// this — a stable session for the sidecar, the live one (or an error while
/// reconnecting) for the pipe — and the blanket impl below turns it into a full
/// [`Backend`], so the command-to-wire mapping exists exactly once.
pub trait WireSession: Send + Sync + 'static {
    fn session(&self) -> Result<Arc<WireClient>, String>;
}

impl<T: WireSession> Backend for T {
    fn status(&self) -> Result<State, String> {
        self.session()?.request_into("status", obj([]))
    }

    fn list_profiles(&self) -> Result<Vec<Profile>, String> {
        let wrap: ProfileList = self.session()?.request_into("list_profiles", obj([]))?;
        Ok(wrap.profiles)
    }

    fn import_subscription(&self, url: String, name: String) -> Result<Profile, String> {
        let wrap: ProfileWrap = self.session()?.request_into(
            "import_subscription",
            obj([("url", json!(url)), ("name", json!(name))]),
        )?;
        Ok(wrap.profile)
    }

    fn import_link(&self, link: String, name: Option<String>) -> Result<Profile, String> {
        let wrap: ProfileWrap = self.session()?.request_into(
            "import_link",
            obj([
                ("link", json!(link)),
                ("name", name.map(Value::from).unwrap_or(Value::Null)),
            ]),
        )?;
        Ok(wrap.profile)
    }

    fn import_links(
        &self,
        links: Vec<String>,
        name: Option<String>,
    ) -> Result<ImportLinksResult, String> {
        // The core returns {profile, imported, skipped} directly (not wrapped in a
        // `profile` envelope like the single-import paths), so deserialize the
        // whole data payload into the result.
        self.session()?.request_into(
            "import_links",
            obj([
                ("links", json!(links)),
                ("name", name.map(Value::from).unwrap_or(Value::Null)),
            ]),
        )
    }

    fn remove_profile(&self, profile: String) -> Result<(), String> {
        self.session()?
            .request("remove_profile", obj([("profile", json!(profile))]))?;
        Ok(())
    }

    fn refresh_subscription(&self, profile: String) -> Result<Profile, String> {
        let wrap: ProfileWrap = self
            .session()?
            .request_into("refresh_subscription", obj([("profile", json!(profile))]))?;
        Ok(wrap.profile)
    }

    fn connect(&self, profile: String, node: Option<String>, auto: bool) -> Result<State, String> {
        // `auto` is sent only when set: omitting it (the common case) keeps the
        // line minimal and the core defaults a missing field to false, the
        // original protocol-fallback behaviour. With an explicit node the core
        // ignores it, so there is no need to special-case that here.
        self.session()?.request_into(
            "connect",
            obj([
                ("profile", json!(profile)),
                ("node", node.map(Value::from).unwrap_or(Value::Null)),
                ("auto", if auto { json!(true) } else { Value::Null }),
            ]),
        )
    }

    fn disconnect(&self) -> Result<State, String> {
        self.session()?.request_into("disconnect", obj([]))
    }

    fn ping(&self, profile: String) -> Result<Vec<PingResult>, String> {
        let wrap: PingList = self
            .session()?
            .request_into("ping", obj([("profile", json!(profile))]))?;
        Ok(wrap.results)
    }

    fn set_routing(&self, mode: RoutingMode) -> Result<State, String> {
        let mode = match mode {
            RoutingMode::Smart => "smart",
            RoutingMode::Global => "global",
            RoutingMode::Direct => "direct",
        };
        self.session()?
            .request_into("set_routing", obj([("mode", json!(mode))]))
    }

    fn set_split(&self, mode: SplitMode, apps: Vec<String>) -> Result<State, String> {
        let mode = match mode {
            SplitMode::Off => "off",
            SplitMode::Exclude => "exclude",
            SplitMode::Include => "include",
        };
        self.session()?.request_into(
            "set_split",
            obj([("mode", json!(mode)), ("apps", json!(apps))]),
        )
    }

    fn set_kill_switch(&self, on: bool) -> Result<State, String> {
        self.session()?
            .request_into("set_kill_switch", obj([("on", json!(on))]))
    }

    fn set_tun(&self, stack: TunStack) -> Result<State, String> {
        let stack = match stack {
            TunStack::System => "system",
            TunStack::Gvisor => "gvisor",
            TunStack::Mixed => "mixed",
        };
        self.session()?
            .request_into("set_tun", obj([("stack", json!(stack))]))
    }

    fn set_autoconnect(&self, on: bool) -> Result<State, String> {
        self.session()?
            .request_into("set_autoconnect", obj([("on", json!(on))]))
    }

    fn set_dns(
        &self,
        ad_block: bool,
        dns_remote: String,
        dns_direct: String,
        ipv4_only: bool,
    ) -> Result<State, String> {
        self.session()?.request_into(
            "set_dns",
            obj([
                ("ad_block", json!(ad_block)),
                ("dns_remote", json!(dns_remote)),
                ("dns_direct", json!(dns_direct)),
                ("ipv4_only", json!(ipv4_only)),
            ]),
        )
    }

    fn leak_check(&self) -> Result<LeakCheck, String> {
        // The core runs the IP/DNS probes itself and returns the assembled
        // verdict; we just deserialize it. It can touch the network, but the core
        // bounds each echo request, so the generous request timeout above is
        // ample.
        self.session()?.request_into("leak_check", obj([]))
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

#[cfg(test)]
mod tests {
    //! Exercises the framing/correlation machinery in isolation — no process,
    //! no OS pipe. The transports layer their streams on top of this; the
    //! sidecar round-trip against the real core is covered by
    //! `tests/sidecar_e2e.rs`, and the pipe transport's stream handling by the
    //! tests in `pipe.rs`.

    use super::super::testutil::{duplex, Rec};
    use super::super::ConnectionState;
    use super::*;
    use std::sync::atomic::AtomicBool;
    use std::thread;

    /// Parse the bytes `build_request` produced back into a `Value`, asserting the
    /// trailing newline framing along the way.
    fn parse_line(bytes: &[u8]) -> Value {
        assert_eq!(bytes.last(), Some(&b'\n'), "request line must end in '\\n'");
        serde_json::from_slice(&bytes[..bytes.len() - 1]).expect("request line is JSON")
    }

    #[test]
    fn build_request_merges_id_and_cmd_into_params() {
        let line = build_request(7, "connect", json!({ "profile": "p", "node": "n" })).unwrap();
        let value = parse_line(&line);
        assert_eq!(
            value,
            json!({ "id": 7, "cmd": "connect", "profile": "p", "node": "n" })
        );
    }

    #[test]
    fn build_request_treats_null_params_as_empty() {
        let line = build_request(1, "status", Value::Null).unwrap();
        let value = parse_line(&line);
        assert_eq!(value, json!({ "id": 1, "cmd": "status" }));
    }

    #[test]
    fn build_request_rejects_non_object_params() {
        let err = build_request(1, "x", json!("notanobject")).unwrap_err();
        assert!(err.contains("must be an object"), "unexpected error: {err}");
    }

    #[test]
    fn obj_drops_null_values() {
        let value = obj([("a", json!(1)), ("b", Value::Null), ("c", json!("x"))]);
        assert_eq!(value, json!({ "a": 1, "c": "x" }));
        assert_eq!(obj([]), json!({}));
    }

    /// Build a `Pending` map and register a waiter for `id`, returning the
    /// receiver so the test can read what `complete_request` delivers.
    fn pending_with(id: u64) -> (Pending, Receiver<ReplyResult>) {
        let pending: Pending = Arc::new(Mutex::new(HashMap::new()));
        let (tx, rx) = mpsc::channel();
        pending.lock().unwrap().insert(id, tx);
        (pending, rx)
    }

    fn recv(rx: &Receiver<ReplyResult>) -> ReplyResult {
        rx.recv_timeout(Duration::from_secs(1))
            .expect("a reply within the timeout")
    }

    #[test]
    fn complete_request_delivers_ok_data() {
        let (pending, rx) = pending_with(5);
        complete_request(
            &pending,
            5,
            &json!({ "id": 5, "ok": true, "data": { "x": 1 } }),
        );
        assert_eq!(recv(&rx), Ok(json!({ "x": 1 })));
        // The waiter is consumed once delivered.
        assert!(pending.lock().unwrap().is_empty());
    }

    #[test]
    fn complete_request_ok_without_data_is_null() {
        let (pending, rx) = pending_with(5);
        complete_request(&pending, 5, &json!({ "id": 5, "ok": true }));
        assert_eq!(recv(&rx), Ok(Value::Null));
    }

    #[test]
    fn complete_request_error_carries_message() {
        let (pending, rx) = pending_with(5);
        complete_request(
            &pending,
            5,
            &json!({ "id": 5, "ok": false, "error": "boom" }),
        );
        assert_eq!(recv(&rx), Err("boom".to_string()));
    }

    #[test]
    fn complete_request_error_without_message_falls_back() {
        let (pending, rx) = pending_with(5);
        complete_request(&pending, 5, &json!({ "id": 5, "ok": false }));
        assert_eq!(recv(&rx), Err("tenebra-core reported an error".to_string()));
    }

    #[test]
    fn complete_request_unknown_id_is_a_noop() {
        let (pending, _rx) = pending_with(5);
        // No waiter for id 9; must not panic and must leave id 5 untouched.
        complete_request(&pending, 9, &json!({ "id": 9, "ok": true, "data": null }));
        assert!(pending.lock().unwrap().contains_key(&5));
    }

    #[test]
    fn forward_event_routes_state() {
        let sink = Rec::default();
        forward_event(&json!({ "event": "state", "state": "connected" }), &sink);
        let states = sink.states.lock().unwrap();
        assert_eq!(states.len(), 1);
        assert_eq!(states[0].state, ConnectionState::Connected);
    }

    #[test]
    fn forward_event_routes_traffic() {
        let sink = Rec::default();
        forward_event(
            &json!({ "event": "traffic", "up": 1, "down": 2, "up_rate": 3, "down_rate": 4 }),
            &sink,
        );
        assert_eq!(*sink.traffic.lock().unwrap(), vec![(1, 2, 3, 4)]);
    }

    #[test]
    fn forward_event_routes_log() {
        let sink = Rec::default();
        forward_event(
            &json!({ "event": "log", "level": "warn", "msg": "hi" }),
            &sink,
        );
        assert_eq!(
            *sink.logs.lock().unwrap(),
            vec![("warn".to_string(), "hi".to_string())]
        );
    }

    #[test]
    fn forward_event_routes_profiles_signal() {
        let sink = Rec::default();
        forward_event(&json!({ "event": "profiles" }), &sink);
        assert_eq!(sink.profiles.load(Ordering::SeqCst), 1);
    }

    #[test]
    fn forward_event_ignores_unknown_kind() {
        let sink = Rec::default();
        forward_event(&json!({ "event": "bogus" }), &sink);
        assert!(sink.states.lock().unwrap().is_empty());
        assert!(sink.traffic.lock().unwrap().is_empty());
        assert!(sink.logs.lock().unwrap().is_empty());
        assert_eq!(sink.profiles.load(Ordering::SeqCst), 0);
    }

    #[test]
    fn forward_event_drops_a_malformed_state() {
        let sink = Rec::default();
        // An unparseable state (bad discriminant) is swallowed, not forwarded.
        forward_event(&json!({ "event": "state", "state": "???" }), &sink);
        assert!(sink.states.lock().unwrap().is_empty());
    }

    #[test]
    fn request_round_trips_over_an_in_memory_stream() {
        // The full path: request encoded and written, response line framed and
        // correlated back — over an in-memory duplex, exactly as a transport
        // would wire it.
        let stop = Arc::new(AtomicBool::new(false));
        let (ours, theirs) = duplex(&stop);

        let client = WireClient::new(ours.writer);
        let sink: Arc<dyn EventSink> = Arc::new(Rec::default());
        let reader_client = Arc::clone(&client);
        let reader = thread::spawn(move || read_loop(ours.reader, reader_client, sink));

        // A minimal core: read the one request, echo its id back with a state.
        let mut server_writer = theirs.writer;
        let server = thread::spawn(move || {
            let mut lines = BufReader::new(theirs.reader).lines();
            let line = lines.next().expect("a request line").expect("readable");
            let req: Value = serde_json::from_str(&line).expect("request is JSON");
            assert_eq!(req["cmd"].as_str(), Some("status"));
            let id = req["id"].as_u64().expect("request carries an id");
            let response = json!({ "id": id, "ok": true, "data": { "state": "idle" } });
            writeln!(server_writer, "{response}").expect("write response");
        });

        let state: State = client.request_into("status", obj([])).expect("a response");
        assert_eq!(state.state, ConnectionState::Idle);

        server.join().expect("server thread");
        stop.store(true, Ordering::SeqCst); // unstick the reader
        reader.join().expect("reader thread");
    }

    /// A [`WireSession`] over a fixed client, so a test can exercise the
    /// blanket `Backend` impl — the command-to-wire mapping — rather than
    /// calling the client directly.
    struct FixedSession(Arc<WireClient>);

    impl WireSession for FixedSession {
        fn session(&self) -> Result<Arc<WireClient>, String> {
            Ok(Arc::clone(&self.0))
        }
    }

    #[test]
    fn set_autoconnect_maps_to_the_protocol_command() {
        // Drive Backend::set_autoconnect through the blanket impl over an
        // in-memory duplex and assert the exact line it puts on the wire, plus
        // that the core's reported state (autoconnect echoed back) round-trips.
        let stop = Arc::new(AtomicBool::new(false));
        let (ours, theirs) = duplex(&stop);

        let client = WireClient::new(ours.writer);
        let sink: Arc<dyn EventSink> = Arc::new(Rec::default());
        let reader_client = Arc::clone(&client);
        let reader = thread::spawn(move || read_loop(ours.reader, reader_client, sink));

        let mut server_writer = theirs.writer;
        let server = thread::spawn(move || {
            let mut lines = BufReader::new(theirs.reader).lines();
            let line = lines.next().expect("a request line").expect("readable");
            let req: Value = serde_json::from_str(&line).expect("request is JSON");
            assert_eq!(req["cmd"].as_str(), Some("set_autoconnect"));
            assert_eq!(req["on"].as_bool(), Some(true));
            let id = req["id"].as_u64().expect("request carries an id");
            let response = json!({
                "id": id, "ok": true,
                "data": { "state": "idle", "autoconnect": true },
            });
            writeln!(server_writer, "{response}").expect("write response");
        });

        let backend = FixedSession(Arc::clone(&client));
        let state = backend.set_autoconnect(true).expect("a response");
        assert_eq!(state.state, ConnectionState::Idle);
        assert_eq!(state.autoconnect, Some(true));

        server.join().expect("server thread");
        stop.store(true, Ordering::SeqCst); // unstick the reader
        reader.join().expect("reader thread");
    }

    #[test]
    fn set_dns_maps_to_the_protocol_command() {
        // Drive Backend::set_dns through the blanket impl and assert the exact line
        // it puts on the wire (the two toggles plus both resolvers), plus that the
        // core's reported DNS state round-trips back.
        let stop = Arc::new(AtomicBool::new(false));
        let (ours, theirs) = duplex(&stop);

        let client = WireClient::new(ours.writer);
        let sink: Arc<dyn EventSink> = Arc::new(Rec::default());
        let reader_client = Arc::clone(&client);
        let reader = thread::spawn(move || read_loop(ours.reader, reader_client, sink));

        let mut server_writer = theirs.writer;
        let server = thread::spawn(move || {
            let mut lines = BufReader::new(theirs.reader).lines();
            let line = lines.next().expect("a request line").expect("readable");
            let req: Value = serde_json::from_str(&line).expect("request is JSON");
            assert_eq!(req["cmd"].as_str(), Some("set_dns"));
            assert_eq!(req["ad_block"].as_bool(), Some(true));
            assert_eq!(req["dns_remote"].as_str(), Some("tls://9.9.9.9"));
            assert_eq!(req["dns_direct"].as_str(), Some("udp://8.8.8.8"));
            assert_eq!(req["ipv4_only"].as_bool(), Some(true));
            let id = req["id"].as_u64().expect("request carries an id");
            let response = json!({
                "id": id, "ok": true,
                "data": {
                    "state": "idle",
                    "ad_block": true,
                    "dns_remote": "tls://9.9.9.9",
                    "dns_direct": "udp://8.8.8.8",
                    "ipv4_only": true,
                },
            });
            writeln!(server_writer, "{response}").expect("write response");
        });

        let backend = FixedSession(Arc::clone(&client));
        let state = backend
            .set_dns(true, "tls://9.9.9.9".into(), "udp://8.8.8.8".into(), true)
            .expect("a response");
        assert_eq!(state.ad_block, Some(true));
        assert_eq!(state.dns_remote.as_deref(), Some("tls://9.9.9.9"));
        assert_eq!(state.dns_direct.as_deref(), Some("udp://8.8.8.8"));
        assert_eq!(state.ipv4_only, Some(true));

        server.join().expect("server thread");
        stop.store(true, Ordering::SeqCst); // unstick the reader
        reader.join().expect("reader thread");
    }

    #[test]
    fn request_fails_fast_once_closed() {
        let client = WireClient::new(Vec::new());
        client.close();
        let err = client.request("status", obj([])).unwrap_err();
        assert!(err.contains("closed"), "unexpected error: {err}");
    }

    #[test]
    fn close_drains_in_flight_requests() {
        let client = WireClient::new(Vec::new());
        let (tx, rx) = mpsc::channel();
        client.pending.lock().unwrap().insert(1, tx);
        client.close();
        assert_eq!(
            rx.recv_timeout(Duration::from_secs(1)),
            Ok(Err(
                "the connection to tenebra-core closed before a response arrived".to_string()
            ))
        );
        assert!(client.pending.lock().unwrap().is_empty());
    }

    /// A sink that panics the first time the reader forwards a state event,
    /// standing in for any Tauri sink call (`app.emit`, tray) blowing up on the
    /// reader thread.
    struct PanickingSink;

    impl EventSink for PanickingSink {
        fn state(&self, _state: &State) {
            panic!("sink boom");
        }
        fn traffic(&self, _up: u64, _down: u64, _up_rate: u64, _down_rate: u64) {}
        fn log(&self, _level: &str, _msg: &str) {}
        fn profiles(&self) {}
    }

    #[test]
    fn read_loop_drains_pending_when_a_sink_panics() {
        let client = WireClient::new(Vec::new());

        // A caller is blocked waiting on id 1, exactly as `request` would leave
        // it.
        let (tx, rx) = mpsc::channel();
        client.pending.lock().unwrap().insert(1, tx);

        // One line: a state event whose dispatch will panic inside the sink.
        let stream = b"{\"event\":\"state\",\"state\":\"connected\"}\n".to_vec();
        let sink: Arc<dyn EventSink> = Arc::new(PanickingSink);

        // The reader thread would unwind here; the panic-safety guarantee is that
        // its cleanup still runs. Swallow the backtrace noise for a clean run.
        let prev = std::panic::take_hook();
        std::panic::set_hook(Box::new(|_| {}));
        let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            read_loop(&stream[..], Arc::clone(&client), sink);
        }));
        std::panic::set_hook(prev);

        assert!(
            result.is_err(),
            "the sink panic must propagate, not be hidden"
        );
        // Despite the panic: the client is closed and the pending caller is
        // failed immediately instead of hanging out the 60s timeout.
        assert!(
            client.closed.load(Ordering::SeqCst),
            "closed must be set even when the reader unwinds"
        );
        assert_eq!(
            rx.recv_timeout(Duration::from_secs(1)),
            Ok(Err(
                "the connection to tenebra-core closed before a response arrived".to_string()
            )),
            "the in-flight request must be drained on a reader panic"
        );
        assert!(
            client.pending.lock().unwrap().is_empty(),
            "pending must be empty after the drain"
        );
    }
}
