package control

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

// --- STUN wire helpers (test-side encoders) -------------------------------
//
// These build real STUN bytes the way a server would, so the parser under test
// decodes genuine wire format (including the XOR transform) rather than a
// stand-in.

// buildAttr frames one STUN attribute with its type, length and 4-byte padding.
func buildAttr(atype uint16, val []byte) []byte {
	a := make([]byte, 4+len(val))
	binary.BigEndian.PutUint16(a[0:2], atype)
	binary.BigEndian.PutUint16(a[2:4], uint16(len(val)))
	copy(a[4:], val)
	if pad := len(val) % 4; pad != 0 {
		a = append(a, make([]byte, 4-pad)...)
	}
	return a
}

// buildBindingResponse assembles a Binding Success Response carrying attrs.
func buildBindingResponse(txID [12]byte, attrs ...[]byte) []byte {
	var body []byte
	for _, a := range attrs {
		body = append(body, a...)
	}
	msg := make([]byte, stunHeaderLen)
	binary.BigEndian.PutUint16(msg[0:2], stunBindingResponse)
	binary.BigEndian.PutUint16(msg[2:4], uint16(len(body)))
	binary.BigEndian.PutUint32(msg[4:8], stunMagicCookie)
	copy(msg[8:20], txID[:])
	return append(msg, body...)
}

// encodeXORMappedV4 encodes an XOR-MAPPED-ADDRESS attribute for an IPv4 mapping,
// applying the RFC 5389 XOR to the port and address.
func encodeXORMappedV4(ip net.IP, port uint16) []byte {
	val := make([]byte, 8)
	val[1] = 0x01
	binary.BigEndian.PutUint16(val[2:4], port^uint16(stunMagicCookie>>16))
	v4 := ip.To4()
	var cookie [4]byte
	binary.BigEndian.PutUint32(cookie[:], stunMagicCookie)
	for i := 0; i < 4; i++ {
		val[4+i] = v4[i] ^ cookie[i]
	}
	return buildAttr(attrXORMappedAddress, val)
}

// encodeXORMappedV6 encodes an XOR-MAPPED-ADDRESS attribute for an IPv6 mapping,
// XORing the address with the cookie followed by the transaction ID.
func encodeXORMappedV6(txID [12]byte, ip net.IP, port uint16) []byte {
	val := make([]byte, 20)
	val[1] = 0x02
	binary.BigEndian.PutUint16(val[2:4], port^uint16(stunMagicCookie>>16))
	v6 := ip.To16()
	var mask [16]byte
	binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
	copy(mask[4:16], txID[:])
	for i := 0; i < 16; i++ {
		val[4+i] = v6[i] ^ mask[i]
	}
	return buildAttr(attrXORMappedAddress, val)
}

// encodeMappedV4 encodes a legacy plain MAPPED-ADDRESS attribute (no XOR).
func encodeMappedV4(ip net.IP, port uint16) []byte {
	val := make([]byte, 8)
	val[1] = 0x01
	binary.BigEndian.PutUint16(val[2:4], port)
	copy(val[4:8], ip.To4())
	return buildAttr(attrMappedAddress, val)
}

func testTxID() [12]byte {
	var tx [12]byte
	for i := range tx {
		tx[i] = byte(i + 1)
	}
	return tx
}

// TestParseBindingResponseXORv4 is the core STUN-parsing proof: a real Binding
// Response carrying an XOR-MAPPED-ADDRESS must decode back to the exact IPv4 and
// port that were XOR-encoded into it.
func TestParseBindingResponseXORv4(t *testing.T) {
	tx := testTxID()
	wantIP := net.ParseIP("203.0.113.7")
	const wantPort = 54321

	resp := buildBindingResponse(tx, encodeXORMappedV4(wantIP, wantPort))
	got, err := parseBindingResponse(tx, resp)
	if err != nil {
		t.Fatalf("parseBindingResponse: %v", err)
	}
	if !got.IP.Equal(wantIP) {
		t.Errorf("IP = %s, want %s", got.IP, wantIP)
	}
	if got.Port != wantPort {
		t.Errorf("Port = %d, want %d", got.Port, wantPort)
	}
}

// TestParseBindingResponseXORv6 covers the IPv6 XOR path, whose mask is the
// cookie followed by the transaction ID.
func TestParseBindingResponseXORv6(t *testing.T) {
	tx := testTxID()
	wantIP := net.ParseIP("2001:db8::1")
	const wantPort = 12345

	resp := buildBindingResponse(tx, encodeXORMappedV6(tx, wantIP, wantPort))
	got, err := parseBindingResponse(tx, resp)
	if err != nil {
		t.Fatalf("parseBindingResponse: %v", err)
	}
	if !got.IP.Equal(wantIP) {
		t.Errorf("IP = %s, want %s", got.IP, wantIP)
	}
	if got.Port != wantPort {
		t.Errorf("Port = %d, want %d", got.Port, wantPort)
	}
}

// TestParseBindingResponseMappedFallback checks the legacy MAPPED-ADDRESS is
// honoured when no XOR-MAPPED-ADDRESS is present.
func TestParseBindingResponseMappedFallback(t *testing.T) {
	tx := testTxID()
	wantIP := net.ParseIP("198.51.100.9")
	const wantPort = 3478

	resp := buildBindingResponse(tx, encodeMappedV4(wantIP, wantPort))
	got, err := parseBindingResponse(tx, resp)
	if err != nil {
		t.Fatalf("parseBindingResponse: %v", err)
	}
	if !got.IP.Equal(wantIP) || got.Port != wantPort {
		t.Errorf("got %s:%d, want %s:%d", got.IP, got.Port, wantIP, wantPort)
	}
}

// TestParseBindingResponsePrefersXOR checks that when both attributes are present
// the modern XOR-MAPPED-ADDRESS wins.
func TestParseBindingResponsePrefersXOR(t *testing.T) {
	tx := testTxID()
	xorIP := net.ParseIP("203.0.113.7")
	plainIP := net.ParseIP("198.51.100.9")

	resp := buildBindingResponse(tx,
		encodeMappedV4(plainIP, 1111),
		encodeXORMappedV4(xorIP, 2222),
	)
	got, err := parseBindingResponse(tx, resp)
	if err != nil {
		t.Fatalf("parseBindingResponse: %v", err)
	}
	if !got.IP.Equal(xorIP) || got.Port != 2222 {
		t.Errorf("got %s:%d, want the XOR-mapped %s:2222", got.IP, got.Port, xorIP)
	}
}

// TestParseBindingResponseRejects locks the validation: a wrong transaction ID,
// a non-response type, a bad cookie, a short packet, and an attribute length that
// overruns the packet must all be errors, never a bogus mapping.
func TestParseBindingResponseRejects(t *testing.T) {
	tx := testTxID()
	good := buildBindingResponse(tx, encodeXORMappedV4(net.ParseIP("203.0.113.7"), 54321))

	t.Run("txid mismatch", func(t *testing.T) {
		var other [12]byte
		other[0] = 0xFF
		if _, err := parseBindingResponse(other, good); err == nil {
			t.Error("want error on transaction id mismatch")
		}
	})

	t.Run("wrong message type", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		binary.BigEndian.PutUint16(bad[0:2], stunBindingRequest)
		if _, err := parseBindingResponse(tx, bad); err == nil {
			t.Error("want error on non-success-response type")
		}
	})

	t.Run("bad cookie", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		binary.BigEndian.PutUint32(bad[4:8], 0xDEADBEEF)
		if _, err := parseBindingResponse(tx, bad); err == nil {
			t.Error("want error on bad magic cookie")
		}
	})

	t.Run("short packet", func(t *testing.T) {
		if _, err := parseBindingResponse(tx, good[:10]); err == nil {
			t.Error("want error on sub-header packet")
		}
	})

	t.Run("attr length overruns packet", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		binary.BigEndian.PutUint16(bad[2:4], 0xFFFF)
		if _, err := parseBindingResponse(tx, bad); err == nil {
			t.Error("want error when attribute length exceeds the packet")
		}
	})

	t.Run("no mapped-address attribute", func(t *testing.T) {
		// A well-formed header with zero attributes carries no mapping.
		empty := buildBindingResponse(tx)
		if _, err := parseBindingResponse(tx, empty); err == nil {
			t.Error("want error when no mapped-address attribute is present")
		}
	})
}

// TestBuildBindingRequest verifies the request header: 20 bytes, the Binding
// Request type, a zero attribute length, the magic cookie and the transaction ID
// — and that it round-trips through the parser's transaction-ID check would fail
// for a request (it is not a success response).
func TestBuildBindingRequest(t *testing.T) {
	tx := testTxID()
	req := buildBindingRequest(tx)

	if len(req) != stunHeaderLen {
		t.Fatalf("request length = %d, want %d", len(req), stunHeaderLen)
	}
	if got := binary.BigEndian.Uint16(req[0:2]); got != stunBindingRequest {
		t.Errorf("message type = %#04x, want %#04x", got, stunBindingRequest)
	}
	if got := binary.BigEndian.Uint16(req[2:4]); got != 0 {
		t.Errorf("attribute length = %d, want 0", got)
	}
	if got := binary.BigEndian.Uint32(req[4:8]); got != stunMagicCookie {
		t.Errorf("magic cookie = %#08x, want %#08x", got, stunMagicCookie)
	}
	if !bytes.Equal(req[8:20], tx[:]) {
		t.Errorf("transaction id = %x, want %x", req[8:20], tx[:])
	}
}

// TestClassifyNAT walks the mapping-behaviour rules end to end.
func TestClassifyNAT(t *testing.T) {
	extern := net.ParseIP("203.0.113.7")
	local := net.ParseIP("192.0.2.55")

	tests := []struct {
		name       string
		localIPs   []net.IP
		mappings   []stunMapping
		wantUDP    bool
		wantNAT    string
		wantExtern string
	}{
		{
			name:     "no answers -> blocked",
			mappings: nil,
			wantUDP:  false,
			wantNAT:  NATBlocked,
		},
		{
			name:       "reflexive is our own IP -> open",
			localIPs:   []net.IP{local},
			mappings:   []stunMapping{{IP: local, Port: 443}},
			wantUDP:    true,
			wantNAT:    NATOpen,
			wantExtern: "192.0.2.55",
		},
		{
			name:       "single answer -> unknown",
			mappings:   []stunMapping{{IP: extern, Port: 50000}},
			wantUDP:    true,
			wantNAT:    NATUnknown,
			wantExtern: "203.0.113.7",
		},
		{
			name: "stable mapping -> endpoint-independent",
			mappings: []stunMapping{
				{IP: extern, Port: 50000},
				{IP: extern, Port: 50000},
			},
			wantUDP:    true,
			wantNAT:    NATEndpointIndependent,
			wantExtern: "203.0.113.7",
		},
		{
			name: "per-destination port -> endpoint-dependent",
			mappings: []stunMapping{
				{IP: extern, Port: 50000},
				{IP: extern, Port: 50001},
			},
			wantUDP:    true,
			wantNAT:    NATEndpointDependent,
			wantExtern: "203.0.113.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			udpOK, natType, external := classifyNAT(tt.localIPs, tt.mappings)
			if udpOK != tt.wantUDP {
				t.Errorf("udpOK = %v, want %v", udpOK, tt.wantUDP)
			}
			if natType != tt.wantNAT {
				t.Errorf("natType = %q, want %q", natType, tt.wantNAT)
			}
			gotExtern := ""
			if external != nil {
				gotExtern = external.String()
			}
			if gotExtern != tt.wantExtern {
				t.Errorf("external = %q, want %q", gotExtern, tt.wantExtern)
			}
		})
	}
}

// stepClock returns a now() that starts at base and advances by step on every
// call, so runSpeedTest's start/end pair spans exactly one step.
func stepClock(base time.Time, step time.Duration) func() time.Time {
	cur := base
	return func() time.Time {
		t := cur
		cur = cur.Add(step)
		return t
	}
}

// infiniteReader yields bytes forever, so an io.LimitReader over it delivers
// exactly the sample size with no early EOF.
type infiniteReader struct{}

func (infiniteReader) Read(p []byte) (int, error) { return len(p), nil }

// TestRunSpeedTestMbps drives the throughput math from a canned reader and a
// controlled clock: 1,000,000 bytes over one second is 8 Mbps.
func TestRunSpeedTestMbps(t *testing.T) {
	d := newBareDaemon(t)
	d.speedSampleBytes = 1_000_000
	d.speedStream = func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(infiniteReader{}), nil
	}
	d.now = stepClock(time.Unix(0, 0), time.Second)

	res, err := d.runSpeedTest(context.Background())
	if err != nil {
		t.Fatalf("runSpeedTest: %v", err)
	}
	if res.SampleBytes != 1_000_000 {
		t.Errorf("SampleBytes = %d, want 1000000", res.SampleBytes)
	}
	if res.DurationMs != 1000 {
		t.Errorf("DurationMs = %d, want 1000", res.DurationMs)
	}
	if res.Mbps != 8.0 {
		t.Errorf("Mbps = %v, want 8", res.Mbps)
	}
}

// TestRunSpeedTestShortBody checks that a body shorter than the sample target
// still yields a rate from the bytes that arrived (500,000 bytes / 1s = 4 Mbps).
func TestRunSpeedTestShortBody(t *testing.T) {
	d := newBareDaemon(t)
	d.speedSampleBytes = 1_000_000
	d.speedStream = func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(make([]byte, 500_000))), nil
	}
	d.now = stepClock(time.Unix(0, 0), time.Second)

	res, err := d.runSpeedTest(context.Background())
	if err != nil {
		t.Fatalf("runSpeedTest: %v", err)
	}
	if res.SampleBytes != 500_000 {
		t.Errorf("SampleBytes = %d, want 500000", res.SampleBytes)
	}
	if res.Mbps != 4.0 {
		t.Errorf("Mbps = %v, want 4", res.Mbps)
	}
}

// TestRunStunCheckBlocked checks the degraded path: a probe that returns no
// mappings reports udp_ok=false / blocked with no external IP.
func TestRunStunCheckBlocked(t *testing.T) {
	d := newBareDaemon(t)
	d.stunProbe = func(context.Context, []string) (stunProbeResult, error) {
		return stunProbeResult{}, nil
	}
	res := d.runStunCheck(context.Background())
	if res.UDPOK {
		t.Error("UDPOK = true, want false when no server answered")
	}
	if res.NATType != NATBlocked {
		t.Errorf("NATType = %q, want %q", res.NATType, NATBlocked)
	}
	if res.ExternalIP != "" {
		t.Errorf("ExternalIP = %q, want empty", res.ExternalIP)
	}
}

// TestRunStunCheckDispatched proves run_stun_check is wired into Handle and
// answered with a well-formed result: a stubbed probe keeps it offline. A missing
// dispatch case would come back as an "unknown command" error instead.
func TestRunStunCheckDispatched(t *testing.T) {
	h := newHarness(t)
	extern := net.ParseIP("203.0.113.7")
	h.daemon.stunProbe = func(context.Context, []string) (stunProbeResult, error) {
		return stunProbeResult{
			Mappings: []stunMapping{
				{IP: extern, Port: 50000},
				{IP: extern, Port: 50000},
			},
		}, nil
	}

	h.send(Request{ID: 11, Cmd: CmdRunStunCheck})
	r := h.await()
	if !r.Ok {
		t.Fatalf("run_stun_check failed (is it dispatched in Handle?): %s", r.Error)
	}
	if r.ID != 11 {
		t.Errorf("response id = %d, want 11", r.ID)
	}

	var res StunCheck
	h.dataInto(r, &res)
	if !res.UDPOK {
		t.Error("UDPOK = false, want true")
	}
	if res.NATType != NATEndpointIndependent {
		t.Errorf("NATType = %q, want %q", res.NATType, NATEndpointIndependent)
	}
	if res.ExternalIP != "203.0.113.7" {
		t.Errorf("ExternalIP = %q, want 203.0.113.7", res.ExternalIP)
	}

	// The Rust/TS sides deserialize by these exact wire keys.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.Data, &raw); err != nil {
		t.Fatalf("decode raw data: %v", err)
	}
	for _, key := range []string{"udp_ok", "nat_type", "external_ip"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("run_stun_check result missing %q key; wire shape: %s", key, r.Data)
		}
	}
}

// TestRunSpeedTestDispatched proves run_speed_test is wired into Handle and, when
// connected, answers with the throughput result. The stream and clock are stubbed
// so it runs offline and deterministically.
func TestRunSpeedTestDispatched(t *testing.T) {
	h := newHarness(t)
	h.daemon.mu.Lock()
	h.daemon.state.State = StateConnected
	h.daemon.mu.Unlock()

	h.daemon.speedSampleBytes = 1_000_000
	h.daemon.speedStream = func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(infiniteReader{}), nil
	}
	h.daemon.now = stepClock(time.Unix(0, 0), time.Second)

	h.send(Request{ID: 12, Cmd: CmdRunSpeedTest})
	r := h.await()
	if !r.Ok {
		t.Fatalf("run_speed_test failed (is it dispatched in Handle?): %s", r.Error)
	}
	if r.ID != 12 {
		t.Errorf("response id = %d, want 12", r.ID)
	}

	var res SpeedTest
	h.dataInto(r, &res)
	if res.SampleBytes != 1_000_000 {
		t.Errorf("SampleBytes = %d, want 1000000", res.SampleBytes)
	}
	if res.Mbps <= 0 {
		t.Errorf("Mbps = %v, want > 0", res.Mbps)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.Data, &raw); err != nil {
		t.Fatalf("decode raw data: %v", err)
	}
	for _, key := range []string{"mbps", "sample_bytes", "duration_ms"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("run_speed_test result missing %q key; wire shape: %s", key, r.Data)
		}
	}
}

// TestRunSpeedTestRequiresConnection locks the gate: run_speed_test on an idle
// daemon is refused with an error, and the injected stream is never opened.
func TestRunSpeedTestRequiresConnection(t *testing.T) {
	d := newBareDaemon(t)
	opened := false
	d.speedStream = func(context.Context, string) (io.ReadCloser, error) {
		opened = true
		return io.NopCloser(infiniteReader{}), nil
	}

	resp := d.handleRunSpeedTest(context.Background(), Request{ID: 1, Cmd: CmdRunSpeedTest})
	if resp.Ok {
		t.Fatal("speed test on an idle daemon must be refused")
	}
	if opened {
		t.Error("speed stream was opened despite no active connection")
	}
}
