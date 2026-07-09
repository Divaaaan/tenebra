//! Test-only plumbing shared by the transport tests: an in-memory duplex byte
//! stream (so the wire client and the pipe supervisor can be exercised without
//! a process or an OS pipe) and a recording [`EventSink`].

use std::collections::VecDeque;
use std::io::{self, Read, Write};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use super::{EventSink, State};

/// How often a blocked [`ChanReader`] rechecks its stop flag. Mirrors the real
/// pipe transport, whose reader also wakes periodically rather than parking in
/// a blocking read.
const READ_TICK: Duration = Duration::from_millis(10);

/// One end of an in-memory duplex stream.
pub struct ChanEnd {
    pub reader: ChanReader,
    pub writer: ChanWriter,
}

/// Two connected stream ends: bytes written to one are read from the other.
/// Readers on both ends watch the same `stop` flag, so a test (or the pipe
/// supervisor's shutdown) can unstick a reader whose peer is still open.
pub fn duplex(stop: &Arc<AtomicBool>) -> (ChanEnd, ChanEnd) {
    let (a_tx, a_rx) = mpsc::channel();
    let (b_tx, b_rx) = mpsc::channel();
    (
        ChanEnd {
            reader: ChanReader::new(a_rx, Arc::clone(stop)),
            writer: ChanWriter { tx: b_tx },
        },
        ChanEnd {
            reader: ChanReader::new(b_rx, Arc::clone(stop)),
            writer: ChanWriter { tx: a_tx },
        },
    )
}

/// The write half: each write becomes one chunk on the channel. Reports
/// `BrokenPipe` once the peer's reader is gone, like a real stream.
pub struct ChanWriter {
    tx: Sender<Vec<u8>>,
}

impl Write for ChanWriter {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        self.tx
            .send(buf.to_vec())
            .map_err(|_| io::Error::new(io::ErrorKind::BrokenPipe, "peer reader is gone"))?;
        Ok(buf.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

/// The read half: blocks until bytes arrive, the peer's writer drops (EOF), or
/// the stop flag is raised (also EOF, so a reader loop ends cleanly).
pub struct ChanReader {
    rx: Receiver<Vec<u8>>,
    buffered: VecDeque<u8>,
    stop: Arc<AtomicBool>,
}

impl ChanReader {
    fn new(rx: Receiver<Vec<u8>>, stop: Arc<AtomicBool>) -> Self {
        Self {
            rx,
            buffered: VecDeque::new(),
            stop,
        }
    }
}

impl Read for ChanReader {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        loop {
            if !self.buffered.is_empty() {
                let n = buf.len().min(self.buffered.len());
                for slot in buf.iter_mut().take(n) {
                    *slot = self.buffered.pop_front().expect("checked non-empty");
                }
                return Ok(n);
            }
            if self.stop.load(Ordering::SeqCst) {
                return Ok(0);
            }
            match self.rx.recv_timeout(READ_TICK) {
                Ok(chunk) => self.buffered.extend(chunk),
                Err(RecvTimeoutError::Timeout) => continue,
                Err(RecvTimeoutError::Disconnected) => return Ok(0),
            }
        }
    }
}

/// A recording sink: stores everything the backend pushes so tests can assert
/// on it.
#[derive(Default)]
pub struct Rec {
    pub states: Mutex<Vec<State>>,
    pub traffic: Mutex<Vec<(u64, u64, u64, u64)>>,
    pub logs: Mutex<Vec<(String, String)>>,
    pub profiles: AtomicUsize,
}

impl EventSink for Rec {
    fn state(&self, state: &State) {
        self.states.lock().unwrap().push(state.clone());
    }
    fn traffic(&self, up: u64, down: u64, up_rate: u64, down_rate: u64) {
        self.traffic
            .lock()
            .unwrap()
            .push((up, down, up_rate, down_rate));
    }
    fn log(&self, level: &str, msg: &str) {
        self.logs
            .lock()
            .unwrap()
            .push((level.to_string(), msg.to_string()));
    }
    fn profiles(&self) {
        self.profiles.fetch_add(1, Ordering::SeqCst);
    }
}
