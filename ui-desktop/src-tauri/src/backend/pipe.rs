//! The named-pipe transport: a client of the core running detached from the
//! GUI — as the Windows service, or via `tenebra-core --pipe`.
//!
//! The core listens on `\\.\pipe\tenebra` (see `core/control/pipe_windows.go`
//! and the Transports section of `docs/control-protocol.md`); this type dials
//! it and runs the same [`wire`](super::wire) client the sidecar uses. What is
//! different from the sidecar is the lifecycle: the service outlives any one
//! GUI process, so the connection — not the process — is the thing to manage.
//!
//! - **Patience on the first dial.** The service is often seconds behind the
//!   GUI (an installer that just ran `sc start`, or an autostart login racing
//!   the SCM), and the transport is chosen once per run, so the opening dial
//!   waits a bounded [`DIAL_ABSENT_WAIT`] for a listener to appear rather than
//!   reading its absence as "no service on this machine".
//! - **Re-sync on connect.** The pipe serves exactly one session at a time and
//!   buffers no events between sessions, so the state on connect is whatever it
//!   already is. Every new session therefore opens with a `status` request and
//!   pushes the answer at the UI.
//! - **Reconnect on loss.** The session ending (service restarted or died, or
//!   another client displaced us — the pipe is last-writer-wins) is not fatal:
//!   a supervisor thread pushes a synthetic "reconnecting" state at the UI and
//!   redials with capped exponential backoff until the pipe answers again,
//!   then re-syncs. Only a loss that outlasts [`RECONNECT_GRACE`] is escalated
//!   to an error state — a planned service restart or a displaced session is
//!   back well inside the window and never reads as a failure. While
//!   disconnected, commands fail fast instead of timing out.
//!
//! # Why the reader polls
//!
//! The pipe handle is opened synchronously (no `FILE_FLAG_OVERLAPPED`), and
//! Windows serializes I/O on a synchronous file object: a `ReadFile` parked
//! waiting for data holds the file-object lock and blocks any `WriteFile` on
//! the same object — including one through a duplicated handle, which shares
//! it. A thread camping in a blocking read would deadlock every request. So
//! the reader never blocks in `read`: it asks `PeekNamedPipe` how many bytes
//! are ready and only reads that fast path, sleeping a short tick otherwise.
//! Reads then always complete immediately, writes only ever wait out a quick
//! read, and the tick doubles as a prompt shutdown check. (The overlapped
//! alternative is a pile of unsafe I/O plumbing for the same result; the Go
//! side needs go-winio for exactly this reason.)

use std::fs::{File, OpenOptions};
use std::io::{self, Read, Write};
use std::os::windows::fs::OpenOptionsExt;
use std::os::windows::io::AsRawHandle;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::sync::{Arc, Mutex};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use windows_sys::Win32::Foundation::{
    ERROR_BROKEN_PIPE, ERROR_FILE_NOT_FOUND, ERROR_NO_DATA, ERROR_PIPE_BUSY,
    ERROR_PIPE_NOT_CONNECTED,
};
use windows_sys::Win32::Storage::FileSystem::{SECURITY_IDENTIFICATION, SECURITY_SQOS_PRESENT};
use windows_sys::Win32::System::Pipes::{PeekNamedPipe, WaitNamedPipeW, NMPWAIT_NOWAIT};

use super::wire::{obj, read_loop, WireClient, WireSession};
use super::{ConnectionState, EventSink, State};

/// The well-known control pipe, mirroring `control.PipeName` on the Go side.
pub const PIPE_NAME: &str = r"\\.\pipe\tenebra";

/// How often the reader re-peeks an idle pipe (and rechecks shutdown). Events
/// and responses arrive at most this much late — imperceptible next to the
/// commands' own latency — and an idle GUI costs one no-op syscall per tick.
const POLL_INTERVAL: Duration = Duration::from_millis(20);

/// Reconnect backoff: first retry comes quickly (the common loss is a service
/// restart or a displaced session, both back within a second), then doubles to
/// a ceiling so a stopped service is probed gently, not hammered.
const INITIAL_BACKOFF: Duration = Duration::from_millis(250);
const MAX_BACKOFF: Duration = Duration::from_secs(5);

/// How long a lost session may present itself as "reconnecting" before the
/// loss is reported as an error. The interruptions worth staying quiet for —
/// the service restarting under an update, a crash the service manager
/// restarts, a displaced session redialing — are back within a couple of
/// seconds. Eight seconds comfortably outlasts all of those and spans the
/// first five dial attempts (backoff puts them ~0.25 s to ~7.75 s after the
/// loss), while still reporting a genuinely stopped service in single-digit
/// seconds.
const RECONNECT_GRACE: Duration = Duration::from_secs(8);

/// How long a dial tolerates `ERROR_PIPE_BUSY` before giving up. Busy means the
/// server exists but has no free instance this instant (it is between accepting
/// a client and creating the next instance), so a short retry is enough.
const DIAL_BUSY_WAIT: Duration = Duration::from_secs(2);

/// How long the *first* dial tolerates `ERROR_FILE_NOT_FOUND` — the name is not
/// bound at all, which is "no service on this machine" and "the service has not
/// got to `ListenPipe` yet" wearing the same error code. One attempt cannot tell
/// them apart, and the cost of guessing wrong is not a dropped beat: the GUI
/// picks its transport exactly once (`make_backend` in `lib.rs`), so a lost race
/// here strands the whole run on an unprivileged sidecar with its own profile
/// store — the user's profiles simply are not there, and Connect does nothing.
///
/// Both ways into that race are ordinary. The installer runs `sc start tenebra`
/// and launches the app in the next breath; an autostart login starts the
/// service and the GUI concurrently. In each the core still has to clamp its
/// data directory's DACL and load the store before it binds the name. Five
/// seconds covers that with room to spare while staying under the eight-second
/// [`RECONNECT_GRACE`] the mid-run path already treats as "not a failure yet",
/// and the dial re-attempts every [`DIAL_RETRY_TICK`], so a listener that
/// arrives early is picked up within tens of milliseconds. Only a machine with
/// genuinely no core pays the whole window, once, before falling back.
const DIAL_ABSENT_WAIT: Duration = Duration::from_secs(5);

/// How often a dial re-attempts while it is waiting out one of the retryable
/// failures above.
const DIAL_RETRY_TICK: Duration = Duration::from_millis(50);

/// The pipe name the GUI should use, or `None` to skip the pipe transport
/// entirely. Honors `TENEBRA_PIPE`: unset or empty means the well-known name,
/// `off`/`0` disables it (handy in development, where a running service would
/// otherwise capture a `tauri dev` session meant for a freshly built sidecar),
/// anything else names an alternate pipe.
pub fn configured_name() -> Option<String> {
    let value = std::env::var("TENEBRA_PIPE").ok();
    name_from(value.as_deref())
}

fn name_from(value: Option<&str>) -> Option<String> {
    match value {
        None | Some("") => Some(PIPE_NAME.to_string()),
        Some("off") | Some("0") => None,
        Some(name) => Some(name.to_string()),
    }
}

/// Whether a core is listening on `name` right now, asked without connecting.
///
/// `WaitNamedPipeW` with `NMPWAIT_NOWAIT` answers exactly the availability
/// question and does nothing else — in particular it never opens a session,
/// which is the whole point: pipe sessions are last-writer-wins (see the
/// Named-pipe sessions section of `docs/control-protocol.md`), so a dial used as
/// a probe would displace whichever client currently holds the service.
///
/// The answer is a snapshot, and it errs towards "no": a server whose only free
/// instance is momentarily taken — it is between accepting a client and creating
/// the next instance — reads as absent. Callers should treat `false` as "not
/// this instant" and look again, never as "there is no service on this machine".
pub fn is_listening(name: &str) -> bool {
    let wide: Vec<u16> = name.encode_utf16().chain(std::iter::once(0)).collect();
    // SAFETY: `wide` is a valid NUL-terminated wide string that outlives the
    // call, and NMPWAIT_NOWAIT keeps it from blocking on a bound-but-busy name.
    unsafe { WaitNamedPipeW(wide.as_ptr(), NMPWAIT_NOWAIT) != 0 }
}

/// One dialed connection, as the halves the wire client consumes.
struct Conn {
    reader: Box<dyn Read + Send>,
    writer: Box<dyn Write + Send>,
}

/// How the supervisor re-establishes a connection. The real implementation
/// opens the named pipe; tests substitute scripted in-memory streams to drive
/// the reconnect logic without an OS pipe.
trait Dial: Send + 'static {
    fn dial(&mut self) -> Result<Conn, String>;
}

/// Backend over the core's named pipe.
pub struct PipeBackend {
    shared: Arc<PipeShared>,
    /// Raised on drop; the reader's poll tick and the dialer's busy loop check
    /// it, so every blocking path unwinds promptly.
    stop: Arc<AtomicBool>,
    /// Dropped on drop, waking a supervisor parked in its backoff wait.
    stop_tx: Mutex<Option<Sender<()>>>,
    supervisor: Mutex<Option<JoinHandle<()>>>,
}

/// State shared with the supervisor thread.
struct PipeShared {
    /// The live session, absent while disconnected. Commands clone it out and
    /// fail fast when it is gone.
    session: Mutex<Option<Arc<WireClient>>>,
}

impl PipeBackend {
    /// Dial `name` and start serving. The dial happens synchronously so the
    /// caller can fall back to another transport when no core is listening;
    /// after that the connection is supervised — lost sessions reconnect with
    /// backoff and re-sync — until the backend is dropped.
    ///
    /// It is deliberately patient: a service that is merely still starting
    /// answers within [`DIAL_ABSENT_WAIT`], and only a name nobody binds in that
    /// window is reported as "no core here" for the caller to fall back on.
    pub fn connect(name: &str, sink: Arc<dyn EventSink>) -> Result<Self, String> {
        Self::connect_within(name, sink, DIAL_ABSENT_WAIT)
    }

    /// [`connect`](Self::connect) with an explicit budget for a pipe that is not
    /// bound yet — [`DIAL_ABSENT_WAIT`] in production, zero in the tests that
    /// assert the fallback path stays prompt when nothing is listening.
    fn connect_within(
        name: &str,
        sink: Arc<dyn EventSink>,
        absent_wait: Duration,
    ) -> Result<Self, String> {
        let stop = Arc::new(AtomicBool::new(false));
        let mut dialer = PipeDialer {
            name: name.to_string(),
            stop: Arc::clone(&stop),
            absent_wait,
        };
        let first = dialer.dial()?;
        Self::start(first, dialer, sink, stop, RECONNECT_GRACE)
    }

    /// Wire up the supervisor around an already-dialed first connection.
    /// `grace` is how long a lost session may stay "reconnecting" before it is
    /// reported as an error — [`RECONNECT_GRACE`] in production, shortened by
    /// the tests that exercise the expiry path.
    fn start(
        first: Conn,
        dialer: impl Dial,
        sink: Arc<dyn EventSink>,
        stop: Arc<AtomicBool>,
        grace: Duration,
    ) -> Result<Self, String> {
        let shared = Arc::new(PipeShared {
            session: Mutex::new(None),
        });
        let (stop_tx, stop_rx) = mpsc::channel();
        let sup_shared = Arc::clone(&shared);
        let sup_stop = Arc::clone(&stop);
        let supervisor = thread::Builder::new()
            .name("tenebra-pipe-supervisor".into())
            .spawn(move || supervise(first, dialer, sup_shared, sink, sup_stop, stop_rx, grace))
            .map_err(|e| format!("failed to start the pipe supervisor thread: {e}"))?;
        Ok(Self {
            shared,
            stop,
            stop_tx: Mutex::new(Some(stop_tx)),
            supervisor: Mutex::new(Some(supervisor)),
        })
    }
}

impl WireSession for PipeBackend {
    fn session(&self) -> Result<Arc<WireClient>, String> {
        self.shared
            .session
            .lock()
            .unwrap()
            .clone()
            .ok_or_else(|| "not connected to the Tenebra service; reconnecting".to_string())
    }
}

impl Drop for PipeBackend {
    fn drop(&mut self) {
        // Closing the GUI leaves the service — and a live tunnel — running by
        // design; only the connection is torn down. Raise stop for the poll
        // loops, wake a backoff wait by dropping its sender, fail any in-flight
        // request, and wait the supervisor out (all its waits are ticked, so
        // this is prompt).
        self.stop.store(true, Ordering::SeqCst);
        self.stop_tx.lock().unwrap().take();
        if let Some(client) = self.shared.session.lock().unwrap().clone() {
            client.close();
        }
        if let Some(handle) = self.supervisor.lock().unwrap().take() {
            let _ = handle.join();
        }
    }
}

/// The synthetic state pushed the moment the control connection drops. The
/// tunnel may or may not still be up (a restarting service tears it down; a
/// displaced session leaves it), so neither `Connected` nor `Idle` would be
/// honest — and `Error` is premature while the redial usually lands within a
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
        // Unknown while the service is away; the UI's staleness detection only
        // trusts idle/connected snapshots, so this None cannot read as "old".
        daemon_version: None,
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
        error: Some("Reconnecting to the Tenebra serviceвЂ¦".to_string()),
    }
}

/// The synthetic state pushed when the service has not answered within the
/// grace window. By now the outage is not a restart blip, the tunnel state is
/// unknown, and `Error` is the honest choice of the protocol's four states —
/// the UI must not claim the tunnel is either up or cleanly down. The re-sync
/// after an eventual reconnect replaces this with the real state.
fn lost_state() -> State {
    State {
        state: ConnectionState::Error,
        node: None,
        profile: None,
        routing: None,
        // Unknown while the service is away (see reconnecting_state).
        daemon_version: None,
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
        error: Some("Lost the connection to the Tenebra service; reconnecting.".to_string()),
    }
}

fn next_backoff(current: Duration) -> Duration {
    current.saturating_mul(2).min(MAX_BACKOFF)
}

/// Run sessions until the backend is dropped: serve the current connection to
/// its end, present the loss as a reconnect in progress, then redial with
/// backoff and serve again. Only when the service stays away past `grace` is
/// the loss escalated to an error state, once per outage. EOF from a
/// displacement (another client took the pipe) is indistinguishable from a
/// service restart on this side and is handled identically — retry, never
/// panic; the single-instance GUI makes a genuine takeover war impossible in
/// practice, and both causes normally redial well inside the grace window.
fn supervise(
    first: Conn,
    mut dialer: impl Dial,
    shared: Arc<PipeShared>,
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
        // tell the UI before spending time redialing — as a reconnect under
        // way, not yet a failure.
        sink.state(&reconnecting_state());
        sink.log(
            "warn",
            "lost the connection to the Tenebra service; reconnecting",
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
                sink.log("warn", "the Tenebra service is still unreachable");
                reported = true;
            }
            if !wait_until(&stop_rx, &stop, dial_at) {
                return;
            }
            match dialer.dial() {
                Ok(next) => {
                    sink.log("info", "reconnected to the Tenebra service");
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

/// Serve one connection to its end: install the session, start the reader,
/// re-sync, and wait the reader out. On return the session is already cleared,
/// so the supervisor's loss report never races a command onto a dead client.
fn serve_session(conn: Conn, shared: &Arc<PipeShared>, sink: &Arc<dyn EventSink>) {
    let client = WireClient::new(conn.writer);
    *shared.session.lock().unwrap() = Some(Arc::clone(&client));

    let reader_client = Arc::clone(&client);
    let reader_sink = Arc::clone(sink);
    let reader_stream = conn.reader;
    let reader = match thread::Builder::new()
        .name("tenebra-pipe-reader".into())
        .spawn(move || read_loop(reader_stream, reader_client, reader_sink))
    {
        Ok(handle) => handle,
        Err(e) => {
            *shared.session.lock().unwrap() = None;
            client.close();
            sink.log("error", &format!("could not start the pipe reader: {e}"));
            return;
        }
    };

    // The pipe buffers no events between sessions, so the state on connect is
    // whatever it already was: ask, and push the answer at the UI. A failure
    // here just means the session died at birth; the reader is about to end
    // and the supervisor will handle it.
    match client.request_into::<State>("status", obj([])) {
        Ok(state) => sink.state(&state),
        Err(e) => sink.log("warn", &format!("status re-sync failed: {e}")),
    }

    let _ = reader.join();
    *shared.session.lock().unwrap() = None;
}

/// Dials the OS pipe. Each dial produces a polling reader and an independent
/// writer handle over one connection.
struct PipeDialer {
    name: String,
    stop: Arc<AtomicBool>,
    /// How long the *next* dial waits for a name nothing has bound yet. The
    /// first dial spends it and leaves it at zero: startup is the one moment an
    /// absent listener is worth waiting out, because the choice of transport
    /// hangs on that single answer. The supervisor's redials must come back
    /// promptly instead — they already have their own backoff, and the grace
    /// escalation is timed against the moment each redial is due, so a dial that
    /// parked for seconds inside that schedule would delay the honest "the
    /// service is still unreachable" report it exists to produce.
    absent_wait: Duration,
}

impl Dial for PipeDialer {
    fn dial(&mut self) -> Result<Conn, String> {
        let absent_wait = std::mem::take(&mut self.absent_wait);
        let file = open_pipe(&self.name, &self.stop, absent_wait)
            .map_err(|e| format!("open {}: {e}", self.name))?;
        let writer = file
            .try_clone()
            .map_err(|e| format!("clone the pipe handle: {e}"))?;
        Ok(Conn {
            reader: Box::new(PollReader {
                file,
                stop: Arc::clone(&self.stop),
            }),
            writer: Box::new(writer),
        })
    }
}

/// Open the pipe as a plain file, waiting out the two failures that mean "ask
/// again in a moment": `ERROR_PIPE_BUSY` (the server exists but has no free
/// instance) for [`DIAL_BUSY_WAIT`], and `ERROR_FILE_NOT_FOUND` (nothing has
/// bound the name) for `absent_wait` — [`DIAL_ABSENT_WAIT`] on the first dial,
/// zero on every later one, see [`PipeDialer::absent_wait`]. Any other failure
/// is the caller's to judge: at startup it selects the sidecar fallback, mid-run
/// it feeds the reconnect backoff.
fn open_pipe(name: &str, stop: &Arc<AtomicBool>, absent_wait: Duration) -> io::Result<File> {
    let started = Instant::now();
    loop {
        let attempt = OpenOptions::new()
            .read(true)
            .write(true)
            // GENERIC_READ|WRITE matches the GRGW the pipe's DACL grants
            // interactive users. The SQOS flags cap impersonation at
            // identification: if something else ever squats an instance of the
            // name (the DACL admits any interactive user), it may learn who we
            // are but cannot act as us.
            .custom_flags(SECURITY_SQOS_PRESENT | SECURITY_IDENTIFICATION)
            .open(name);
        match attempt {
            Err(e)
                if dial_wait_for(&e, absent_wait)
                    .is_some_and(|window| started.elapsed() < window)
                    && !stop.load(Ordering::SeqCst) =>
            {
                thread::sleep(DIAL_RETRY_TICK)
            }
            other => return other,
        }
    }
}

/// How long a dial keeps re-attempting after a failure like this one, or `None`
/// when the failure is not one to wait out. Only the two transient shapes get a
/// window: `ERROR_PIPE_BUSY` (every instance is taken this instant) and
/// `ERROR_FILE_NOT_FOUND` (the name is not bound — nobody home, or not home
/// *yet*). Everything else, `ERROR_ACCESS_DENIED` above all, describes a
/// standing condition that a retry loop would only turn into a stall, so it goes
/// straight back to the caller.
fn dial_wait_for(e: &io::Error, absent_wait: Duration) -> Option<Duration> {
    match e.raw_os_error().map(|code| code as u32) {
        Some(ERROR_PIPE_BUSY) => Some(DIAL_BUSY_WAIT),
        Some(ERROR_FILE_NOT_FOUND) => Some(absent_wait),
        _ => None,
    }
}

/// `Read` over the pipe that never parks in `ReadFile` — see the module docs
/// for why that would deadlock writes. EOF (`Ok(0)`) covers both the peer
/// closing the pipe and our own shutdown flag, which is exactly the signal
/// `read_loop` ends on.
struct PollReader {
    file: File,
    stop: Arc<AtomicBool>,
}

impl Read for PollReader {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        loop {
            if self.stop.load(Ordering::SeqCst) {
                return Ok(0);
            }
            match pipe_bytes_available(&self.file) {
                // Data is ready, so this read returns immediately with some of
                // it; the brief file-object lock is exactly what keeps writers
                // safe alongside us.
                Ok(n) if n > 0 => return self.file.read(buf),
                Ok(_) => thread::sleep(POLL_INTERVAL),
                Err(e) if pipe_is_gone(&e) => return Ok(0),
                Err(e) => return Err(e),
            }
        }
    }
}

/// How many bytes a read could take right now without blocking.
fn pipe_bytes_available(file: &File) -> io::Result<u32> {
    let mut available: u32 = 0;
    // SAFETY: the handle is owned by `file` and outlives the call; a null
    // buffer with zero length is the documented way to only query availability.
    let ok = unsafe {
        PeekNamedPipe(
            file.as_raw_handle(),
            std::ptr::null_mut(),
            0,
            std::ptr::null_mut(),
            &mut available,
            std::ptr::null_mut(),
        )
    };
    if ok == 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(available)
    }
}

/// Whether an error from the pipe means the peer is gone (EOF for our
/// purposes) rather than something being wrong with the call itself.
fn pipe_is_gone(e: &io::Error) -> bool {
    matches!(
        e.raw_os_error().map(|code| code as u32),
        Some(ERROR_BROKEN_PIPE) | Some(ERROR_PIPE_NOT_CONNECTED) | Some(ERROR_NO_DATA)
    )
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
    fn name_from_maps_the_env_convention() {
        assert_eq!(name_from(None).as_deref(), Some(PIPE_NAME));
        assert_eq!(name_from(Some("")).as_deref(), Some(PIPE_NAME));
        assert_eq!(name_from(Some("off")), None);
        assert_eq!(name_from(Some("0")), None);
        assert_eq!(
            name_from(Some(r"\\.\pipe\tenebra-test")).as_deref(),
            Some(r"\\.\pipe\tenebra-test")
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
    /// the service stays unreachable.
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

    fn conn_from(end: ChanEnd) -> Conn {
        Conn {
            reader: Box::new(end.reader),
            writer: Box::new(end.writer),
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
        let backend = PipeBackend::start(
            conn_from(ours),
            dialer,
            Arc::clone(&sink) as Arc<dyn EventSink>,
            Arc::clone(&stop),
            RECONNECT_GRACE,
        )
        .expect("start pipe backend");

        // The re-sync pushes the service's current state without the UI asking.
        let states = sink.wait_for_states(1, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);
        assert_eq!(states[0].node.as_deref(), Some("n1"));

        // Commands flow through the same session.
        let state = backend.disconnect().expect("disconnect over the pipe");
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
        // The first service instance answers exactly one request (the re-sync)
        // and hangs up — a restart. The second serves normally.
        let stub1 = spawn_stub_core(theirs1, Arc::clone(&requests1), connected_state, Some(1));
        let stub2 = spawn_stub_core(theirs2, Arc::clone(&requests2), connected_state, None);

        let sink = Arc::new(Rec::default());
        // First redial fails (the service is still coming back), the next one
        // lands on the new instance — well inside the production grace window.
        let dialer = ScriptDialer {
            script: VecDeque::from([
                Err("the service is still down".to_string()),
                Ok(conn_from(ours2)),
            ]),
        };
        let backend = PipeBackend::start(
            conn_from(ours1),
            dialer,
            Arc::clone(&sink) as Arc<dyn EventSink>,
            Arc::clone(&stop),
            RECONNECT_GRACE,
        )
        .expect("start pipe backend");

        // connected (re-sync 1) → connecting (loss) → connected (re-sync 2).
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
    fn a_displaced_session_redials_at_once_and_stays_quiet() {
        // A displacement (another client took the pipe — last-writer-wins) is
        // an EOF with the listener still up, so the very first redial lands.
        // The UI sees a brief reconnecting state and then the re-synced real
        // state; nothing about the blip reads as a failure.
        let stop = Arc::new(AtomicBool::new(false));
        let (ours1, theirs1) = duplex(&stop);
        let (ours2, theirs2) = duplex(&stop);
        let requests1 = Arc::new(Mutex::new(Vec::new()));
        let requests2 = Arc::new(Mutex::new(Vec::new()));
        let stub1 = spawn_stub_core(theirs1, Arc::clone(&requests1), connected_state, Some(1));
        let stub2 = spawn_stub_core(theirs2, Arc::clone(&requests2), connected_state, None);

        let sink = Arc::new(Rec::default());
        let dialer = ScriptDialer {
            script: VecDeque::from([Ok(conn_from(ours2))]),
        };
        let backend = PipeBackend::start(
            conn_from(ours1),
            dialer,
            Arc::clone(&sink) as Arc<dyn EventSink>,
            Arc::clone(&stop),
            RECONNECT_GRACE,
        )
        .expect("start pipe backend");

        let states = sink.wait_for_states(3, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);
        assert_eq!(states[1].state, ConnectionState::Connecting);
        assert_eq!(states[2].state, ConnectionState::Connected);
        assert!(
            states.iter().all(|s| s.state != ConnectionState::Error),
            "an instant redial must not report an error: {states:?}"
        );

        // The retaken session re-synced, so the UI holds the real state again.
        let cmds2: Vec<String> = requests2
            .lock()
            .unwrap()
            .iter()
            .map(|r| r["cmd"].as_str().unwrap_or_default().to_string())
            .collect();
        assert_eq!(cmds2.first().map(String::as_str), Some("status"));

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
        // dial — in the middle of a backoff wait, which must wake for it.
        let grace = Duration::from_millis(600);
        let dialer = ScriptDialer {
            script: VecDeque::from([
                Err("still down".to_string()),
                Err("still down".to_string()),
                Ok(conn_from(ours2)),
            ]),
        };
        let backend = PipeBackend::start(
            conn_from(ours1),
            dialer,
            Arc::clone(&sink) as Arc<dyn EventSink>,
            Arc::clone(&stop),
            grace,
        )
        .expect("start pipe backend");

        // connected (re-sync 1) → connecting (loss) → error (grace expired) →
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
        let backend = PipeBackend::start(
            conn_from(ours),
            dialer,
            Arc::clone(&sink) as Arc<dyn EventSink>,
            Arc::clone(&stop),
            RECONNECT_GRACE,
        )
        .expect("start pipe backend");

        // Once the reconnecting state is out, the session is guaranteed cleared
        // (the supervisor clears it before reporting), so a command must fail
        // fast with the reconnecting error rather than riding out a timeout —
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

    // --- the real OS pipe ----------------------------------------------------
    //
    // An in-process named-pipe server (the same CreateNamedPipeW surface the
    // core's winio listener uses, byte mode, one instance) proves the client
    // against real npfs semantics: that the poll-reader and concurrent writes
    // coexist on one synchronous handle, that a server hangup surfaces as the
    // loss/reconnect path, and that a dial with nobody listening fails cleanly.

    use std::os::windows::io::FromRawHandle;
    use std::sync::atomic::AtomicUsize;
    use windows_sys::Win32::Foundation::{
        ERROR_ACCESS_DENIED, ERROR_PIPE_CONNECTED, INVALID_HANDLE_VALUE,
    };
    use windows_sys::Win32::Storage::FileSystem::{
        FILE_FLAG_FIRST_PIPE_INSTANCE, PIPE_ACCESS_DUPLEX,
    };
    use windows_sys::Win32::System::Pipes::{
        ConnectNamedPipe, CreateNamedPipeW, PIPE_READMODE_BYTE, PIPE_TYPE_BYTE, PIPE_WAIT,
    };

    /// A pipe name unique to this test process and case, so parallel test runs
    /// never collide on the namespace.
    fn unique_pipe_name(tag: &str) -> String {
        static COUNTER: AtomicUsize = AtomicUsize::new(0);
        format!(
            r"\\.\pipe\tenebra-test-{}-{}-{}",
            std::process::id(),
            tag,
            COUNTER.fetch_add(1, Ordering::SeqCst)
        )
    }

    /// Create one byte-mode duplex instance of `name` and block until a client
    /// connects, returning the connected stream as a `File`. Runs entirely on
    /// the calling (server) thread.
    fn accept_one(name: &str, first_instance: bool) -> File {
        let wide: Vec<u16> = name.encode_utf16().chain(std::iter::once(0)).collect();
        let mut open_mode = PIPE_ACCESS_DUPLEX;
        if first_instance {
            open_mode |= FILE_FLAG_FIRST_PIPE_INSTANCE;
        }
        // SAFETY: the name is a valid NUL-terminated wide string for the
        // duration of the call; null security attributes take the defaults.
        let handle = unsafe {
            CreateNamedPipeW(
                wide.as_ptr(),
                open_mode,
                PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT,
                1,
                64 * 1024,
                64 * 1024,
                0,
                std::ptr::null(),
            )
        };
        assert!(
            handle != INVALID_HANDLE_VALUE,
            "CreateNamedPipeW({name}) failed: {}",
            io::Error::last_os_error()
        );
        // SAFETY: the handle is a valid pipe instance we own.
        let connected = unsafe { ConnectNamedPipe(handle, std::ptr::null_mut()) };
        if connected == 0 {
            let err = io::Error::last_os_error();
            assert_eq!(
                err.raw_os_error().map(|code| code as u32),
                Some(ERROR_PIPE_CONNECTED),
                "ConnectNamedPipe({name}) failed: {err}"
            );
        }
        // SAFETY: ownership of the connected instance handle moves into the
        // File, which closes it on drop.
        unsafe { File::from_raw_handle(handle as _) }
    }

    /// Serve requests on a connected instance until EOF or `response_limit`,
    /// pushing `extra_event` (if any) right after the first response.
    fn serve_requests(
        conn: &File,
        requests: &Arc<Mutex<Vec<Value>>>,
        response_limit: Option<usize>,
        extra_event: Option<&str>,
    ) {
        let mut served = 0usize;
        let mut lines = BufReader::new(conn).lines();
        while let Some(Ok(line)) = lines.next() {
            if line.trim().is_empty() {
                continue;
            }
            let req: Value = serde_json::from_str(&line).expect("request is JSON");
            let id = req["id"].as_u64().expect("request carries an id");
            let cmd = req["cmd"].as_str().unwrap_or_default().to_string();
            requests.lock().unwrap().push(req);
            let reply = json!({ "id": id, "ok": true, "data": connected_state(&cmd) });
            let mut writer = conn;
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

    #[test]
    fn real_pipe_serves_commands_and_events() {
        let name = unique_pipe_name("serve");
        let requests = Arc::new(Mutex::new(Vec::new()));
        let server_requests = Arc::clone(&requests);
        let server_name = name.clone();
        let server = thread::spawn(move || {
            let conn = accept_one(&server_name, true);
            serve_requests(
                &conn,
                &server_requests,
                None,
                Some(r#"{"event":"log","level":"info","msg":"hello from the service"}"#),
            );
        });

        let sink = Arc::new(Rec::default());
        let backend = retry_connect(&name, Arc::clone(&sink));

        // Re-sync state arrived without any command from us.
        let states = sink.wait_for_states(1, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);

        // An unsolicited event crossed the same stream.
        wait_until(WAIT, || {
            sink.logs
                .lock()
                .unwrap()
                .iter()
                .any(|(_, msg)| msg.contains("hello from the service"))
        });

        // A command round-trips while the poll-reader idles on the same handle
        // — the write would deadlock if reads parked blocking (module docs).
        let state = backend.status().expect("status over the real pipe");
        assert_eq!(state.state, ConnectionState::Connected);

        drop(backend); // closes the client end; the server sees EOF
        server.join().expect("pipe server thread");
    }

    #[test]
    fn real_pipe_reconnects_after_a_server_restart() {
        let name = unique_pipe_name("restart");
        let requests = Arc::new(Mutex::new(Vec::new()));
        let server_requests = Arc::clone(&requests);
        let server_name = name.clone();
        let server = thread::spawn(move || {
            // First life: answer the re-sync, then hang up (restart).
            let conn = accept_one(&server_name, true);
            serve_requests(&conn, &server_requests, Some(1), None);
            drop(conn);
            // Second life: a fresh instance on the same name.
            let conn = accept_one(&server_name, true);
            serve_requests(&conn, &server_requests, None, None);
        });

        let sink = Arc::new(Rec::default());
        let backend = retry_connect(&name, Arc::clone(&sink));

        // connected → connecting (hangup, the redial lands inside the grace
        // window) → connected (redial + re-sync).
        let states = sink.wait_for_states(3, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);
        assert_eq!(states[1].state, ConnectionState::Connecting);
        assert_eq!(states[2].state, ConnectionState::Connected);

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
        server.join().expect("pipe server thread");
    }

    #[test]
    fn connect_fails_cleanly_with_nobody_listening() {
        // With no patience budget (the production one is exercised below), a
        // name nothing has bound must fail at once rather than wait: this is the
        // answer `make_backend` turns into the sidecar fallback, and a machine
        // that simply has no service should not pay for the verdict twice.
        let name = unique_pipe_name("absent");
        let sink: Arc<dyn EventSink> = Arc::new(Rec::default());
        let started = Instant::now();
        let err = match PipeBackend::connect_within(&name, sink, Duration::ZERO) {
            Ok(_) => panic!("dialing a nonexistent pipe must fail"),
            Err(e) => e,
        };
        assert!(err.contains(&name), "the error should name the pipe: {err}");
        assert!(
            started.elapsed() < Duration::from_secs(1),
            "a dial with no patience budget must fail fast, took {:?}",
            started.elapsed()
        );
    }

    #[test]
    fn dial_waits_out_only_the_transient_failures() {
        let absent_wait = Duration::from_secs(5);
        // Busy keeps its own short window: the server is there, just between
        // instances.
        assert_eq!(
            dial_wait_for(
                &io::Error::from_raw_os_error(ERROR_PIPE_BUSY as i32),
                absent_wait
            ),
            Some(DIAL_BUSY_WAIT)
        );
        // "No such pipe" is the one the startup race turns on, so it gets the
        // caller's budget — which is zero for the supervisor's redials.
        assert_eq!(
            dial_wait_for(
                &io::Error::from_raw_os_error(ERROR_FILE_NOT_FOUND as i32),
                absent_wait
            ),
            Some(absent_wait)
        );
        assert_eq!(
            dial_wait_for(
                &io::Error::from_raw_os_error(ERROR_FILE_NOT_FOUND as i32),
                Duration::ZERO
            ),
            Some(Duration::ZERO)
        );
        // A standing refusal must not be retried for seconds; the caller decides
        // what it means.
        assert_eq!(
            dial_wait_for(
                &io::Error::from_raw_os_error(ERROR_ACCESS_DENIED as i32),
                absent_wait
            ),
            None
        );
    }

    #[test]
    fn the_first_dial_waits_out_a_service_that_is_still_starting() {
        // The production race this transport lives with: the SCM has been told
        // to start the service (the installer's `sc start`, or an autostart
        // login) but its listener is not up yet, so the very first dial hits
        // ERROR_FILE_NOT_FOUND. Giving up there is not a small miss — the GUI
        // picks its transport exactly once, so it spends the whole run on an
        // unprivileged sidecar with a different profile store.
        let name = unique_pipe_name("late");
        let server_name = name.clone();
        let requests = Arc::new(Mutex::new(Vec::new()));
        let server_requests = Arc::clone(&requests);
        let server = thread::spawn(move || {
            thread::sleep(Duration::from_millis(600));
            let conn = accept_one(&server_name, true);
            serve_requests(&conn, &server_requests, None, None);
        });

        let sink = Arc::new(Rec::default());
        let backend = PipeBackend::connect(&name, Arc::clone(&sink) as Arc<dyn EventSink>)
            .expect("the first dial must wait out a service that is still starting");

        // And the session that came out of it is a real one: the re-sync landed.
        let states = sink.wait_for_states(1, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);

        drop(backend);
        server.join().expect("pipe server thread");
    }

    #[test]
    fn a_probe_sees_a_listener_without_taking_its_session() {
        // The probe behind the late-service watch runs while some other client
        // may be on the pipe, and a dial would displace it — sessions are
        // last-writer-wins. So it must answer "is anyone listening?" without
        // opening a session: the instance it reports has to still be there,
        // unconsumed, for the client that actually wants it.
        let name = unique_pipe_name("probe");
        assert!(!is_listening(&name), "nothing has bound {name} yet");

        let server_name = name.clone();
        let requests = Arc::new(Mutex::new(Vec::new()));
        let server_requests = Arc::clone(&requests);
        let server = thread::spawn(move || {
            let conn = accept_one(&server_name, true);
            serve_requests(&conn, &server_requests, None, None);
        });

        // The instance exists from CreateNamedPipeW on, before anyone connects.
        wait_until(WAIT, || is_listening(&name));
        // Asking again changes nothing: the waiting instance is still free.
        assert!(
            is_listening(&name),
            "the probe must not consume the listener"
        );

        // And that same untouched instance is what a real client connects to.
        let sink = Arc::new(Rec::default());
        let backend = retry_connect(&name, Arc::clone(&sink));
        let states = sink.wait_for_states(1, WAIT);
        assert_eq!(states[0].state, ConnectionState::Connected);

        drop(backend);
        server.join().expect("pipe server thread");
    }

    /// Dial until the server thread's instance is up — the tests create it on
    /// another thread, so the very first dial can lose the race.
    fn retry_connect(name: &str, sink: Arc<Rec>) -> PipeBackend {
        let deadline = Instant::now() + WAIT;
        loop {
            match PipeBackend::connect(name, Arc::clone(&sink) as Arc<dyn EventSink>) {
                Ok(backend) => return backend,
                Err(e) => {
                    assert!(
                        Instant::now() < deadline,
                        "could not connect to {name} in time: {e}"
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

    // --- the real core over the real pipe ------------------------------------

    /// Locate the built core binary, or `None` if it isn't present yet — the
    /// same skip contract as `tests/sidecar_e2e.rs`, so `cargo test` stays
    /// green on a fresh checkout.
    fn core_binary() -> Option<std::path::PathBuf> {
        let manifest = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        let exe = std::env::consts::EXE_SUFFIX;
        let triple = env!("TENEBRA_TARGET_TRIPLE");
        [
            manifest.join(format!("binaries/tenebra-core-{triple}{exe}")),
            manifest.join(format!("binaries/tenebra-core{exe}")),
        ]
        .into_iter()
        .find(|p| p.exists())
    }

    /// Kills the spawned core on drop, so a failed assertion can't leak a
    /// process that keeps holding the well-known pipe name.
    struct KillOnDrop(std::process::Child);

    impl Drop for KillOnDrop {
        fn drop(&mut self) {
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }

    /// The full cross-implementation round-trip: this client against the real
    /// Go core serving `--pipe`, on the well-known name — the exact production
    /// wiring of the service transport. Catches framing or session-semantics
    /// drift between the two sides that the in-process stub server cannot.
    ///
    /// `--pipe` always serves `\\.\pipe\tenebra`, so this test would collide
    /// with an actually-installed service; the core fails to claim the name in
    /// that case (FILE_FLAG_FIRST_PIPE_INSTANCE) and the test surfaces it
    /// rather than silently driving the wrong daemon.
    #[test]
    fn real_core_serves_the_well_known_pipe() {
        let Some(program) = core_binary() else {
            eprintln!("SKIP: tenebra-core binary not built; see tests/sidecar_e2e.rs");
            return;
        };

        // Isolate the profile store; a bogus sing-box path is fine because
        // nothing here starts a tunnel.
        let store = std::env::temp_dir().join(format!("tenebra-pipe-e2e-{}", std::process::id()));
        let _ = std::fs::create_dir_all(&store);
        let _core = KillOnDrop(
            std::process::Command::new(&program)
                .arg("--pipe")
                .env("TENEBRA_SINGBOX", "sing-box-not-needed-for-this-test")
                .env("TENEBRA_CONFIG_DIR", &store)
                .stdin(std::process::Stdio::null())
                .stdout(std::process::Stdio::null())
                .stderr(std::process::Stdio::null())
                .spawn()
                .expect("spawn tenebra-core --pipe"),
        );

        // The listener takes a moment to come up; retry_connect covers it.
        let sink = Arc::new(Rec::default());
        let backend = retry_connect(PIPE_NAME, Arc::clone(&sink));

        // The re-sync pushed the core's actual state (idle on a fresh store).
        let states = sink.wait_for_states(1, WAIT);
        assert_eq!(states[0].state, ConnectionState::Idle);

        // And a command round-trips against the real dispatcher.
        let state = backend.status().expect("status against the real core");
        assert_eq!(state.state, ConnectionState::Idle);

        // Drop order: the backend disconnects first, then KillOnDrop reaps the
        // core.
        drop(backend);
    }
}
