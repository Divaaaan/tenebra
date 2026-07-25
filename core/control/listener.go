package control

import (
	"context"
	"net"
	"sync"
)

// ServeListener runs the control protocol over connections accepted from l. It
// is the transport for the Windows service (and the --pipe console mode), where
// the daemon outlives any one client: the UI comes and goes, the tunnel stays.
//
// Exactly one session is active at a time. A new connection displaces the
// current one — the usual cause is the UI restarting and reconnecting before
// its old stream is reaped — by closing the old stream and pointing the
// daemon's events at the new one. Neither a displacement nor a client's own
// EOF touches the daemon's connection state. Only ctx cancellation (or a
// listener failure) closes the daemon, tearing down a live tunnel, before
// ServeListener returns.
//
// Nothing a client does may stop this loop from accepting. That is not a nicety:
// a listener that stops accepting is indistinguishable from a dead service to
// every UI (on Windows the pipe instance only exists while Accept is in flight,
// so dials fail with ERROR_SEM_TIMEOUT), while the service still reports Running
// and the tunnel still carries traffic — a state only a service restart clears.
// So displacement closes the old session and moves on rather than waiting for
// its goroutine, which may still be finishing a command; the closed session
// unwinds on its own and is joined at shutdown. Writes to a client can't stall
// this loop either, because the Server queues them (see Server).
func ServeListener(ctx context.Context, d *Daemon, l net.Listener) error {
	// The subscription auto-refresh runs for the whole listener lifetime, not
	// per session, so profiles stay fresh while no UI is attached. Same
	// stop-then-wait ordering as Serve.
	bgCtx, stopBg := context.WithCancel(ctx)
	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		d.runAutoRefresh(bgCtx)
	}()
	defer bg.Wait()
	defer stopBg()

	// Accept blocks, so cancellation must close the listener to unblock it.
	unregister := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer unregister()
	defer func() { _ = l.Close() }()

	// sessions counts every session started here, including ones already
	// displaced but still unwinding, so shutdown can wait for all of them.
	var sessions sync.WaitGroup
	var cur *session
	for {
		conn, err := l.Accept()
		if err != nil {
			// Shutting down, or the listener itself failed. Every session's turn is
			// over and the process is about to exit, so tear them down — waiting for
			// them, as before, so no request handling outlives this — and take the
			// tunnel with them.
			if cur != nil {
				cur.close()
				cur = nil
			}
			sessions.Wait()
			_ = d.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		// Authenticate the peer before it can displace the live session or reach
		// the daemon. The channel is world-reachable by design, so a local user
		// who isn't the one the GUI runs as (see peerAllowed) must be turned away
		// here — and, crucially, turned away WITHOUT stopping the current
		// authorized session, so an unauthorized connect can't even be used to
		// kick the real GUI off. An authorized client still displaces the old one
		// with the usual takeover semantics.
		if !d.authorizePeer(conn) {
			d.emitLog(LogWarn, "control: rejecting unauthorized local peer")
			_ = conn.Close()
			continue
		}
		// A new authorized client displaces the current one: closing it ends its
		// stream and cancels any in-flight command, but leaves the daemon (and any
		// live tunnel) untouched. We do not wait for its goroutine here — a command
		// that is still unwinding would otherwise hold up this client's turn, and
		// the invariant that matters ("one active session, events go to it") is
		// established by the new Server installing itself as the daemon's emitter,
		// not by the old goroutine having returned. A late response from the old
		// session goes to its own closed stream and is discarded.
		if cur != nil {
			cur.close()
			cur = nil
		}
		cur = startSession(ctx, d, conn, &sessions)
	}
}

// session is one client stream being served in its own goroutine.
type session struct {
	cancel context.CancelFunc
	conn   net.Conn
}

// startSession wires conn to the daemon and serves it in the background.
// Constructing the Server points the daemon's event emitter at this client. The
// session ends on its own when the client goes away or stops taking delivery;
// close ends it from the outside (displacement or shutdown). It registers with
// wg for the duration, so the listener can wait every session out at shutdown.
func startSession(ctx context.Context, d *Daemon, conn net.Conn, wg *sync.WaitGroup) *session {
	sctx, cancel := context.WithCancel(ctx)
	s := &session{cancel: cancel, conn: conn}
	srv := NewServer(d, conn, conn)

	// A client that stops taking delivery is dropped by its writer (see Server);
	// end the session with it, so the daemon isn't left emitting into a stream
	// nobody reads and the next client's takeover finds nothing to do. The watcher
	// exits with the session either way.
	go func() {
		select {
		case <-srv.stalledStream():
			cancel()
			_ = conn.Close()
		case <-sctx.Done():
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		_ = srv.serveStream(sctx)
		// Close the conn so serveStream's scanner goroutine, which may still be
		// blocked in a read, unblocks and no half-open stream lingers. Until the
		// next session is installed, events the daemon emits at this dead writer
		// fail and are dropped — the protocol has no backlog, and a reconnecting
		// client re-syncs with a status request. Closing first also makes stopping
		// the writer immediate: any frame it is mid-write on fails at once.
		_ = conn.Close()
		srv.stopWriter()
	}()
	return s
}

// close cancels the session (aborting any in-flight command's network work) and
// closes its stream. It does not wait for the serving goroutine: the caller is
// the accept loop, and a command still unwinding must not hold it. Everything
// the session owns is its own — its conn and its writer — so the goroutine can
// finish unobserved, and the listener joins it at shutdown.
func (s *session) close() {
	s.cancel()
	_ = s.conn.Close()
}
