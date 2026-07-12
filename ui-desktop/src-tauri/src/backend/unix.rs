//! The unix-domain-socket transport: a client of the core running detached from
//! the GUI as a root LaunchDaemon вЂ” `tenebra-core --socket`, which serves the
//! control protocol on `/var/run/tenebra.sock`.
//!
//! The daemon listens on that socket through the same transport-agnostic
//! `ServeListener` the Windows pipe uses (see `core/control/listener.go` and the
//! Transports section of `docs/control-protocol.md`); this type dials it and
//! runs the same [`wire`](super::wire) client the sidecar and pipe backends use.
//! The lifecycle is the pipe's, not the sidecar's: the daemon outlives any one
//! GUI process, so the connection вЂ” not the process вЂ” is the thing to manage,
//! and the unprivileged GUI needs no elevation of its own because the privileged
//! tunnel already lives in the daemon.
//!
//! - **Re-sync on connect.** The socket delivers no backlog of events on attach,
//!   so the state on connect is whatever it already is. Every new session
//!   therefore opens with a `status` request and pushes the answer at the UI.
//! - **Reconnect on loss.** The session ending (the daemon restarted or died, or
//!   another client displaced us вЂ” `ServeListener` keeps exactly one session
//!   live, last-writer-wins) is not fatal: a supervisor thread pushes a synthetic
//!   "reconnecting" state at the UI and redials with capped exponential backoff
//!   until the socket answers again, then re-syncs. Only a loss that outlasts
//!   [`RECONNECT_GRACE`] is escalated to an error state вЂ” a planned daemon
//!   restart or a displaced session is back well inside the window and never
//!   reads as a failure. While disconnected, commands fail fast instead of
//!   timing out.
//!
//! # Deltas from the pipe transport
//!
//! This mirrors the `pipe` backend but sheds its two Windows-specific
//! contortions, because a unix socket is a simpler object:
//!
//! - **No peek-polling.** A unix socket is full-duplex: a blocking read on one
//!   `UnixStream::try_clone` half never blocks a write on another, so the reader
//!   just parks in `read` like an ordinary stream вЂ” no `PeekNamedPipe`
//!   availability probe, no idle poll tick. The pipe needs that dance only
//!   because Windows serializes I/O on a synchronous file object, where a parked
//!   `ReadFile` would deadlock every `WriteFile` on the same handle. To wake a
//!   reader still parked when the backend is torn down (a peer close wakes it on
//!   its own), we keep a spare clone of the socket and `shutdown(Shutdown::Both)`
//!   it, which makes the parked read return EOF.
//! - **No impersonation guard.** The pipe dials with `SECURITY_SQOS_PRESENT |
//!   SECURITY_IDENTIFICATION` to cap what a squatter on the pipe name could do
//!   with the client's token. A unix socket carries no such token and does not
//!   impersonate, so there is nothing to cap: the access control is the socket
//!   file's own permissions, which the daemon (the core) sets when it binds. The
//!   GUI only dials, and cannot be tricked into lending an identity it never had.

use std::io::{Read, Write};
use std::net::Shutdown;
use std::os::unix::net::UnixStream;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::sync::{Arc, Mutex};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use super::wire::{obj, read_loop, WireClient, WireSession};
use super::{ConnectionState, EventSink, State};

/// The well-known control socket, mirroring the path the macOS LaunchDaemon
/// binds on the Go side.
pub const SOCKET_PATH: &str = "/var/run/tenebra.sock";

/// Reconnect backoff: first retry comes quickly (the common loss is a daemon
/// restart or a displaced session, both back within a second), then doubles to
/// a ceiling so a stopped daemon is probed gently, not hammered.
const INITIAL_BACKOFF: Duration = Duration::from_millis(250);
const MAX_BACKOFF: Duration = Duration::from_secs(5);

/// How long a lost session may present itself as "reconnecting" before the loss
/// is reported as an error. The interruptions worth staying quiet for вЂ” the
/// daemon restarting under an update, a crash launchd restarts, a displaced
/// session redialing вЂ” are back within a couple of seconds. Eight seconds
/// comfortably outlasts all of those and spans the first five dial attempts
/// (backoff puts them ~0.25 s to ~7.75 s after the loss), while still reporting
/// a genuinely stopped daemon in single-digit seconds.
const RECONNECT_GRACE: Duration = Duration::from_secs(8);

/// The socket path the GUI should dial, or `None` to skip the unix-socket
/// transport entirely. Honors `TENEBRA_SOCKET`: unset or empty means the
/// well-known path, `off`/`0` disables it (handy in development, where a running
/// daemon would otherwise capture a `tauri dev` session meant for a freshly
/// built sidecar), anything else names an alternate socket path.
pub fn configured_path() -> Option<String> {
    let value = std::env::var("TENEBRA_SOCKET").ok();
    path_from(value.as_deref())
}

fn path_from(value: Option<&str>) -> Option<String> {
    match value {
        None | Some("") => Some(SOCKET_PATH.to_string()),
        Some("off") | Some("0") => None,
        Some(path) => Some(path.to_string()),
    }
}

/// One dialed connection, as the halves the wire client consumes plus the spare
/// handle used to wake the reader on teardown.
struct Conn {
    reader: Box<dyn Read + Send>,
    writer: Box<dyn Write + Send>,
    /// A third clone of the same socket, kept only so teardown can
    /// `shutdown(Shutdown::Both)` it and wake a reader parked in a blocking read
    /// (a peer-initiated close wakes it on its own). `None` for the in-memory
    /// test streams, which the shared stop flag inside the duplex already
    /// unsticks.
    wake: Option<UnixStream>,
}

/// How the supervisor re-establishes a connection. The real implementation
/// dials the unix socket; tests substitute scripted in-memory streams to drive
/// the reconnect logic without an OS socket.
trait Dial: Send + 'static {
    fn dial(&mut self) -> Result<Conn, String>;
}

/// Backend over the core's unix domain socket.
pub struct UnixBackend {
    shared: Arc<UnixShared>,
    /// Raised on drop; the supervisor's backoff wait checks it, so every parked
    /// wait unwinds promptly.
    stop: Arc<AtomicBool>,
    /// Dropped on drop, waking a supervisor parked in its backoff wait.
    stop_tx: Mutex<Option<Sender<()>>>,
    supervisor: Mutex<Option<JoinHandle<()>>>,
}

/// State shared with the supervisor thread.
struct UnixShared {
    /// The live session, absent while disconnected. Commands clone it out and
    /// fail fast when it is gone.
    session: Mutex<Option<Arc<WireClient>>>,
    /// A clone of the live socket kept solely to wake the reader: shutting it
    /// down makes the parked blocking read return EOF, ending the current
    /// session so the supervisor can exit on teardown. Absent while
    /// disconnected, and `None` for the in-memory test streams.
    wake: Mutex<Option<UnixStream>>,
}

impl UnixBackend {
    /// Dial `path` and start serving. The dial happens synchronously so the
    /// caller can fall back to another transport when no core is listening;
    /// after that the connection is supervised вЂ” lost sessions reconnect with
    /// backoff and re-sync вЂ” until the backend is dropped.
    pub fn connect(path: &str, sink: Arc<dyn EventSink>) -> Result<Self, String> {
        let stop = Arc::new(AtomicBool::new(false));
        let mut dialer = UnixDialer {
            path: path.to_string(),
        };
        let first = dialer.dial()?;
        Self::start(first, dialer, sink, stop, RECONNECT_GRACE)
    }

    /// Wire up the supervisor around an already-dialed first connection.
    /// `grace` is how long a lost session may stay "reconnecting" before it is
    /// reported as an error вЂ” [`RECONNECT_GRACE`] in production, shortened by
    /// the tests that exercise the expiry path.
    fn start(
        first: Conn,
        dialer: impl Dial,
        sink: Arc<dyn EventSink>,
        stop: Arc<AtomicBool>,
        grace: Duration,
    ) -> Result<Self, String> {
        let shared = Arc::new(UnixShared {
            session: Mutex::new(None),
            wake: Mutex::new(None),
        });
        let (stop_tx, stop_rx) = mpsc::channel();
        let sup_shared = Arc::clone(&shared);
        let sup_stop = Arc::clone(&stop);
        let supervisor = thread::Builder::new()
            .name("tenebra-unix-supervisor".into())
            .spawn(move || supervise(first, dialer, sup_shared, sink, sup_stop, stop_rx, grace))
            .map_err(|e| format!("failed to start the socket supervisor thread: {e}"))?;
        Ok(Self {
            shared,
            stop,
            stop_tx: Mutex::new(Some(stop_tx)),
            supervisor: Mutex::new(Some(supervisor)),
        })
    }
}

impl WireSession for UnixBackend {
    fn session(&self) -> Result<Arc<WireClient>, String> {
        self.shared
            .session
            .lock()
            .unwrap()
            .clone()
            .ok_or_else(|| "not connected to the Tenebra daemon; reconnecting".to_string())
    }
}

impl Drop for UnixBackend {
    fn drop(&mut self) {
        // Closing the GUI leaves the daemon вЂ” and a live tunnel вЂ” running by
        // design; only the connection is torn down. Raise stop for the backoff
        // wait, wake it by dropping its sender, shut the live socket down so the
        // reader unblocks (a live daemon won't hang up just because we are going
        // away), fail any in-flight request, and wait the supervisor out (all
        // its waits are ticked or shut down, so this is prompt).
        self.stop.store(true, Ordering::SeqCst);
        self.stop_tx.lock().unwrap().take();
        if let Some(stream) = self.shared.wake.lock().unwrap().as_ref() {
            let _ = stream.shutdown(Shutdown::Both);
        }
        if let Some(client) = self.shared.session.lock().unwrap().clone() {
            client.close();
        }
        if let Some(handle) = self.supervisor.lock().unwrap().take() {
            let _ = handle.join();
        }
    }
}

/// The synthetic state pushed the moment the control connection drops. The
/// tunnel may or may not still be up (a restarting daemon tears it down; a
/// displaced session leaves it), so neither `Connected` nor `Idle` would be
/// honest вЂ” and `Error` is premature while the redial usually lands within a
/// second or two. `Connecting` is the truthful in-between: nothing is claimed
/// about the tunnel, session-bound commands already fail fast with their own
/// "reconnecting" error, and the message says what is actually going on. The
/// re-sync after reconnecting replaces this with the real state; [`lost_state`]
/// replaces it if the grace window runs out first.
fn reconnecting_state() -> State {
    State {
        state: ConnectionState::Connecting,
        node: None,
        profile: None,
        routing: None,
        split: None,
        split_apps: None,
        kill_switch: None,
        tls_fragment: None,
        multihop: None,
        tun_stack: None,
        proxy_mode: None,
        proxy_port: None,
        autoconnect: None,
        auto_failover: None,
        ad_block: None,
        dns_remote: None,
        dns_direct: None,
        ipv4_only: None,
        rules_direct: None,
        rules_proxy: None,
        preset_ru_banking: None,
        preset_ru_gov: None,
        crash_reports: None,
        crash_reports_asked: false,
        error: Some("Reconnecting to the Tenebra daemonвЂ¦".to_string()),
    }
}

/// The synthetic state pushed when the daemon has not answered within the grace
/// window. By now the outage is not a restart blip, the tunnel state is unknown,
/// and `Error` is the honest choice of the protocol's four states вЂ” the UI must
/// not claim the tunnel is either up or cleanly down. The re-sync after an
/// eventual reconnect replaces this with the real state.
fn lost_state() -> State {
    State {
        state: ConnectionState::Error,
        node: None,
        profile: None,
        routing: None,
        split: None,
        split_apps: None,
        kill_switch: None,
        tls_fragment: None,
        multihop: None,
        tun_stack: None,
        proxy_mode: None,
        proxy_port: None,
        autoconnect: None,
        auto_failover: None,
        ad_block: None,
        dns_remote: None,
        dns_direct: None,
        ipv4_only: None,
        rules_direct: None,
        rules_proxy: None,
        preset_ru_banking: None,
        preset_ru_gov: None,
        crash_reports: None,
        crash_reports_asked: false,
        error: Some("Lost the connection to the Tenebra daemon; reconnecting.".to_string()),
    }
}

fn next_backoff(current: Duration) -> Duration {
    current.saturating_mul(2).min(MAX_BACKOFF)
}

/// Run sessions until the backend is dropped: serve the current connection to
/// its end, present the loss as a reconnect in progress, then redial with
/// backoff and serve again. Only when the daemon stays away past `grace` is the
/// loss escalated to an error state, once per outage. EOF from a displacement
/// (another client took the session вЂ” `ServeListener` is last-writer-wins) is
/// indistinguishable from a daemon restart on this side and is handled
/// identically вЂ” retry, never panic; the single-instance GUI makes a genuine
/// takeover war impossible in practice, and both causes normally redial well
/// inside the grace window.
fn supervise(
    first: Conn,
    mut dialer: impl Dial,
    shared: Arc<UnixShared>,
    sink: Arc<dyn EventSink>,
    stop: Arc<AtomicBool>,
    stop_rx: Receiver<()>,
    grace: Duration,
) {
    let mut conn = Some(first);
    while let Some(current) = conn.take() {
        serve_session(current, &shared, &sink);
        if stop.load(Ordering::SeqCst) {
            return;
        }

        // The session is gone and already cleared, so commands now fail fast;
        // tell the UI before spending time redialing вЂ” as a reconnect under
        // way, not yet a failure.
        sink.state(&reconnecting_state());
        sink.log(
            "warn",
            "lost the connection to the Tenebra daemon; reconnecting",
        );

        let deadline = Instant::now() + grace;
        let mut reported = false;
        let mut backoff = INITIAL_BACKOFF;
        loop {
            let dial_at = Instant::now() + backoff;
            // If the grace window closes before the next dial, wake for it:
            // the escalation should land at the deadline, not whenever the
            // (up to MAX_BACKOFF) wait happens to end.
            if !reported && deadline < dial_at {
                if !wait_until(&stop_rx, &stop, deadline) {
                    return;
                }
                sink.state(&lost_state());
                sink.log("warn", "the Tenebra daemon is still unreachable");
                reported = true;
            }
            if !wait_until(&stop_rx, &stop, dial_at) {
                return;
            }
            match dialer.dial() {
                Ok(next) => {
                    sink.log("info", "reconnected to the Tenebra daemon");
                    conn = Some(next);
                    break;
                }
                Err(_) => backoff = next_backoff(backoff),
            }
        }
    }
}

/// Park until `until`, waking early only for shutdown: `false` means the
/// backend is stopping (its drop raised the flag and hung up the channel),
/// `true` means the deadline passed.
fn wait_until(stop_rx: &Receiver<()>, stop: &AtomicBool, until: Instant) -> bool {
    loop {
        if stop.load(Ordering::SeqCst) {
            return false;
        }
        let remaining = until.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            return true;
        }
        match stop_rx.recv_timeout(remaining) {
            Err(RecvTimeoutError::Timeout) => {}
            Ok(()) | Err(RecvTimeoutError::Disconnected) => return false,
        }
    }
}

/// Serve one connection to its end: install the session (and its wake handle),
/// start the reader, re-sync, and wait the reader out. On return the session is
/// already cleared, so the supervisor's loss report never races a command onto a
/// dead client.
fn serve_session(conn: Conn, shared: &Arc<UnixShared>, sink: &Arc<dyn EventSink>) {
    let Conn {
        reader: reader_stream,
        writer,
        wake,
    } = conn;
    let client = WireClient::new(writer);
    *shared.session.lock().unwrap() = Some(Arc::clone(&client));
    *shared.wake.lock().unwrap() = wake;

    let reader_client = Arc::clone(&client);
    let reader_sink = Arc::clone(sink);
    let reader = match thread::Builder::new()
        .name("tenebra-unix-reader".into())
        .spawn(move || read_loop(reader_stream, reader_client, reader_sink))
    {
        Ok(handle) => handle,
        Err(e) => {
            *shared.session.lock().unwrap() = None;
            *shared.wake.lock().unwrap() = None;
            client.close();
            sink.log("error", &format!("could not start the socket reader: {e}"));
            return;
        }
    };

    // The socket delivers no backlog of events on attach, so the state on
    // connect is whatever it already was: ask, and push the answer at the UI. A
    // failure here just means the session died at birth; the reader is about to
    // end and the supervisor will handle it.
    match client.request_into::<State>("status", obj([])) {
        Ok(state) => sink.state(&state),
        Err(e) => sink.log("warn", &format!("status re-sync failed: {e}")),
    }

    let _ = reader.join();
    *shared.session.lock().unwrap() = None;
    *shared.wake.lock().unwrap() = None;
}

/// Dials the OS socket. Each dial produces a blocking reader and an independent
/// writer over one connection, plus a spare clone kept for `shutdown`.
struct UnixDialer {
    path: String,
}

impl Dial for UnixDialer {
    fn dial(&mut self) -> Result<Conn, String> {
        // `connect` either succeeds or fails at once вЂ” a missing socket file is
        // `ENOENT`, a bound-but-unserved one `ECONNREFUSED` вЂ” so unlike the
        // named pipe there is no "server exists but has no free instance"
        // transient to wait through. A failure here is the caller's to judge: at
        // startup it selects the sidecar fallback, mid-run it feeds the
        // reconnect backoff.
        let stream =
            UnixStream::connect(&self.path).map_err(|e| format!("connect {}: {e}", self.path))?;
        // Both halves and the wake handle are clones of one socket: the kernel
        // reference-counts them, and a blocking read on one never blocks a write
        // on another (full-duplex), so no polling is needed to keep writes live.
        let reader = stream
            .try_clone()
            .map_err(|e| format!("clone the socket handle: {e}"))?;
        let wake = stream
            .try_clone()
            .map_err(|e| format!("clone the socket handle: {e}"))?;
        Ok(Conn {
            reader: Box::new(reader),
            writer: Box::new(stream),
            wake: Some(wake),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::super::testutil::{duplex, ChanEnd, Rec};
    use super::super::Backend;
    use super::*;
    use serde_json::{json, Value};
    use std::collections::VecDeque;
    use std::io::{BufRead, BufReader};
    use std::time::Duration;

    /// A generous ceiling for the asynchronous assertions: reconnects take a
    /// few backoff steps (sub-second), so this only bounds a genuine hang.
    const WAIT: Duration = Duration::from_secs(10);

    #[test]
    fn path_from_maps_the_env_convention() {
        assert_eq!(path_from(None).as_deref(), Some(SOCKET_PATH));
        assert_eq!(path_from(Some("")).as_deref(), Some(SOCKET_PATH));
        assert_eq!(path_from(Some("off")), None);
        assert_eq!(path_from(Some("0")), None);
        assert_eq!(
            path_from(Some("/tmp/tenebra-test.sock")).as_deref(),
            Some("/tmp/tenebra-test.sock")
        );
    }

    #[test]
    fn backoff_doubles_and_caps() {
        let mut delay = INITIAL_BACKOFF;
        let mut seen = Vec::new();
        for _ in 0..8 {
            seen.push(delay);
            delay = next_backoff(delay);
        }
        assert_eq!(seen[0], Duration::from_millis(250));
        assert_eq!(seen[1], Duration::from_millis(500));
        assert_eq!(seen[2], Duration::from_millis(1000));
        assert!(seen.iter().all(|d| *d <= MAX_BACKOFF));
        assert_eq!(next_backoff(MAX_BACKOFF), MAX_BACKOFF);
    }

    // --- supervisor behaviour over in-memory streams ------------------------

    /// Hands out pre-built connections (or failures) in order; exhausted means
    /// the daemon stays unreachable.
    struct ScriptDialer {
        script: VecDeque<Result<Conn, String>>,
    }

    impl Dial for ScriptDialer {
        fn dial(&mut self) -> Result<Conn, String> {
            self.script
                .pop_front()
                .unwrap_or_else(|| Err("script exhausted".into()))
        }
    }

    /// Wrap an in-memory duplex end as a `Conn`. Its reader is unblocked by the
    /// duplex's shared stop flag rather than a socket `shutdown`, so there is no
    /// wake handle.
    fn conn_from(end: ChanEnd) -> Conn {
        Conn {
            reader: Box::new(end.reader),
            writer: Box::new(end.writer),
            wake: None,
        }
    }

    /// A stub core on the far end of a duplex: answers every request `ok:true`
    /// with `data` from `respond`, records what it saw, and hangs up after
    /// `response_limit` responses (`None` = serve until the client goes away).
    fn spawn_stub_core(
        end: ChanEnd,
        requests: Arc<Mutex<Vec<Value>>>,
        respond: fn(&str) -> Value,
        response_limit: Option<usize>,
    ) -> thread::JoinHandle<()> {
        thread::spawn(move || {
            let mut writer = end.writer;
            let mut served = 0usize;
            for line in BufReader::new(end.reader).lines() {
                let Ok(line) = line else { break };
                if line.trim().is_empty() {
                    continue;
                }
                let req: Value = serde_json::from_str(&line).expect("request is JSON");
                let id = req["id"].as_u64().expect("request carries an id");
                let cmd = req["cmd"]
                    .as_str()
                    .expect("request carries a cmd")
                    .to_string();
                requests.lock().unwrap().push(req);
                let reply = json!({ "id": id, "ok": true, "data": respond(&cmd) });
                if writeln!(writer, "{reply}").is_err() {
                    break;
                }
                served += 1;
                if response_limit.is_some_and(|limit| served >= limit) {
                    break; // dropping both halves hangs up on the client
                }
            }
        })
    }

    fn connected_state(cmd: &str) -> Value {
        match cmd {
            "disconnect" => json!({ "state": "idle" }),
            _ => json!({ "state": "connected", "node": "n1" }),
        }
    }

    #[test]
    fn a_new_session_resyncs_with_status_and_serves_commands() {
        let stop = Arc::new(AtomicBool::new(false));
        let (ours, theirs) = duplex(&stop);
        let requests = Arc::new(Mutex::new(Vec::new()));
        let stub = spawn_stub_core(theirs, Arc::clone(&requests), connected_state, None);

        let sink = Arc::new(Rec::default());
        let dialer = ScriptDialer {
            script: VecDeque::new(),
        };
        let backend = UnixBackend::start(
            conn_from(ours),
            dialer,
            Arc::clone(&sink) as Arc<dyn EventSink>,
            Arc::clone(&stop),
            RECONNECT_GRACE,
        )
        .expect("start unix backend");

        // The re-sync pushes the daemon's current state without the UI asking.
        let states = sink.wait_for_states(1, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);
        assert_eq!(states[0].node.as_deref(), Some("n1"));

        // Commands flow through the same session вЂ” driven via the blanket
        // `Backend` impl over `WireSession`, exactly as the command layer calls
        // them.
        let state = backend.disconnect().expect("disconnect over the socket");
        assert_eq!(state.state, ConnectionState::Idle);

        let cmds: Vec<String> = requests
            .lock()
            .unwrap()
            .iter()
            .map(|r| r["cmd"].as_str().unwrap_or_default().to_string())
            .collect();
        assert_eq!(
            cmds,
            vec!["status".to_string(), "disconnect".to_string()],
            "the first request on a new session must be the status re-sync"
        );

        drop(backend);
        stub.join().expect("stub core thread");
    }

    #[test]
    fn a_lost_session_reconnects_within_grace_without_an_error() {
        let stop = Arc::new(AtomicBool::new(false));
        let (ours1, theirs1) = duplex(&stop);
        let (ours2, theirs2) = duplex(&stop);
        let requests1 = Arc::new(Mutex::new(Vec::new()));
        let requests2 = Arc::new(Mutex::new(Vec::new()));
        // The first daemon instance answers exactly one request (the re-sync)
        // and hangs up вЂ” a restart. The second serves normally.
        let stub1 = spawn_stub_core(theirs1, Arc::clone(&requests1), connected_state, Some(1));
        let stub2 = spawn_stub_core(theirs2, Arc::clone(&requests2), connected_state, None);

        let sink = Arc::new(Rec::default());
        // First redial fails (the daemon is still coming back), the next one
        // lands on the new instance вЂ” well inside the production grace window.
        let dialer = ScriptDialer {
            script: VecDeque::from([
                Err("the daemon is still down".to_string()),
                Ok(conn_from(ours2)),
            ]),
        };
        let backend = UnixBackend::start(
            conn_from(ours1),
            dialer,
            Arc::clone(&sink) as Arc<dyn EventSink>,
            Arc::clone(&stop),
            RECONNECT_GRACE,
        )
        .expect("start unix backend");

        // connected (re-sync 1) в†’ connecting (loss) в†’ connected (re-sync 2).
        let states = sink.wait_for_states(3, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);
        assert_eq!(states[1].state, ConnectionState::Connecting);
        assert!(
            states[1]
                .error
                .as_deref()
                .is_some_and(|e| e.contains("Reconnecting")),
            "the loss state should say it is reconnecting: {:?}",
            states[1]
        );
        assert_eq!(states[2].state, ConnectionState::Connected);
        // The redial landed inside the grace window, so the loss must never
        // have been escalated to an error.
        assert!(
            states.iter().all(|s| s.state != ConnectionState::Error),
            "a redial inside the grace window must not report an error: {states:?}"
        );

        // The new session re-synced with its own status request.
        let cmds2: Vec<String> = requests2
            .lock()
            .unwrap()
            .iter()
            .map(|r| r["cmd"].as_str().unwrap_or_default().to_string())
            .collect();
        assert_eq!(cmds2.first().map(String::as_str), Some("status"));

        let logs = sink.logs.lock().unwrap().clone();
        assert!(
            logs.iter().any(|(_, msg)| msg.contains("reconnected")),
            "expected a reconnect log, got {logs:?}"
        );

        drop(backend);
        stub1.join().expect("stub core 1");
        stub2.join().expect("stub core 2");
    }

    #[test]
    fn a_loss_outlasting_the_grace_reports_an_error_once() {
        let stop = Arc::new(AtomicBool::new(false));
        let (ours1, theirs1) = duplex(&stop);
        let (ours2, theirs2) = duplex(&stop);
        let requests1 = Arc::new(Mutex::new(Vec::new()));
        let requests2 = Arc::new(Mutex::new(Vec::new()));
        let stub1 = spawn_stub_core(theirs1, Arc::clone(&requests1), connected_state, Some(1));
        let stub2 = spawn_stub_core(theirs2, Arc::clone(&requests2), connected_state, None);

        let sink = Arc::new(Rec::default());
        // Two failed dials (~0.25 s and ~0.75 s in), success on the third
        // (~1.75 s in). A 600 ms grace expires between the first and second
        // dial вЂ” in the middle of a backoff wait, which must wake for it.
        let grace = Duration::from_millis(600);
        let dialer = ScriptDialer {
            script: VecDeque::from([
                Err("still down".to_string()),
                Err("still down".to_string()),
                Ok(conn_from(ours2)),
            ]),
        };
        let backend = UnixBackend::start(
            conn_from(ours1),
            dialer,
            Arc::clone(&sink) as Arc<dyn EventSink>,
            Arc::clone(&stop),
            grace,
        )
        .expect("start unix backend");

        // connected (re-sync 1) в†’ connecting (loss) в†’ error (grace expired) в†’
        // connected (re-sync 2 replaces the error).
        let states = sink.wait_for_states(4, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);
        assert_eq!(states[1].state, ConnectionState::Connecting);
        assert_eq!(states[2].state, ConnectionState::Error);
        assert!(
            states[2]
                .error
                .as_deref()
                .is_some_and(|e| e.contains("Lost")),
            "the escalated state should name the loss: {:?}",
            states[2]
        );
        assert_eq!(states[3].state, ConnectionState::Connected);
        // Later failed dials in the same outage must not repeat the report.
        assert_eq!(
            states
                .iter()
                .filter(|s| s.state == ConnectionState::Error)
                .count(),
            1,
            "the loss is escalated once per outage: {states:?}"
        );

        drop(backend);
        stub1.join().expect("stub core 1");
        stub2.join().expect("stub core 2");
    }

    #[test]
    fn commands_fail_fast_while_disconnected() {
        let stop = Arc::new(AtomicBool::new(false));
        let (ours, theirs) = duplex(&stop);
        // The only session answers one request and hangs up; every redial
        // fails, so the backend stays disconnected.
        let requests = Arc::new(Mutex::new(Vec::new()));
        let stub = spawn_stub_core(theirs, Arc::clone(&requests), connected_state, Some(1));

        let sink = Arc::new(Rec::default());
        let dialer = ScriptDialer {
            script: VecDeque::new(),
        };
        let backend = UnixBackend::start(
            conn_from(ours),
            dialer,
            Arc::clone(&sink) as Arc<dyn EventSink>,
            Arc::clone(&stop),
            RECONNECT_GRACE,
        )
        .expect("start unix backend");

        // Once the reconnecting state is out, the session is guaranteed cleared
        // (the supervisor clears it before reporting), so a command must fail
        // fast with the reconnecting error rather than riding out a timeout вЂ”
        // the grace window softens the presentation, never the semantics.
        let states = sink.wait_for_states(2, WAIT);
        assert_eq!(states[1].state, ConnectionState::Connecting);
        let err = backend.status().expect_err("no session to serve this");
        assert!(
            err.contains("reconnecting"),
            "expected the fail-fast reconnect error, got: {err}"
        );

        drop(backend);
        stub.join().expect("stub core thread");
    }

    // --- the real OS socket --------------------------------------------------
    //
    // An in-process `UnixListener` in the temp dir proves the client against
    // real socket semantics: that a blocking reader and concurrent writes
    // coexist on cloned halves of one stream, that a server hangup surfaces as
    // the loss/reconnect path, and that a dial with nobody listening fails
    // cleanly. The cross-implementation round-trip against the real Go core over
    // `--socket` belongs with `tests/sidecar_e2e.rs` once the daemon side lands.

    use std::os::unix::net::UnixListener;
    use std::sync::atomic::AtomicUsize;

    /// A socket path unique to this process and case, kept under the temp dir
    /// and short so it stays inside the platform's `sun_path` limit, and so
    /// parallel test runs never collide.
    fn unique_socket_path(tag: &str) -> String {
        static COUNTER: AtomicUsize = AtomicUsize::new(0);
        std::env::temp_dir()
            .join(format!(
                "tnb-{}-{}-{}.sock",
                std::process::id(),
                tag,
                COUNTER.fetch_add(1, Ordering::SeqCst)
            ))
            .to_string_lossy()
            .into_owned()
    }

    /// Removes the socket node on drop: a bound `UnixListener` leaves its file
    /// on the filesystem, and a stale node would fail the next bind. Held by the
    /// test so cleanup runs even if the server thread panics.
    struct RemoveOnDrop(String);

    impl Drop for RemoveOnDrop {
        fn drop(&mut self) {
            let _ = std::fs::remove_file(&self.0);
        }
    }

    /// Bind one listener on `path`, clearing any stale node first.
    fn bind_listener(path: &str) -> UnixListener {
        let _ = std::fs::remove_file(path);
        UnixListener::bind(path).expect("bind the test socket")
    }

    /// Serve requests on one accepted connection until EOF or `response_limit`,
    /// pushing `extra_event` (if any) right after the first response. Reads and
    /// writes go through two shared borrows of the same stream, mirroring how a
    /// real client's cloned halves share one socket.
    fn serve_requests(
        stream: &UnixStream,
        requests: &Arc<Mutex<Vec<Value>>>,
        response_limit: Option<usize>,
        extra_event: Option<&str>,
    ) {
        let mut served = 0usize;
        let mut lines = BufReader::new(stream).lines();
        while let Some(Ok(line)) = lines.next() {
            if line.trim().is_empty() {
                continue;
            }
            let req: Value = serde_json::from_str(&line).expect("request is JSON");
            let id = req["id"].as_u64().expect("request carries an id");
            let cmd = req["cmd"].as_str().unwrap_or_default().to_string();
            requests.lock().unwrap().push(req);
            let reply = json!({ "id": id, "ok": true, "data": connected_state(&cmd) });
            let mut writer = stream;
            if writeln!(writer, "{reply}").is_err() {
                break;
            }
            if served == 0 {
                if let Some(event) = extra_event {
                    if writeln!(writer, "{event}").is_err() {
                        break;
                    }
                }
            }
            served += 1;
            if response_limit.is_some_and(|limit| served >= limit) {
                break;
            }
        }
    }

    /// Dial until the server thread's listener is up вЂ” the tests bind it on
    /// another thread, so the very first dial can lose the race.
    fn retry_connect(path: &str, sink: Arc<Rec>) -> UnixBackend {
        let deadline = Instant::now() + WAIT;
        loop {
            match UnixBackend::connect(path, Arc::clone(&sink) as Arc<dyn EventSink>) {
                Ok(backend) => return backend,
                Err(e) => {
                    assert!(
                        Instant::now() < deadline,
                        "could not connect to {path} in time: {e}"
                    );
                    thread::sleep(Duration::from_millis(20));
                }
            }
        }
    }

    fn wait_until(timeout: Duration, check: impl Fn() -> bool) {
        let deadline = Instant::now() + timeout;
        while !check() {
            assert!(Instant::now() < deadline, "condition not met in time");
            thread::sleep(Duration::from_millis(10));
        }
    }

    #[test]
    fn real_socket_serves_commands_and_events() {
        let path = unique_socket_path("serve");
        let _cleanup = RemoveOnDrop(path.clone());
        let requests = Arc::new(Mutex::new(Vec::new()));
        let server_requests = Arc::clone(&requests);
        let server_path = path.clone();
        let server = thread::spawn(move || {
            let listener = bind_listener(&server_path);
            let (stream, _addr) = listener.accept().expect("accept a client");
            serve_requests(
                &stream,
                &server_requests,
                None,
                Some(r#"{"event":"log","level":"info","msg":"hello from the daemon"}"#),
            );
        });

        let sink = Arc::new(Rec::default());
        let backend = retry_connect(&path, Arc::clone(&sink));

        // Re-sync state arrived without any command from us.
        let states = sink.wait_for_states(1, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);

        // An unsolicited event crossed the same stream.
        wait_until(WAIT, || {
            sink.logs
                .lock()
                .unwrap()
                .iter()
                .any(|(_, msg)| msg.contains("hello from the daemon"))
        });

        // A command round-trips through the blanket `Backend` impl while the
        // blocking reader idles on a clone of the same socket вЂ” the write would
        // deadlock only if reads and writes contended on one handle, which unix
        // sockets do not (module docs).
        let state = backend.status().expect("status over the real socket");
        assert_eq!(state.state, ConnectionState::Connected);

        drop(backend); // shuts the socket down; the server sees EOF
        server.join().expect("socket server thread");
    }

    #[test]
    fn real_socket_reconnects_after_a_server_restart() {
        let path = unique_socket_path("restart");
        let _cleanup = RemoveOnDrop(path.clone());
        let requests = Arc::new(Mutex::new(Vec::new()));
        let server_requests = Arc::clone(&requests);
        let server_path = path.clone();
        let server = thread::spawn(move || {
            let listener = bind_listener(&server_path);
            // First session: answer the re-sync, then hang up (a restart, from
            // the client's point of view вЂ” the listener stays up so the redial
            // lands at once, like a displaced session).
            let (stream, _addr) = listener.accept().expect("accept first client");
            serve_requests(&stream, &server_requests, Some(1), None);
            drop(stream);
            // Second session: a fresh connection on the same listener.
            let (stream, _addr) = listener.accept().expect("accept second client");
            serve_requests(&stream, &server_requests, None, None);
        });

        let sink = Arc::new(Rec::default());
        let backend = retry_connect(&path, Arc::clone(&sink));

        // connected в†’ connecting (hangup, the redial lands inside the grace
        // window) в†’ connected (redial + re-sync).
        let states = sink.wait_for_states(3, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);
        assert_eq!(states[1].state, ConnectionState::Connecting);
        assert_eq!(states[2].state, ConnectionState::Connected);
        assert!(
            states.iter().all(|s| s.state != ConnectionState::Error),
            "a redial inside the grace window must not report an error: {states:?}"
        );

        // Both sessions opened with the status re-sync.
        let cmds: Vec<String> = requests
            .lock()
            .unwrap()
            .iter()
            .map(|r| r["cmd"].as_str().unwrap_or_default().to_string())
            .collect();
        assert!(
            cmds.len() >= 2 && cmds[0] == "status" && cmds[1] == "status",
            "both sessions must re-sync first, got {cmds:?}"
        );

        drop(backend);
        server.join().expect("socket server thread");
    }

    #[test]
    fn connect_fails_cleanly_with_nobody_listening() {
        // A path nothing ever binds: the dial must fail (not hang), and name the
        // socket so the fallback log is actionable.
        let path = unique_socket_path("absent");
        let sink: Arc<dyn EventSink> = Arc::new(Rec::default());
        let err = match UnixBackend::connect(&path, sink) {
            Ok(_) => panic!("dialing a nonexistent socket must fail"),
            Err(e) => e,
        };
        assert!(
            err.contains(&path),
            "the error should name the socket: {err}"
        );
    }
}
