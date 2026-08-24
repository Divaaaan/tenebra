package dnswire

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// answerFor builds the response a fake resolver sends back for a query: the same
// question, the records the table holds, and the query's own ID echoed — which
// is what a client checking for off-path forgeries insists on.
func answerFor(t *testing.T, query []byte, answers map[string][]net.IP) []byte {
	t.Helper()
	host, next, err := ReadName(query, 12)
	if err != nil {
		t.Errorf("fake resolver: read question: %v", err)
		return nil
	}
	qtype := binary.BigEndian.Uint16(query[next : next+2])

	var rrs []testRR
	for _, ip := range answers[host] {
		v4 := ip.To4() != nil
		if (qtype == TypeA) != v4 {
			continue
		}
		rrs = append(rrs, testRR{name: host, a: ip})
	}
	resp := buildDNSResponse(host, qtype, 0, rrs)
	copy(resp[:2], query[:2])
	return resp
}

// fakeUDPResolver starts a plain DNS resolver on loopback. forge, when set, is
// sent first with a mangled ID: an off-path forgery arriving before the real
// answer, which the client must ignore.
func fakeUDPResolver(t *testing.T, answers map[string][]net.IP, forge net.IP) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { pc.Close() })

	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			query := append([]byte(nil), buf[:n]...)
			if forge != nil {
				bogus := answerFor(t, query, map[string][]net.IP{mustHost(t, query): {forge}})
				bogus[1]++ // an ID that is not the one asked with
				pc.WriteTo(bogus, from)
			}
			pc.WriteTo(answerFor(t, query, answers), from)
		}
	}()
	return pc.LocalAddr().String()
}

// mustHost reads the queried name out of a wire query.
func mustHost(t *testing.T, query []byte) string {
	t.Helper()
	host, _, err := ReadName(query, 12)
	if err != nil {
		t.Errorf("fake resolver: read question: %v", err)
	}
	return host
}

// fakeTCPResolver starts a DNS resolver speaking the two-byte length framing.
func fakeTCPResolver(t *testing.T, answers map[string][]net.IP) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var length [2]byte
				if _, err := io.ReadFull(conn, length[:]); err != nil {
					return
				}
				query := make([]byte, binary.BigEndian.Uint16(length[:]))
				if _, err := io.ReadFull(conn, query); err != nil {
					return
				}
				resp := answerFor(t, query, answers)
				out := binary.BigEndian.AppendUint16(nil, uint16(len(resp)))
				conn.Write(append(out, resp...))
			}()
		}
	}()
	return ln.Addr().String()
}

// A resolver configured as plain DNS is what sing-box would be asking, so it is
// what this asks too — with a random ID, and deaf to answers carrying another.
func TestResolverOverUDP(t *testing.T) {
	addr := fakeUDPResolver(t, map[string][]net.IP{
		"node.example.com": {net.IPv4(198, 51, 100, 10)},
	}, nil)

	r, ok := NewResolver("udp://" + addr)
	if !ok {
		t.Fatal("NewResolver refused a plain resolver address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := r.LookupIP(ctx, "node.example.com")
	if err != nil {
		t.Fatalf("LookupIP: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(198, 51, 100, 10)) {
		t.Fatalf("LookupIP = %v, want [198.51.100.10]", ips)
	}
}

func TestResolverIgnoresAnAnswerWithTheWrongID(t *testing.T) {
	addr := fakeUDPResolver(t, map[string][]net.IP{
		"node.example.com": {net.IPv4(198, 51, 100, 10)},
	}, net.IPv4(10, 6, 6, 6))

	r, ok := NewResolver("udp://" + addr)
	if !ok {
		t.Fatal("NewResolver refused a plain resolver address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := r.LookupIP(ctx, "node.example.com")
	if err != nil {
		t.Fatalf("LookupIP: %v", err)
	}
	for _, ip := range ips {
		if ip.Equal(net.IPv4(10, 6, 6, 6)) {
			t.Fatalf("LookupIP = %v, took an answer whose ID was not the one asked with", ips)
		}
	}
	if len(ips) != 1 {
		t.Fatalf("LookupIP = %v, want the one real answer", ips)
	}
}

// A plain answer that does not fit in a datagram comes back with the truncation
// bit and nothing usable behind it; the rest of it exists only over TCP. Without
// the retry the name would look dead on exactly the resolvers that answer with
// many records.
func TestResolverRetriesATruncatedAnswerOverTCP(t *testing.T) {
	answers := map[string][]net.IP{"node.example.com": {net.IPv4(198, 51, 100, 10)}}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { pc.Close() })

	// The same port over TCP is where a truncated answer says to go.
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		t.Skipf("the udp port is not free over tcp: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			query := append([]byte(nil), buf[:n]...)
			truncated := answerFor(t, query, nil)
			binary.BigEndian.PutUint16(truncated[2:4], binary.BigEndian.Uint16(truncated[2:4])|0x0200)
			pc.WriteTo(truncated, from)
		}
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var length [2]byte
				if _, err := io.ReadFull(conn, length[:]); err != nil {
					return
				}
				query := make([]byte, binary.BigEndian.Uint16(length[:]))
				if _, err := io.ReadFull(conn, query); err != nil {
					return
				}
				resp := answerFor(t, query, answers)
				out := binary.BigEndian.AppendUint16(nil, uint16(len(resp)))
				conn.Write(append(out, resp...))
			}()
		}
	}()

	r, ok := NewResolver("udp://" + pc.LocalAddr().String())
	if !ok {
		t.Fatal("NewResolver refused a plain resolver address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := r.LookupIP(ctx, "node.example.com")
	if err != nil {
		t.Fatalf("LookupIP: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(198, 51, 100, 10)) {
		t.Fatalf("LookupIP = %v, want the answer the retry over tcp carried", ips)
	}
}

func TestResolverOverTCP(t *testing.T) {
	addr := fakeTCPResolver(t, map[string][]net.IP{
		"node.example.com": {net.IPv4(203, 0, 113, 9), net.ParseIP("2001:db8::9")},
	})

	r, ok := NewResolver("tcp://" + addr)
	if !ok {
		t.Fatal("NewResolver refused a tcp resolver address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := r.LookupIP(ctx, "node.example.com")
	if err != nil {
		t.Fatalf("LookupIP: %v", err)
	}
	// Both record types are asked at once and the answers joined: a node reached
	// over either family is a node the filter must leave alone.
	if len(ips) != 2 {
		t.Fatalf("LookupIP = %v, want both the A and the AAAA answer", ips)
	}
}

// The shipped default is DoH, so this is the path that actually runs.
func TestResolverOverDoH(t *testing.T) {
	answers := map[string][]net.IP{"node.example.com": {net.IPv4(198, 51, 100, 42)}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		query, err := base64.RawURLEncoding.DecodeString(req.URL.Query().Get("dns"))
		if err != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Write(answerFor(t, query, answers))
	}))
	t.Cleanup(srv.Close)

	r, ok := newResolver("https://"+srv.Listener.Addr().String()+"/dns-query", srv.Client())
	if !ok {
		t.Fatal("NewResolver refused a DoH address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := r.LookupIP(ctx, "node.example.com")
	if err != nil {
		t.Fatalf("LookupIP: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(198, 51, 100, 42)) {
		t.Fatalf("LookupIP = %v, want [198.51.100.42]", ips)
	}
}

// Saying "cannot" is the point: a caller that is told false falls back to the
// system resolver, where a caller handed a resolver that quietly asks somewhere
// else would believe an answer from a server the user never configured.
func TestNewResolverRefusesWhatItCannotSpeak(t *testing.T) {
	for _, addr := range []string{"", "   ", "quic://dns.adguard.com", "https://"} {
		if _, ok := NewResolver(addr); ok {
			t.Errorf("NewResolver(%q) accepted an address it cannot speak", addr)
		}
	}
	for _, addr := range []string{"77.88.8.8", "udp://8.8.8.8", "tcp://8.8.8.8", "tls://dns.quad9.net", "https://77.88.8.8/dns-query", "h3://dns.google/dns-query"} {
		if _, ok := NewResolver(addr); !ok {
			t.Errorf("NewResolver(%q) refused an address it can speak", addr)
		}
	}
}

// A node given as an IP needs no resolver: an endpoint that would fail proves it
// is never consulted.
func TestResolverIPLiteralSkipsTheQuery(t *testing.T) {
	r, ok := NewResolver("udp://127.0.0.1:1")
	if !ok {
		t.Fatal("NewResolver refused a plain resolver address")
	}
	ips, err := r.LookupIP(context.Background(), "203.0.113.5")
	if err != nil {
		t.Fatalf("LookupIP: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(203, 0, 113, 5)) {
		t.Fatalf("LookupIP = %v, want [203.0.113.5]", ips)
	}
}

// The caller's deadline is the only budget: this runs in front of a connect.
func TestResolverStopsWithTheContext(t *testing.T) {
	// A listener that accepts and says nothing; the query can only end on time.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
		}
	}()

	r, ok := NewResolver("tcp://" + ln.Addr().String())
	if !ok {
		t.Fatal("NewResolver refused a tcp resolver address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := r.LookupIP(ctx, "node.example.com"); err == nil {
		t.Fatal("LookupIP returned an answer from a resolver that sent none")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("LookupIP waited %v past a 150ms deadline", elapsed)
	}
}

func TestWithPort(t *testing.T) {
	for _, tc := range []struct{ host, port, want string }{
		{"77.88.8.8", portDoH, "77.88.8.8:443"},
		{"77.88.8.8:8443", portDoH, "77.88.8.8:8443"},
		{"dns.quad9.net", portDoT, "dns.quad9.net:853"},
		{"2001:db8::1", portPlain, "[2001:db8::1]:53"},
		{"[2001:db8::1]:5353", portPlain, "[2001:db8::1]:5353"},
	} {
		if got := withPort(tc.host, tc.port); got != tc.want {
			t.Errorf("withPort(%q, %q) = %q, want %q", tc.host, tc.port, got, tc.want)
		}
	}
}
