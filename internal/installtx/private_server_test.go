package installtx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type observedPrivateConn struct {
	net.Conn
	reads *atomic.Int32
}

func (c *observedPrivateConn) Read(p []byte) (int, error) { c.reads.Add(1); return c.Conn.Read(p) }

type observedPrivateListener struct {
	net.Listener
	reads *atomic.Int32
}

func (l observedPrivateListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	return &observedPrivateConn{Conn: c, reads: l.reads}, nil
}
func privateEndpointFixture() PrivateEndpoint {
	e := readyFixture()
	return PrivateEndpoint{TxID: e.TxID, Info: func(context.Context) (ReadyReply, error) { return readyReply(e, ReadyRequest{}), nil },
		Admit: func(context.Context, string, string) error { return errors.New("SYNTHETIC_ONLY /private/source/path") }}
}
func TestPrivateListenerRejectsPeerBeforeReadingOrCallingHandler(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reads atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- servePrivate(ctx, observedPrivateListener{Listener: listener, reads: &reads}, privateEndpointFixture(), func(net.Conn) bool { return false })
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client.SetDeadline(time.Now().Add(time.Second))
	client.Write([]byte("{\"schema\":1}\n"))
	var body [1]byte
	client.Read(body[:])
	client.Close()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if reads.Load() != 0 {
		t.Fatal("unauthorized peer reached protocol decoder")
	}
}
func TestPrivateListenerFramesReadyAndRedactsAdmissionFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- servePrivate(ctx, listener, privateEndpointFixture(), func(net.Conn) bool { return true })
	}()
	exchange := func(request PrivateRequest) []byte {
		t.Helper()
		client, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		client.SetDeadline(time.Now().Add(time.Second))
		data, _ := json.Marshal(request)
		if _, err := client.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(client).ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		return line
	}
	e := readyFixture()
	body := exchange(PrivateRequest{Schema: 1, Cmd: "ready", TxID: e.TxID, Challenge: strings.Repeat("b", 32)})
	var reply ReadyReply
	if strictDecode(body, &reply) != nil || reply.Challenge != strings.Repeat("b", 32) || reply.Status != "ready" {
		t.Fatal("ready response lost framing")
	}
	body = exchange(PrivateRequest{Schema: 1, Cmd: "admit", TxID: e.TxID, OperationalCommitID: "oc1_" + strings.Repeat("c", 32)})
	if strings.Contains(string(body), "SYNTHETIC") || strings.Contains(string(body), "/private") || !strings.Contains(string(body), "refused") {
		t.Fatal("raw failure reached private wire")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
