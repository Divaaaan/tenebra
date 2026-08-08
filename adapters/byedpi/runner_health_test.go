package byedpi

import (
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Divaaaan/tenebra/core/dpi"
)

// serveSocksGreetings answers the SOCKS5 no-auth handshake on every connection,
// the way ciadpi's listener does, until l is closed.
func serveSocksGreetings(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			var greeting [3]byte
			if _, err := io.ReadFull(conn, greeting[:]); err != nil {
				return
			}
			conn.Write([]byte{0x05, 0x00})
		}()
	}
}

// fakeSocks starts a stand-in SOCKS5 listener on loopback and returns its
// address.
func fakeSocks(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go serveSocksGreetings(l)
	return l.Addr().String()
}

// fakeListener starts a loopback listener whose accepted connections are handed
// to handle, for the "something is listening but it isn't ciadpi" cases.
func fakeListener(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	return l.Addr().String()
}

// deadAddr returns a loopback address nothing is listening on.
func deadAddr(t *testing.T) string {
	t.Helper()
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(dpi.FreePort()))
}

func TestReadyAcceptsSocksListener(t *testing.T) {
	r := New()
	if err := r.Ready(context.Background(), fakeSocks(t)); err != nil {
		t.Errorf("Ready against a SOCKS5 listener = %v, want nil", err)
	}
}

func TestReadyRejectsSquattedPort(t *testing.T) {
	// A bare TCP dial would pass here: something is listening, it just isn't a
	// SOCKS proxy. That is exactly the case a port picked ahead of the spawn can
	// land in, so the greeting has to be part of the check.
	addr := fakeListener(t, func(conn net.Conn) {
		defer conn.Close()
		io.Copy(io.Discard, conn) // read the greeting, answer nothing sensible
	})
	// A silent peer would otherwise be held until the probe's own budget runs
	// out; the shorter context deadline must win, which is what lets a connect
	// bound the whole check.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := r0().Ready(ctx, addr); err == nil {
		t.Error("Ready against a non-SOCKS listener = nil, want error")
	}
	if elapsed := time.Since(start); elapsed >= dialTimeout {
		t.Errorf("Ready took %s, ignoring the context deadline", elapsed)
	}
}

func TestReadyRejectsWrongProtocolAnswer(t *testing.T) {
	addr := fakeListener(t, func(conn net.Conn) {
		defer conn.Close()
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
	})
	err := r0().Ready(context.Background(), addr)
	if err == nil {
		t.Fatal("Ready against an HTTP server = nil, want error")
	}
	if !strings.Contains(err.Error(), "SOCKS5") {
		t.Errorf("error %q does not explain the protocol mismatch", err)
	}
}

func TestReadyRejectsImmediateClose(t *testing.T) {
	addr := fakeListener(t, func(conn net.Conn) { conn.Close() })
	if err := r0().Ready(context.Background(), addr); err == nil {
		t.Error("Ready against a listener that hangs up = nil, want error")
	}
}

func TestReadyFailsWhenNothingListens(t *testing.T) {
	if err := r0().Ready(context.Background(), deadAddr(t)); err == nil {
		t.Error("Ready against a dead port = nil, want error")
	}
}

func TestReadyRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r0().Ready(ctx, fakeSocks(t)); err == nil {
		t.Error("Ready with a cancelled context = nil, want error")
	}
}

func TestWaitReadyWaitsForTheListener(t *testing.T) {
	r := startFake(t, "block")
	addr := deadAddr(t)
	stop := socksLater(t, addr, 150*time.Millisecond)
	defer stop()

	if err := r.WaitReady(context.Background(), addr, 5*time.Second); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestWaitReadyGivesUpWhenTheProcessExits(t *testing.T) {
	r := startFake(t, "quiet")
	if err := <-r.Done(); err != nil {
		t.Fatalf("quiet child exited with %v", err)
	}

	// The wait budget is generous on purpose: the point is that a dead process
	// short-circuits it instead of burning the whole window.
	start := time.Now()
	err := r.WaitReady(context.Background(), deadAddr(t), 10*time.Second)
	if err == nil {
		t.Fatal("WaitReady after the process exited = nil, want error")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error %q does not say the process is gone", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("WaitReady burned %s waiting on a dead process", elapsed)
	}
}

func TestWaitReadyWithoutStart(t *testing.T) {
	if err := r0().WaitReady(context.Background(), deadAddr(t), time.Second); err == nil {
		t.Error("WaitReady before any Start = nil, want error")
	}
}

func TestWaitReadyRespectsContext(t *testing.T) {
	r := startFake(t, "block")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if err := r.WaitReady(ctx, deadAddr(t), 10*time.Second); err == nil {
		t.Fatal("WaitReady with a cancelled context = nil, want error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("WaitReady ignored the cancelled context for %s", elapsed)
	}
}

// r0 is a runner that never spawned anything, for the probe cases that only
// exercise the network side.
func r0() *Runner { return New() }

// socksLater binds addr after delay and answers SOCKS greetings from then on,
// so a wait has something to actually wait for. The returned func closes the
// listener and joins the goroutine.
func socksLater(t *testing.T, addr string, delay time.Duration) func() {
	t.Helper()
	var (
		mu   sync.Mutex
		ln   net.Listener
		done = make(chan struct{})
	)
	go func() {
		defer close(done)
		time.Sleep(delay)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return // the wait will time out and report it
		}
		mu.Lock()
		ln = l
		mu.Unlock()
		serveSocksGreetings(l)
	}()
	return func() {
		mu.Lock()
		l := ln
		mu.Unlock()
		if l != nil {
			l.Close()
		}
		<-done
	}
}
