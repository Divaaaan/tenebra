use super::*;
use std::io;
use std::thread;

struct BlockedWriter {
    entered: Sender<()>,
    release: Receiver<()>,
}

impl Write for BlockedWriter {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        let _ = self.entered.send(());
        let _ = self.release.recv();
        Ok(buf.len())
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

#[test]
fn close_releases_caller_while_writer_is_blocked() {
    let (entered_tx, entered_rx) = mpsc::channel();
    let (release_tx, release_rx) = mpsc::channel();
    let client = WireClient::new(BlockedWriter {
        entered: entered_tx,
        release: release_rx,
    });
    let request_client = Arc::clone(&client);
    let (done_tx, done_rx) = mpsc::channel();
    let handle = thread::spawn(move || {
        let _ = done_tx.send(request_client.request("status", json!({})));
    });
    entered_rx.recv_timeout(Duration::from_secs(2)).unwrap();
    client.close();
    let result = done_rx.recv_timeout(Duration::from_millis(200));
    // Release the controlled writer even on RED: this test never leaks a thread.
    drop(release_tx);
    handle.join().unwrap();
    assert!(
        result.is_ok(),
        "close must release the request without waiting for the writer"
    );
    assert!(result.unwrap().is_err());
}

#[test]
fn deadline_bounds_blocked_write_and_queued_commands() {
    let (entered_tx, entered_rx) = mpsc::channel();
    let (release_tx, release_rx) = mpsc::channel();
    let cancelled = Arc::new(AtomicBool::new(false));
    let cancel_flag = Arc::clone(&cancelled);
    let client = WireClient::new_cancellable(
        BlockedWriter {
            entered: entered_tx,
            release: release_rx,
        },
        Arc::new(move || {
            cancel_flag.store(true, Ordering::SeqCst);
        }),
    );
    let first = Arc::clone(&client);
    let caller = thread::spawn(move || {
        first.request_with_timeout("import_links", json!({}), Duration::from_millis(100))
    });
    entered_rx.recv_timeout(Duration::from_secs(2)).unwrap();
    let started = Instant::now();
    let queued = client.request_with_timeout("disconnect", json!({}), Duration::from_millis(300));
    drop(release_tx);
    assert!(caller.join().unwrap().unwrap_err().contains("in time"));
    assert!(queued.is_err());
    assert!(started.elapsed() < Duration::from_secs(1));
    assert!(cancelled.load(Ordering::SeqCst));
    assert!(client.pending.lock().unwrap().is_empty());
}

#[test]
fn close_and_registration_race_never_strands_a_waiter() {
    for _ in 0..100 {
        let client = WireClient::new(io::sink());
        let other = Arc::clone(&client);
        let caller = thread::spawn(move || {
            other.request_with_timeout("status", json!({}), Duration::from_secs(2))
        });
        client.close();
        let started = Instant::now();
        assert!(caller.join().unwrap().is_err());
        assert!(started.elapsed() < Duration::from_millis(500));
    }
}

#[test]
fn queued_frames_are_not_written_after_cancellation() {
    let (entered_tx, entered_rx) = mpsc::channel();
    let (release_tx, release_rx) = mpsc::channel();
    let client = WireClient::new(BlockedWriter {
        entered: entered_tx,
        release: release_rx,
    });
    let first = Arc::clone(&client);
    let caller = thread::spawn(move || first.request("status", json!({})));
    entered_rx.recv_timeout(Duration::from_secs(2)).unwrap();
    client
        .writer
        .try_send(Outbound {
            line: b"must not send\n".to_vec(),
            deadline: Instant::now() + Duration::from_secs(5),
        })
        .unwrap();
    client.close();
    drop(release_tx);
    assert!(caller.join().unwrap().is_err());
    assert!(entered_rx.recv_timeout(Duration::from_millis(200)).is_err());
}

#[test]
fn expired_request_never_reaches_the_stream() {
    let (entered_tx, entered_rx) = mpsc::channel();
    let (_release_tx, release_rx) = mpsc::channel();
    let client = WireClient::new(BlockedWriter {
        entered: entered_tx,
        release: release_rx,
    });
    assert!(client
        .request_with_timeout("connect", json!({}), Duration::ZERO)
        .is_err());
    assert!(entered_rx.recv_timeout(Duration::from_millis(100)).is_err());
}
