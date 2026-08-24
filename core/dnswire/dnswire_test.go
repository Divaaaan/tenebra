package dnswire

import (
	"encoding/binary"
	"net"
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

	qname, _ := EncodeName(question)
	msg = append(msg, qname...)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, ClassIN)

	for _, rr := range rrs {
		name, _ := EncodeName(rr.name)
		msg = append(msg, name...)
		switch {
		case rr.cname != "":
			target, _ := EncodeName(rr.cname)
			msg = binary.BigEndian.AppendUint16(msg, TypeCNAME)
			msg = binary.BigEndian.AppendUint16(msg, ClassIN)
			msg = binary.BigEndian.AppendUint32(msg, 60)
			msg = binary.BigEndian.AppendUint16(msg, uint16(len(target)))
			msg = append(msg, target...)
		case rr.a.To4() != nil:
			v4 := rr.a.To4()
			msg = binary.BigEndian.AppendUint16(msg, TypeA)
			msg = binary.BigEndian.AppendUint16(msg, ClassIN)
			msg = binary.BigEndian.AppendUint32(msg, 60)
			msg = binary.BigEndian.AppendUint16(msg, net.IPv4len)
			msg = append(msg, v4...)
		default:
			v6 := rr.a.To16()
			msg = binary.BigEndian.AppendUint16(msg, TypeAAAA)
			msg = binary.BigEndian.AppendUint16(msg, ClassIN)
			msg = binary.BigEndian.AppendUint32(msg, 60)
			msg = binary.BigEndian.AppendUint16(msg, net.IPv6len)
			msg = append(msg, v6...)
		}
	}
	return msg
}

func TestParseResponseA(t *testing.T) {
	wire := buildDNSResponse("sub.example.com", TypeA, 0, []testRR{
		{name: "sub.example.com", a: net.IPv4(203, 0, 113, 7)},
	})
	ips, err := ParseResponse(wire, "sub.example.com", TypeA)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(203, 0, 113, 7)) {
		t.Fatalf("got %v, want [203.0.113.7]", ips)
	}
}

func TestParseResponseAAAA(t *testing.T) {
	want := net.ParseIP("2001:db8::1")
	wire := buildDNSResponse("sub.example.com", TypeAAAA, 0, []testRR{
		{name: "sub.example.com", a: want},
	})
	ips, err := ParseResponse(wire, "sub.example.com", TypeAAAA)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(want) {
		t.Fatalf("got %v, want [%v]", ips, want)
	}
}

func TestParseResponseCNAMEChain(t *testing.T) {
	// sub.example.com -> edge.cdn.net -> A. The A record's owner is the CNAME
	// target, not the queried name; the parser must follow the chain.
	wire := buildDNSResponse("sub.example.com", TypeA, 0, []testRR{
		{name: "sub.example.com", cname: "edge.cdn.net"},
		{name: "edge.cdn.net", a: net.IPv4(198, 51, 100, 9)},
	})
	ips, err := ParseResponse(wire, "sub.example.com", TypeA)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(198, 51, 100, 9)) {
		t.Fatalf("got %v, want [198.51.100.9]", ips)
	}
}

func TestParseResponseCaseInsensitive(t *testing.T) {
	// Resolvers may echo the question name in a different case (0x20 encoding).
	wire := buildDNSResponse("Sub.Example.COM", TypeA, 0, []testRR{
		{name: "SUB.EXAMPLE.com", a: net.IPv4(203, 0, 113, 1)},
	})
	ips, err := ParseResponse(wire, "sub.example.com", TypeA)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if len(ips) != 1 {
		t.Fatalf("got %v, want one address", ips)
	}
}

func TestParseResponseWrongQName(t *testing.T) {
	// An A record for an unrelated name, not linked by a CNAME, must be ignored.
	wire := buildDNSResponse("sub.example.com", TypeA, 0, []testRR{
		{name: "attacker.example", a: net.IPv4(10, 0, 0, 1)},
	})
	_, err := ParseResponse(wire, "sub.example.com", TypeA)
	if err == nil {
		t.Fatal("ParseResponse() error = nil, want error for unrelated record")
	}
}

func TestParseResponseNXDOMAIN(t *testing.T) {
	wire := buildDNSResponse("nope.example.com", TypeA, 3 /* NXDOMAIN */, nil)
	_, err := ParseResponse(wire, "nope.example.com", TypeA)
	if err == nil || !strings.Contains(err.Error(), "rcode") {
		t.Fatalf("ParseResponse() error = %v, want rcode error", err)
	}
}

func TestParseResponseGarbage(t *testing.T) {
	for _, in := range [][]byte{
		nil,
		{0x00},
		{0xde, 0xad, 0xbe, 0xef},
		header12(0x8000, 1, 1), // claims one answer but body is truncated
		append(header12(0x8000, 1, 1), 0xff, 0xff, 0xff), // bad name/labels
	} {
		// Must return an error, never panic.
		if _, err := ParseResponse(in, "sub.example.com", TypeA); err == nil {
			t.Errorf("ParseResponse(% x) error = nil, want error", in)
		}
	}
}

func TestReadNameCompression(t *testing.T) {
	// Build a message where the answer owner name is a compression pointer back
	// to the question name at offset 12.
	msg := header12(0x8000, 1, 1)
	qname, _ := EncodeName("host.example")
	msg = append(msg, qname...)                           // question name at offset 12
	msg = binary.BigEndian.AppendUint16(msg, TypeA)       // qtype
	msg = binary.BigEndian.AppendUint16(msg, ClassIN)     // qclass
	msg = append(msg, 0xC0, 0x0C)                         // answer name -> pointer to offset 12
	msg = binary.BigEndian.AppendUint16(msg, TypeA)       // type
	msg = binary.BigEndian.AppendUint16(msg, ClassIN)     // class
	msg = binary.BigEndian.AppendUint32(msg, 60)          // ttl
	msg = binary.BigEndian.AppendUint16(msg, net.IPv4len) // rdlength
	msg = append(msg, 192, 0, 2, 44)                      // A 192.0.2.44

	ips, err := ParseResponse(msg, "host.example", TypeA)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(192, 0, 2, 44)) {
		t.Fatalf("got %v, want [192.0.2.44]", ips)
	}
}

func TestReadNamePointerLoopRejected(t *testing.T) {
	// A pointer at offset 12 that points to itself must be rejected, not loop.
	msg := header12(0x8000, 1, 1)
	msg = append(msg, 0xC0, 0x0C) // self-referential pointer at offset 12
	if _, _, err := ReadName(msg, 12); err == nil {
		t.Fatal("ReadName() error = nil, want error for self pointer")
	}
}

func TestEncodeQueryRoundTrip(t *testing.T) {
	q, err := EncodeQuery("a.b.example", TypeA)
	if err != nil {
		t.Fatalf("EncodeQuery() error = %v", err)
	}
	// Question name begins at offset 12 and must decode back to the host.
	name, _, err := ReadName(q, 12)
	if err != nil {
		t.Fatalf("ReadName() error = %v", err)
	}
	if name != "a.b.example" {
		t.Fatalf("round-trip name = %q, want a.b.example", name)
	}
}

func TestEncodeNameRejectsBadInput(t *testing.T) {
	if _, err := EncodeName(""); err == nil {
		t.Error("EncodeName(\"\") error = nil, want error")
	}
	if _, err := EncodeName("a." + strings.Repeat("x", 64) + ".b"); err == nil {
		t.Error("EncodeName(oversized label) error = nil, want error")
	}
}

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
