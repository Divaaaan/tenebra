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

	"github.com/Divaaaan/tenebra/core/dnswire"
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

	qname, _ := dnswire.EncodeName(question)
	msg = append(msg, qname...)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, dnswire.ClassIN)

	for _, rr := range rrs {
		name, _ := dnswire.EncodeName(rr.name)
		msg = append(msg, name...)
		switch {
		case rr.cname != "":
			target, _ := dnswire.EncodeName(rr.cname)
			msg = binary.BigEndian.AppendUint16(msg, dnswire.TypeCNAME)
			msg = binary.BigEndian.AppendUint16(msg, dnswire.ClassIN)
			msg = binary.BigEndian.AppendUint32(msg, 60)
			msg = binary.BigEndian.AppendUint16(msg, uint16(len(target)))
			msg = append(msg, target...)
		case rr.a.To4() != nil:
			v4 := rr.a.To4()
			msg = binary.BigEndian.AppendUint16(msg, dnswire.TypeA)
			msg = binary.BigEndian.AppendUint16(msg, dnswire.ClassIN)
			msg = binary.BigEndian.AppendUint32(msg, 60)
			msg = binary.BigEndian.AppendUint16(msg, net.IPv4len)
			msg = append(msg, v4...)
		default:
			v6 := rr.a.To16()
			msg = binary.BigEndian.AppendUint16(msg, dnswire.TypeAAAA)
			msg = binary.BigEndian.AppendUint16(msg, dnswire.ClassIN)
			msg = binary.BigEndian.AppendUint32(msg, 60)
			msg = binary.BigEndian.AppendUint16(msg, net.IPv6len)
			msg = append(msg, v6...)
		}
	}
	return msg
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
	name, next, err := dnswire.ReadName(msg, 12)
	if err != nil {
		return "", 0, err
	}
	qtype := binary.BigEndian.Uint16(msg[next : next+2])
	return name, qtype, nil
}

func TestDoHResolverViaFakeServer(t *testing.T) {
	srv, pool := newFakeDoHServer(t, func(host string, qtype uint16) []testRR {
		if qtype == dnswire.TypeA {
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
