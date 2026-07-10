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

	var cur *session
	for {
		conn, err := l.Accept()
		if err != nil {
			// Shutting down, or the listener itself failed. The current session's
			// turn is over and the process is about to exit, so tear it down and
			// take the tunnel with it.
			if cur != nil {
				cur.stop()
				cur = nil
			}
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
		// A new authorized client displaces the current one: stopping it closes
		// its stream and waits the serving goroutine out, but leaves the daemon
		// (and any live tunnel) untouched.
		if cur != nil {
			cur.stop()
			cur = nil
		}
		cur = startSession(ctx, d, conn)
	}
}

// session is one client stream being served in its own goroutine.
type session struct {
	cancel context.CancelFunc
	conn   net.Conn
	done   chan struct{}
}

// startSession wires conn to the daemon and serves it in the background.
// Constructing the Server points the daemon's event emitter at this client. The
// session ends on its own when the client goes away; stop ends it from the
// outside (displacement or shutdown).
func startSession(ctx context.Context, d *Daemon, conn net.Conn) *session {
	sctx, cancel := context.WithCancel(ctx)
	s := &session{cancel: cancel, conn: conn, done: make(chan struct{})}
	srv := NewServer(d, conn, conn)
	go func() {
		defer close(s.done)
		defer cancel()
		_ = srv.serveStream(sctx)
		// Close the conn so serveStream's scanner goroutine, which may still be
		// blocked in a read, unblocks and no half-open stream lingers. Until the
		// next session is installed, events the daemon emits at this dead writer
		// fail and are dropped — the protocol has no backlog, and a reconnecting
		// client re-syncs with a status request.
		_ = conn.Close()
	}()
	return s
}

// stop cancels the session (aborting any in-flight command's network work),
// closes its stream, and waits for the serving goroutine to finish, so no read
// or request handling happens on this conn after it returns.
func (s *session) stop() {
	s.cancel()
	_ = s.conn.Close()
	<-s.done
}
