package subscription

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testRR is a resource record for the wire-response builder below.
type testRR struct {
	name  string
	a     net.IP // when set: an A or AAAA record
	cname string // when set: a CNAME record
}

// buildDNSResponse assembles a wire-format DNS response. It is an independent
// encoder (the production code only decodes responses) so it also cross-checks
// the decoder. Names are written uncompressed; compression is exercised
// separately by TestReadNameCompression.
func buildDNSResponse(question string, qtype uint16, rcode uint16, rrs []testRR) []byte {
	var msg []byte
	msg = binary.BigEndian.AppendUint16(msg, 0)                   // ID
	msg = binary.BigEndian.AppendUint16(msg, 0x8000|0x0100|rcode) // QR + RD + rcode
	msg = binary.BigEndian.AppendUint16(msg, 1)                   // QDCOUNT
	msg = binary.BigEndian.AppendUint16(msg, uint16(len(rrs)))    // ANCOUNT
	msg = binary.BigEndian.AppendUint16(msg, 0)                   // NSCOUNT
	msg = binary.BigEndian.AppendUint16(msg, 0)                   // ARCOUNT

	qname, _ := encodeName(question)
	msg = append(msg, qname...)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, dnsClassIN)

	for _, rr := range rrs {
		name, _ := encodeName(rr.name)
		msg = append(msg, name...)
		switch {
		case rr.cname != "":
			target, _ := encodeName(rr.cname)
			msg = binary.BigEndian.AppendUint16(msg, dnsTypeCNAME)
			msg = binary.BigEndian.AppendUint16(msg, dnsClassIN)
			msg = binary.BigEndian.AppendUint32(msg, 60)
			msg = binary.BigEndian.AppendUint16(msg, uint16(len(target)))
			msg = append(msg, target...)
		case rr.a.To4() != nil:
			v4 := rr.a.To4()
			msg = binary.BigEndian.AppendUint16(msg, dnsTypeA)
			msg = binary.BigEndian.AppendUint16(msg, dnsClassIN)
			msg = binary.BigEndian.AppendUint32(msg, 60)
			msg = binary.BigEndian.AppendUint16(msg, net.IPv4len)
			msg = append(msg, v4...)
		default:
			v6 := rr.a.To16()
			msg = binary.BigEndian.AppendUint16(msg, dnsTypeAAAA)
			msg = binary.BigEndian.AppendUint16(msg, dnsClassIN)
			msg = binary.BigEndian.AppendUint32(msg, 60)
			msg = binary.BigEndian.AppendUint16(msg, net.IPv6len)
			msg = append(msg, v6...)
		}
	}
	return msg
}

func TestParseDNSResponseA(t *testing.T) {
	wire := buildDNSResponse("sub.example.com", dnsTypeA, 0, []testRR{
		{name: "sub.example.com", a: net.IPv4(203, 0, 113, 7)},
	})
	ips, err := parseDNSResponse(wire, "sub.example.com", dnsTypeA)
	if err != nil {
		t.Fatalf("parseDNSResponse() error = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(203, 0, 113, 7)) {
		t.Fatalf("got %v, want [203.0.113.7]", ips)
	}
}

func TestParseDNSResponseAAAA(t *testing.T) {
	want := net.ParseIP("2001:db8::1")
	wire := buildDNSResponse("sub.example.com", dnsTypeAAAA, 0, []testRR{
		{name: "sub.example.com", a: want},
	})
	ips, err := parseDNSResponse(wire, "sub.example.com", dnsTypeAAAA)
	if err != nil {
		t.Fatalf("parseDNSResponse() error = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(want) {
		t.Fatalf("got %v, want [%v]", ips, want)
	}
}

func TestParseDNSResponseCNAMEChain(t *testing.T) {
	// sub.example.com -> edge.cdn.net -> A. The A record's owner is the CNAME
	// target, not the queried name; the parser must follow the chain.
	wire := buildDNSResponse("sub.example.com", dnsTypeA, 0, []testRR{
		{name: "sub.example.com", cname: "edge.cdn.net"},
		{name: "edge.cdn.net", a: net.IPv4(198, 51, 100, 9)},
	})
	ips, err := parseDNSResponse(wire, "sub.example.com", dnsTypeA)
	if err != nil {
		t.Fatalf("parseDNSResponse() error = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(198, 51, 100, 9)) {
		t.Fatalf("got %v, want [198.51.100.9]", ips)
	}
}

func TestParseDNSResponseCaseInsensitive(t *testing.T) {
	// Resolvers may echo the question name in a different case (0x20 encoding).
	wire := buildDNSResponse("Sub.Example.COM", dnsTypeA, 0, []testRR{
		{name: "SUB.EXAMPLE.com", a: net.IPv4(203, 0, 113, 1)},
	})
	ips, err := parseDNSResponse(wire, "sub.example.com", dnsTypeA)
	if err != nil {
		t.Fatalf("parseDNSResponse() error = %v", err)
	}
	if len(ips) != 1 {
		t.Fatalf("got %v, want one address", ips)
	}
}

func TestParseDNSResponseWrongQName(t *testing.T) {
	// An A record for an unrelated name, not linked by a CNAME, must be ignored.
	wire := buildDNSResponse("sub.example.com", dnsTypeA, 0, []testRR{
		{name: "attacker.example", a: net.IPv4(10, 0, 0, 1)},
	})
	_, err := parseDNSResponse(wire, "sub.example.com", dnsTypeA)
	if err == nil {
		t.Fatal("parseDNSResponse() error = nil, want error for unrelated record")
	}
}

func TestParseDNSResponseNXDOMAIN(t *testing.T) {
	wire := buildDNSResponse("nope.example.com", dnsTypeA, 3 /* NXDOMAIN */, nil)
	_, err := parseDNSResponse(wire, "nope.example.com", dnsTypeA)
	if err == nil || !strings.Contains(err.Error(), "rcode") {
		t.Fatalf("parseDNSResponse() error = %v, want rcode error", err)
	}
}

func TestParseDNSResponseGarbage(t *testing.T) {
	for _, in := range [][]byte{
		nil,
		{0x00},
		{0xde, 0xad, 0xbe, 0xef},
		bytes12(0x8000, 1, 1), // claims one answer but body is truncated
		append(header12(0x8000, 1, 1), 0xff, 0xff, 0xff), // bad name/labels
	} {
		// Must return an error, never panic.
		if _, err := parseDNSResponse(in, "sub.example.com", dnsTypeA); err == nil {
			t.Errorf("parseDNSResponse(% x) error = nil, want error", in)
		}
	}
}

func TestReadNameCompression(t *testing.T) {
	// Build a message where the answer owner name is a compression pointer back
	// to the question name at offset 12.
	msg := header12(0x8000, 1, 1)
	qname, _ := encodeName("host.example")
	msg = append(msg, qname...)                           // question name at offset 12
	msg = binary.BigEndian.AppendUint16(msg, dnsTypeA)    // qtype
	msg = binary.BigEndian.AppendUint16(msg, dnsClassIN)  // qclass
	msg = append(msg, 0xC0, 0x0C)                         // answer name -> pointer to offset 12
	msg = binary.BigEndian.AppendUint16(msg, dnsTypeA)    // type
	msg = binary.BigEndian.AppendUint16(msg, dnsClassIN)  // class
	msg = binary.BigEndian.AppendUint32(msg, 60)          // ttl
	msg = binary.BigEndian.AppendUint16(msg, net.IPv4len) // rdlength
	msg = append(msg, 192, 0, 2, 44)                      // A 192.0.2.44

	ips, err := parseDNSResponse(msg, "host.example", dnsTypeA)
	if err != nil {
		t.Fatalf("parseDNSResponse() error = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(192, 0, 2, 44)) {
		t.Fatalf("got %v, want [192.0.2.44]", ips)
	}
}

func TestReadNamePointerLoopRejected(t *testing.T) {
	// A pointer at offset 12 that points to itself must be rejected, not loop.
	msg := header12(0x8000, 1, 1)
	msg = append(msg, 0xC0, 0x0C) // self-referential pointer at offset 12
	if _, _, err := readName(msg, 12); err == nil {
		t.Fatal("readName() error = nil, want error for self pointer")
	}
}

func TestEncodeDNSQueryRoundTrip(t *testing.T) {
	q, err := encodeDNSQuery("a.b.example", dnsTypeA)
	if err != nil {
		t.Fatalf("encodeDNSQuery() error = %v", err)
	}
	// Question name begins at offset 12 and must decode back to the host.
	name, _, err := readName(q, 12)
	if err != nil {
		t.Fatalf("readName() error = %v", err)
	}
	if name != "a.b.example" {
		t.Fatalf("round-trip name = %q, want a.b.example", name)
	}
}

func TestEncodeNameRejectsBadInput(t *testing.T) {
	if _, err := encodeName(""); err == nil {
		t.Error("encodeName(\"\") error = nil, want error")
	}
	if _, err := encodeName("a." + strings.Repeat("x", 64) + ".b"); err == nil {
		t.Error("encodeName(oversized label) error = nil, want error")
	}
}

// newFakeDoHServer starts a TLS DoH resolver that answers every query with the
// records returned by answer. The returned pool trusts its certificate.
func newFakeDoHServer(t *testing.T, answer func(host string, qtype uint16) []testRR) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		if err != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		host, qtype, err := decodeQuestion(raw)
		if err != nil {
			http.Error(w, "bad question", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", dohContentType)
		w.Write(buildDNSResponse(host, qtype, 0, answer(host, qtype)))
	}))
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, pool
}

// decodeQuestion extracts the queried host and type from a wire query.
func decodeQuestion(msg []byte) (string, uint16, error) {
	name, next, err := readName(msg, 12)
	if err != nil {
		return "", 0, err
	}
	qtype := binary.BigEndian.Uint16(msg[next : next+2])
	return name, qtype, nil
}

func TestDoHResolverViaFakeServer(t *testing.T) {
	srv, pool := newFakeDoHServer(t, func(host string, qtype uint16) []testRR {
		if qtype == dnsTypeA {
			return []testRR{{name: host, a: net.IPv4(198, 51, 100, 23)}}
		}
		return nil // no AAAA
	})
	ep := DoHEndpoint{URL: srv.URL + "/dns-query", Addr: serverAddr(srv)}
	r := newDoHResolver([]DoHEndpoint{ep}, pool)

	ips, err := r.resolve(context.Background(), "sub.example.com")
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(198, 51, 100, 23)) {
		t.Fatalf("resolve() = %v, want [198.51.100.23]", ips)
	}
}

func TestDoHResolverIPLiteralSkipsQuery(t *testing.T) {
	// An IP literal needs no resolver; an endpoint that would fail proves it is
	// never consulted.
	r := newDoHResolver([]DoHEndpoint{{URL: "https://unused.invalid/dns-query", Addr: "127.0.0.1:1"}}, nil)
	ips, err := r.resolve(context.Background(), "203.0.113.5")
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(203, 0, 113, 5)) {
		t.Fatalf("resolve() = %v, want [203.0.113.5]", ips)
	}
}

func TestDoHResolverAllEndpointsDown(t *testing.T) {
	// Both endpoints point at closed loopback ports: resolve must fail cleanly
	// with a combined error, not hang or panic.
	r := newDoHResolver([]DoHEndpoint{
		{URL: "https://a.invalid/dns-query", Addr: "127.0.0.1:1"},
		{URL: "https://b.invalid/dns-query", Addr: "127.0.0.1:1"},
	}, nil)
	_, err := r.resolve(context.Background(), "sub.example.com")
	if err == nil || !strings.Contains(err.Error(), "could not resolve") {
		t.Fatalf("resolve() error = %v, want could-not-resolve error", err)
	}
}

// --- small wire helpers ---

func header12(flags, qd, an uint16) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 0)     // ID
	b = binary.BigEndian.AppendUint16(b, flags) // flags
	b = binary.BigEndian.AppendUint16(b, qd)    // QDCOUNT
	b = binary.BigEndian.AppendUint16(b, an)    // ANCOUNT
	b = binary.BigEndian.AppendUint16(b, 0)     // NSCOUNT
	b = binary.BigEndian.AppendUint16(b, 0)     // ARCOUNT
	return b
}

func bytes12(flags, qd, an uint16) []byte { return header12(flags, qd, an) }

func serverAddr(srv *httptest.Server) string {
	return srv.Listener.Addr().String()
}
